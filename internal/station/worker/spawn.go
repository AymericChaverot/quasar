package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// The parent's half: start a worker, hand it one call, answer what it asks
// for, and bound it from outside.
//
// Bounding a process from inside itself is the arrangement that does not work.
// A watchdog living in the process it watches only notices after the kernel
// has already picked a victim, and on a server running the dashboard the
// victim it picks may be the dashboard — the one process on the machine that
// must not die. Out here the killing is instant and safe, because the worker
// holds nothing worth losing.

// Limits bound one call. They come from the specification's table: a panel
// source gets seconds, an action gets a minute, a hook gets two.
type Limits struct {
	// Wall is what the worker is given. It interrupts itself at this, so an
	// ordinary runaway loop reports a timeout its author can read.
	Wall time.Duration

	// Grace is how long after Wall the parent waits before killing a worker
	// that ignored its own interrupt.
	Grace time.Duration

	// MaxMemoryBytes is the resident size above which the worker is killed.
	MaxMemoryBytes int64

	// MaxResultBytes caps the value a call may return.
	MaxResultBytes int
}

// DefaultLimits are the ones a panel source runs under.
func DefaultLimits() Limits {
	return Limits{
		Wall:           10 * time.Second,
		Grace:          2 * time.Second,
		MaxMemoryBytes: 128 << 20,
		MaxResultBytes: 1 << 20,
	}
}

// sampleEvery is how often the parent asks the operating system how large the
// worker has become. Often enough that an allocation storm is caught before
// the machine feels it, rarely enough to cost nothing.
const sampleEvery = 50 * time.Millisecond

// Broker performs a capability on the worker's behalf.
//
// This is where the permission model actually lives. The worker cannot do any
// of these things — it holds no socket, no disk and no network — so it asks,
// and the implementation of this interface is what decides whether the station
// declared that capability, performs it with privileges the worker does not
// have, and writes the audit entry.
type Broker interface {
	Do(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error)
}

// BrokerFunc adapts a function to Broker.
type BrokerFunc func(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error)

func (f BrokerFunc) Do(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
	return f(ctx, capability, args)
}

// DenyAll refuses every capability, naming the one that was asked for. It is
// what a station with no permissions block gets, and what the boundary is
// tested against.
func DenyAll() Broker {
	return BrokerFunc(func(_ context.Context, capability string, _ json.RawMessage) (json.RawMessage, error) {
		return nil, fmt.Errorf("this station has not been granted %s", capability)
	})
}

// Spawner is how to start a worker: the argv, and the environment it gets.
//
// The environment is empty unless a caller says otherwise. A worker needs
// nothing from it, and everything in the dashboard's own is something a
// station has no business reading.
type Spawner struct {
	Argv []string
	Env  []string
}

// Self spawns this binary in worker mode, which is what the dashboard does.
func Self() (Spawner, error) {
	exe, err := os.Executable()
	if err != nil {
		return Spawner{}, fmt.Errorf("finding this binary to re-execute: %w", err)
	}
	return Spawner{Argv: []string{exe, "station-worker"}, Env: []string{}}, nil
}

// Reasons a call ended without a value.
const (
	FailTimeout  = "timeout"
	FailMemory   = "memory"
	FailCrash    = "crash"
	FailProtocol = "protocol"
	FailSpawn    = "spawn"
	FailTooLarge = "too-large"
)

// Failure is a call that did not return, and which limit or accident ended it.
// It is a type rather than a string because the panel says one thing for a
// script that took too long and another for one that was killed for growing,
// and telling them apart is the difference between a fixable report and a
// shrug.
type Failure struct {
	Reason string
	Detail string
}

func (f *Failure) Error() string {
	switch f.Reason {
	case FailTimeout:
		return "the script ran longer than it is allowed to and was stopped"
	case FailMemory:
		return "the script used more memory than it is allowed to and was stopped"
	case FailTooLarge:
		return "the script returned more data than a panel may carry"
	case FailCrash:
		return "the script's process stopped without answering: " + f.Detail
	case FailSpawn:
		return "could not start the process the script runs in: " + f.Detail
	}
	return "the script's process misbehaved: " + f.Detail
}

// ScriptError is the script's own failure: it ran, and threw. Distinct from
// Failure because this one is the author's bug and reads like one.
type ScriptError struct{ Message string }

func (e *ScriptError) Error() string { return e.Message }

