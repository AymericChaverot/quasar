package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// The worker's half: read one call, run it, answer, exit.
//
// Everything privileged goes back up the pipe through Requester. That is not a
// convenience for the runtime — it is the reason the runtime is safe to have.
// A capability the worker could perform itself would be one the parent never
// got to check against the station's permissions, and an escape from the
// interpreter would then buy an attacker something.

// Requester is how the runtime asks for anything it cannot do, which is
// anything at all.
type Requester interface {
	Request(capability string, args any) (json.RawMessage, error)
}

// Engine runs one call and returns the value to send back.
//
// This is the seam the JavaScript runtime is plugged into, and the reason the
// boundary is built before the interpreter that lives behind it: everything
// the runtime is allowed to do already has to pass through the Requester it is
// handed here, so there is no way to add a capability later that quietly
// skips the parent.
type Engine func(call Call, req Requester) (json.RawMessage, error)

// Serve reads one call from r, runs it, and writes the answer to w. It returns
// when the call is finished, and the process is expected to exit immediately
// afterwards: one call, one process.
func Serve(r io.Reader, w io.Writer, engine Engine) error {
	conn := NewConn(r, w)
	call, err := conn.ReadCall()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil // the parent went away; nothing to report to
		}
		return err
	}

	// Whatever the operating system will let this process ask of itself. It is
	// a second line behind the parent's watchdog and deliberately generous:
	// a limit tight enough to kill the runtime before it has run a line would
	// turn every call into a crash report.
	selfLimit(call)

	value, err := engine(call, &requester{conn: conn})
	switch {
	case err != nil:
		return conn.Send(Message{Type: MsgError, Error: err.Error()})
	case len(value) > call.MaxResultBytes && call.MaxResultBytes > 0:
		return conn.Send(Message{Type: MsgError,
			Error: fmt.Sprintf("this returned %d KB, over the %d KB a call may answer with",
				len(value)/1024, call.MaxResultBytes/1024)})
	}
	return conn.Send(Message{Type: MsgResult, Value: value})
}

// requester turns a capability call into a line on the pipe and waits for the
// answer. Synchronous throughout, which is what the whole design is: a station
// action is a function that runs and returns.
type requester struct {
	conn *Conn
	next int
}

func (q *requester) Request(capability string, args any) (json.RawMessage, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", capability, err)
	}
	q.next++
	id := q.next
	if err := q.conn.Send(Message{Type: MsgRequest, ID: id, Capability: capability, Args: body}); err != nil {
		return nil, err
	}
	m, err := q.conn.Receive()
	if err != nil {
		return nil, err
	}
	if m.Type != MsgResponse || m.ID != id {
		return nil, fmt.Errorf("%s: the answer did not match the question", capability)
	}
	if m.Error != "" {
		return nil, errors.New(m.Error)
	}
	return m.Value, nil
}
