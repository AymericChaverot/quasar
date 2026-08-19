package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/notify"
	"quasar/internal/station"
)

// The privileged half of a station call.
//
// A station's script runs in a process holding no socket, no disk, no network
// and no database handle, so everything it wants done arrives here as a
// request. This is where the permission is checked, where the work is done
// with privileges the worker does not have, and where the audit entry is
// written — all three on the same side of the process boundary, which is the
// property the whole design was built for. A check that only existed as "we
// did not inject that binding" would be one goja bug away from nothing.
//
// Every capability follows the same three steps: read the arguments, refuse
// unless the document declared this exact thing, do it. The refusals say what
// was missing, because the operator reading them is usually the author.
//
// The dispatch, the refusal, the notification and the audit entry live here;
// the capabilities themselves are in the station_broker_* files beside it,
// one per thing a station reaches for — the store, the application's files,
// its .env, Docker, and the network.

// stationCall is one call's context: which application, which station, and who
// asked.
type stationCall struct {
	srv *Server
	app *db.App
	doc station.Station
	r   *http.Request

	// net is the way out, built on first use.
	net *station.Fetcher

	// sent counts the notifications this call has already sent.
	sent int

	// job is the progress pane a long action is being watched in, empty for
	// every ordinary call.
	job *stationJob

	// dock is the containers half, named as an interface so the refusals can
	// be tested without a Docker daemon. What is worth testing here is which
	// commands get through and in what shape, not whether Docker runs them.
	dock stationDocker
}

// stationDocker is all of the Docker client a station can reach. Two methods,
// both narrowed to a named service, is the whole of it.
type stationDocker interface {
	ExecInService(ctx context.Context, a *db.App, service string, argv []string, stdin string) (docker.ExecResult, error)
	TailLogs(ctx context.Context, a *db.App, service string, tail int, since string) (string, error)
	ServiceHost(ctx context.Context, a *db.App, service string) (string, error)
}

// containers is the Docker client this call goes through, or a plain refusal
// when the dashboard has none.
func (c *stationCall) containers() (stationDocker, error) {
	if c.dock != nil {
		return c.dock, nil
	}
	if c.srv.dock == nil {
		return nil, errors.New("this dashboard has no connection to Docker")
	}
	return c.srv.dock, nil
}

// Log receives a line the script wrote while it is still running. It is how a
// long action's progress reaches the pane somebody is watching, rather than
// arriving all at once when there is nothing left to wait for.
func (c *stationCall) Log(line string) {
	if c.job != nil {
		c.job.Log(line)
	}
}

// Do performs one capability on the worker's behalf.
func (c *stationCall) Do(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
	switch capability {
	case "store.get", "store.set", "store.delete", "store.keys":
		return c.store(capability, args)
	case "files.list", "files.read", "files.readBytes", "files.write", "files.delete", "files.mkdir":
		return c.files(capability, args)
	case "env.get", "env.set":
		return c.env(capability, args)
	case "exec":
		return c.exec(ctx, args)
	case "logs":
		return c.logs(ctx, args)
	case "http.get", "http.post":
		return c.fetch(ctx, capability, args)
	case "service":
		return c.serviceURL(ctx, args)
	case "lifecycle":
		return c.lifecycle(ctx, args)
	case "notify":
		return c.notify(args)
	}
	return nil, fmt.Errorf("this build of Quasar does not offer %s", capability)
}

// denied is a refusal in the words the author needs: what was reached for, and
// which line of the document would have allowed it.
func denied(what, permission string) error {
	return fmt.Errorf("%s: this station's %s permission does not cover it", what, permission)
}

// --------------------------------------------------------------- notify ----

// maxNotifications is what one call may send. A station that has something to
// say says it once; one in a loop is a bug, and a bug that reaches somebody's
// phone at three in the morning is a bug they remember.
const maxNotifications = 3

func (c *stationCall) notify(raw json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if !c.doc.Permissions.Notify {
		return nil, denied("sending a notification", "notify")
	}
	if strings.TrimSpace(a.Message) == "" {
		return nil, errors.New("quasar.notify needs something to say")
	}
	if c.sent >= maxNotifications {
		return nil, fmt.Errorf("this station has already sent %d notifications in this call", c.sent)
	}
	c.sent++

	// Named, because a message arriving out of nowhere is one nobody can act
	// on: whose station, about which application.
	notify.Send(c.srv.db, fmt.Sprintf("%s (%s): %s", c.doc.Name, c.app.Name, a.Message))
	c.audit("station.notify", a.Message)
	return json.RawMessage("null"), nil
}

// ---------------------------------------------------------------- audit ----

// audit records one privileged thing a station did, so that "what did this
// station do to my server" has an answer that is not "look at the logs and
// guess".
func (c *stationCall) audit(action, detail string) {
	target := c.doc.ID + " on " + c.app.Name
	if c.r != nil {
		c.srv.audit(c.r, action, target, detail)
		return
	}
	// A hook or a scheduled action is nobody's click. Attributing it to
	// whoever happened to be signed in would be worse than saying which
	// station did it.
	if err := db.RecordAudit(c.srv.db, db.AuditEntry{
		Actor:  "station " + c.doc.ID,
		Action: action,
		Target: target,
		Detail: detail,
	}); err != nil {
		log.Printf("audit: recording %q for station %s: %v", action, c.doc.ID, err)
	}
}