// Run starts a worker, gives it one call, answers what it asks for, and
// returns what it produced.
//
// Everything about it is disposable. The process dies at the end of this
// function whatever happened inside it, so nothing a station did survives into
// the next call, and anything it wants to remember has to have gone through
// quasar.store — which is to say, past the broker.
func Run(ctx context.Context, sp Spawner, call Call, lim Limits, b Broker) (json.RawMessage, error) {
	if len(sp.Argv) == 0 {
		return nil, &Failure{Reason: FailSpawn, Detail: "no command to run"}
	}
	call.WallMS = int(lim.Wall / time.Millisecond)
	call.MaxResultBytes = lim.MaxResultBytes
	call.MaxMemoryBytes = lim.MaxMemoryBytes

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, sp.Argv[0], sp.Argv[1:]...)
	cmd.Env = sp.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &Failure{Reason: FailSpawn, Detail: err.Error()}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &Failure{Reason: FailSpawn, Detail: err.Error()}
	}
	// Kept and shown rather than discarded: a worker that died before it could
	// say anything on the pipe usually said it here.
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, left: 8 << 10}

	if err := cmd.Start(); err != nil {
		return nil, &Failure{Reason: FailSpawn, Detail: err.Error()}
	}

	// Why the worker was killed, if it was. Written before the kill, so it is
	// always there by the time the pipe closes and the loop below notices.
	var killed atomic.Value
	kill := func(reason string) {
		killed.CompareAndSwap(nil, reason)
		_ = cmd.Process.Kill()
	}

	wall := time.AfterFunc(lim.Wall+lim.Grace, func() { kill(FailTimeout) })
	defer wall.Stop()

	done := make(chan struct{})
	defer close(done)
	go watchMemory(cmd.Process.Pid, lim.MaxMemoryBytes, done, func() { kill(FailMemory) })

	value, callErr := exchange(ctx, NewConn(stdout, stdin), call, lim, b)

	// The worker is finished with either way; closing its input is what tells
	// a well-behaved one to stop, and the kill is for the rest.
	stdin.Close()
	io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()

	if reason, ok := killed.Load().(string); ok {
		return nil, &Failure{Reason: reason}
	}
	if callErr != nil {
		var f *Failure
		if errors.As(callErr, &f) && f.Reason == FailCrash && waitErr != nil {
			f.Detail = crashDetail(waitErr, stderr.String())
		}
		return nil, callErr
	}
	return value, nil
}

// exchange is the conversation: send the call, answer every request, stop at
// the first result or error.
func exchange(ctx context.Context, conn *Conn, call Call, lim Limits, b Broker) (json.RawMessage, error) {
	if err := conn.SendCall(call); err != nil {
		return nil, &Failure{Reason: FailCrash, Detail: err.Error()}
	}
	for {
		m, err := conn.Receive()
		if err != nil {
			return nil, &Failure{Reason: FailCrash, Detail: err.Error()}
		}
		switch m.Type {
		case MsgRequest:
			value, err := b.Do(ctx, m.Capability, m.Args)
			reply := Message{Type: MsgResponse, ID: m.ID, Value: value}
			if err != nil {
				reply.Value, reply.Error = nil, err.Error()
			}
			if err := conn.Send(reply); err != nil {
				return nil, &Failure{Reason: FailCrash, Detail: err.Error()}
			}
		case MsgResult:
			// Checked here as well as in the worker: the cap is the parent's,
			// and a worker that has been taken over is exactly the one that
			// would ignore its own.
			if len(m.Value) > lim.MaxResultBytes {
				return nil, &Failure{Reason: FailTooLarge}
			}
			return m.Value, nil
		case MsgError:
			return nil, &ScriptError{Message: m.Error}
		default:
			return nil, &Failure{Reason: FailProtocol, Detail: "unexpected " + m.Type}
		}
	}
}

// watchMemory kills the worker if it grows past what it is allowed.
func watchMemory(pid int, max int64, done <-chan struct{}, kill func()) {
	if max <= 0 {
		return
	}
	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return
	}
	t := time.NewTicker(sampleEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			info, err := p.MemoryInfo()
			if err != nil {
				return // it is gone, which is the outcome this was for
			}
			if int64(info.RSS) > max {
				kill()
				return
			}
		}
	}
}

// crashDetail is what to say about a worker that died: its exit status, and
// whatever it managed to write on the way out.
func crashDetail(waitErr error, stderr string) string {
	detail := waitErr.Error()
	if s := strings.TrimSpace(stderr); s != "" {
		detail += " — " + firstLine(s)
	}
	return detail
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// limitedWriter keeps the first few kilobytes of a stream and drops the rest,
// so a worker that dies printing a stack trace in a loop cannot fill the
// dashboard's memory on its way out.
type limitedWriter struct {
	w    io.Writer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.left <= 0 {
		return len(p), nil
	}
	if len(p) > l.left {
		p = p[:l.left]
	}
	l.left -= len(p)
	n, err := l.w.Write(p)
	if n < len(p) {
		return n, err
	}
	return len(p), err
}
