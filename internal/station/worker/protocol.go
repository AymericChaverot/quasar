// Package worker is the boundary a station's script runs behind.
//
// The dashboard re-executes its own binary in worker mode, hands it the script
// and one call over a pipe, reads the answer, and the worker dies. Nothing
// survives between two calls, which was already the rule and is now enforced by
// the operating system rather than by convention.
//
// This is not only about memory. An interpreter is not a security boundary: a
// bug in it would otherwise hand a station everything the dashboard can reach —
// the Docker socket, the database, the master key. The worker has none of that.
// It holds no socket, no filesystem, no network and no database handle. Every
// capability is a request sent back up the pipe, which the parent checks
// against the station's declared permissions and executes on the worker's
// behalf, so the permission model is enforced by a process boundary rather than
// by which bindings somebody remembered not to inject.
//
// It also means a station cannot take the dashboard down with it. A panic, a
// stack overflow, an allocation storm: the worker dies, the panel says why, and
// every application on the server keeps running.
//
// This file is the protocol. spawn.go is the parent's half, serve.go the
// worker's.
package worker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// The kinds of message that cross the pipe. Line-delimited JSON in both
// directions, because the worker does not only answer — it asks.
const (
	// MsgCall is the parent's opening message: the script, and which of its
	// functions to run with what.
	MsgCall = "call"

	// MsgRequest is the worker asking for a capability it cannot perform
	// itself, which is all of them.
	MsgRequest = "request"

	// MsgResponse is the parent's answer to one request.
	MsgResponse = "response"

	// MsgLog is a line the script wrote for whoever is reading the panel. It
	// is sent as it happens rather than gathered up and returned at the end,
	// because the call most worth having a log of is the one that never
	// reaches the end.
	MsgLog = "log"

	// MsgResult ends the call with a value, MsgError with a reason.
	MsgResult = "result"
	MsgError  = "error"
)

// Call is what the parent asks for: one action, once.
type Call struct {
	// Script is the station's whole script. It is sent every time rather than
	// cached in the worker, because there is no worker to cache it in: one
	// call, one process.
	Script string `json:"script"`

	// Action is the exported function to run, and Input what it receives.
	Action string          `json:"action"`
	Input  json.RawMessage `json:"input,omitempty"`

	// WallMS is the budget the worker interrupts itself at, so an ordinary
	// runaway loop reports a timeout its author can read rather than being
	// shot from outside. The parent kills a worker that ignores it.
	WallMS int `json:"wall_ms"`

	// MaxResultBytes caps what the worker may send back.
	MaxResultBytes int `json:"max_result_bytes"`

	// MaxMemoryBytes is what the parent will kill the worker above. It is sent
	// so the worker can set its own resource limits from the same number,
	// which is a second line rather than the first: the parent's sampler is
	// what actually holds.
	MaxMemoryBytes int64 `json:"max_memory_bytes"`

	// App is the application the call is about, as quasar.app: its id, name,
	// domain, status, image and the parameters it was deployed with. Sent
	// rather than asked for, because every action reads some of it and none of
	// it is privileged.
	App json.RawMessage `json:"app,omitempty"`
}

// Message is one line on the pipe, in either direction. One shape with a Type
// rather than a type per direction: the whole exchange is four kinds of line,
// and a reader that has to guess which of six structs to try is a reader that
// will eventually guess wrong.
type Message struct {
	Type string `json:"type"`

	// ID pairs a response with the request that asked for it.
	ID int `json:"id,omitempty"`

	// Capability and Args are the request; Value and Error are the answer, and
	// also the end of the call.
	Capability string          `json:"capability,omitempty"`
	Args       json.RawMessage `json:"args,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// maxLineBytes caps one line of protocol. It bounds the parent's reader
// against a worker that decides to send a gigabyte on one line, which is a
// thing a compromised worker would do precisely because the parent is the
// process that must not fall over.
const maxLineBytes = 16 << 20

// Conn is one end of the pipe. Writes are serialised: on the worker's side a
// capability request and the final result are written from the same goroutine
// today, but a runtime that ever writes a log line from another one should not
// be able to interleave two JSON objects on one line.
type Conn struct {
	in  *bufio.Scanner
	out io.Writer
	mu  sync.Mutex
}

// NewConn reads messages from r and writes them to w.
func NewConn(r io.Reader, w io.Writer) *Conn {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxLineBytes)
	return &Conn{in: sc, out: w}
}

// Send writes one message, followed by the newline that ends it.
func (c *Conn) Send(m Message) error {
	line, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.out.Write(append(line, '\n'))
	return err
}

// Receive reads the next message. It returns io.EOF when the other end has
// gone, which for the parent is the ordinary way a worker that crashed
// announces it.
func (c *Conn) Receive() (Message, error) {
	if !c.in.Scan() {
		if err := c.in.Err(); err != nil {
			return Message{}, err
		}
		return Message{}, io.EOF
	}
	var m Message
	if err := json.Unmarshal(c.in.Bytes(), &m); err != nil {
		return Message{}, fmt.Errorf("unreadable message on the pipe: %w", err)
	}
	return m, nil
}

// SendCall is the parent's opening line.
func (c *Conn) SendCall(call Call) error {
	body, err := json.Marshal(call)
	if err != nil {
		return err
	}
	return c.Send(Message{Type: MsgCall, Args: body})
}

// ReadCall is the worker's first read, and the only message it ever expects
// that is not an answer to something it asked.
func (c *Conn) ReadCall() (Call, error) {
	m, err := c.Receive()
	if err != nil {
		return Call{}, err
	}
	if m.Type != MsgCall {
		return Call{}, fmt.Errorf("expected a call, got %q", m.Type)
	}
	var call Call
	if err := json.Unmarshal(m.Args, &call); err != nil {
		return Call{}, err
	}
	return call, nil
}
