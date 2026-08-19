package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"quasar/internal/db"
	"quasar/internal/docker"
)

// The capabilities that go through Docker: running a command in a service,
// reading its logs, and starting or stopping the application.
//
// Dispatched from Do in station_broker.go.

// ----------------------------------------------------------------- exec ----

type execArgs struct {
	Service string   `json:"service"`
	Argv    []string `json:"argv"`
	Stdin   string   `json:"stdin"`
}

// exec runs a command in one of the services the permission names.
//
// This is the strongest permission a station can hold — root on the container,
// by design, and the install screen says so in those words. What narrows it is
// the service list: a station granted exec on the game server has not been
// granted it on the database beside it.
func (c *stationCall) exec(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a execArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if !c.doc.Permissions.AllowsExec(a.Service) {
		return nil, denied(fmt.Sprintf("running a command in %q", a.Service), "exec")
	}
	if len(a.Argv) == 0 {
		return nil, errors.New("quasar.exec needs a command to run")
	}
	dock, err := c.containers()
	if err != nil {
		return nil, err
	}

	ctx, cancel := docker.ExecContext(ctx)
	defer cancel()
	result, err := dock.ExecInService(ctx, c.app, a.Service, a.Argv, a.Stdin)
	if err != nil {
		return nil, err
	}

	// Recorded whatever it returned: the audit log's question is what this
	// station did, and a command that failed was still run.
	c.audit("station.exec", a.Service+": "+strings.Join(a.Argv, " "))
	return json.Marshal(result)
}

// ----------------------------------------------------------------- logs ----

type logArgs struct {
	Service string `json:"service"`
	Tail    int    `json:"tail"`
	Since   string `json:"since"`
}

// logs reads a named service's recent output.
//
// Separate from exec because it is far weaker and far more often the whole of
// what a station needs: reading why a server refused to start is not the same
// capability as being able to do anything at all inside it.
func (c *stationCall) logs(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a logArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if !c.doc.Permissions.AllowsLogs(a.Service) {
		return nil, denied(fmt.Sprintf("reading the logs of %q", a.Service), "logs")
	}
	dock, err := c.containers()
	if err != nil {
		return nil, err
	}

	ctx, cancel := docker.ExecContext(ctx)
	defer cancel()
	out, err := dock.TailLogs(ctx, c.app, a.Service, a.Tail, a.Since)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// ------------------------------------------------------------ lifecycle ----

type lifecycleArgs struct {
	Verb  string `json:"verb"`
	Image string `json:"image"`
}

// lifecycle drives the application the station is running on, in the verbs the
// document listed and no others.
//
// It goes through exactly the same machinery as the buttons on the
// application's page — the same Start, the same async deploy, the same
// progress pane — because a station driving a redeploy down a second path
// would be a second way for a deploy to go wrong.
func (c *stationCall) lifecycle(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var a lifecycleArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if !c.doc.Permissions.AllowsLifecycle(a.Verb) {
		return nil, denied(fmt.Sprintf("%s on this application", a.Verb), "lifecycle")
	}
	if c.srv.dock == nil {
		return nil, errors.New("this dashboard has no connection to Docker")
	}

	detail := a.Verb
	switch a.Verb {
	case "start":
		if err := c.srv.dock.Start(ctx, c.app); err != nil {
			return nil, err
		}
	case "stop":
		if err := c.srv.dock.Stop(ctx, c.app); err != nil {
			return nil, err
		}
	case "restart":
		if err := c.srv.dock.Restart(ctx, c.app); err != nil {
			return nil, err
		}
	case "redeploy":
		// Asynchronous, like the button: a deploy outlives the request that
		// started it, and a station action is not a place to wait for one.
		c.srv.dock.DeployAsync(c.app, "station "+c.doc.ID)
	case "set_image":
		if a.Image == "" {
			return nil, errors.New("setImage needs an image reference")
		}
		if err := db.UpdateAppImage(c.srv.db, c.app.ID, a.Image); err != nil {
			return nil, err
		}
		c.app.ImageRef = a.Image
		detail = "set_image to " + a.Image
		c.srv.dock.UpdateAsync(c.app, "station "+c.doc.ID)
	default:
		return nil, fmt.Errorf("%q is not a lifecycle verb", a.Verb)
	}

	c.audit("station.lifecycle", detail)
	return json.RawMessage("null"), nil
}
