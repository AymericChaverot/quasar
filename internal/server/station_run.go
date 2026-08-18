package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"quasar/internal/db"
	"quasar/internal/station"
	"quasar/internal/station/worker"
)

// Running one of a station's actions.
//
// Everything a call needs is assembled here and handed across the process
// boundary once: the script, which function to run, what it receives, and a
// description of the application it is about. Nothing else goes over — no
// credentials, no configuration, no handle to anything — because the worker
// would have nowhere to put them and no way to use them.

// The budgets, from the specification's table. A panel source is fetched while
// somebody is looking at a page, so it gets seconds; an action is something
// they pressed and are waiting on; a hook is nobody's page load at all.
const (
	CallSource = "source"
	CallAction = "action"
	CallHook   = "hook"
)

func stationLimits(kind string) worker.Limits {
	lim := worker.DefaultLimits()
	switch kind {
	case CallAction:
		lim.Wall = 60 * time.Second
	case CallHook:
		lim.Wall = 120 * time.Second
	}
	return lim
}

// runStation runs one action of a station for one application, and returns
// what it produced along with everything it logged.
//
// The request is only there for the audit trail, and may be nil: a hook or a
// scheduled action is nobody's click, and recording it against whoever
// happened to be signed in would be worse than recording it against the
// station.
func (s *Server) runStation(ctx context.Context, r *http.Request, app *db.App, doc station.Station,
	kind, action string, input any) (worker.Outcome, error) {

	body, err := json.Marshal(input)
	if err != nil {
		return worker.Outcome{}, err
	}
	sp, err := worker.Self()
	if err != nil {
		return worker.Outcome{}, err
	}

	call := worker.Call{
		Script: doc.Script,
		Action: action,
		Input:  body,
		App:    s.appContext(ctx, app),
	}
	broker := &stationCall{srv: s, app: app, doc: doc, r: r}
	return worker.Run(ctx, sp, call, stationLimits(kind), broker)
}

// appContext is quasar.app: what a script may know about the application it is
// running for, without asking for anything.
//
// None of it is privileged and all of it is read by ordinary actions — the
// name to put in a heading, the parameters the operator picked, whether the
// thing is even running. The environment is deliberately not here: reading a
// value out of it is a permission, per key.
func (s *Server) appContext(ctx context.Context, app *db.App) json.RawMessage {
	fields := map[string]any{
		"id":        app.ID,
		"name":      app.Name,
		"domain":    appHost(app, s.cfg.Domain),
		"image":     app.ImageRef,
		"subdomain": app.Subdomain,
		"port":      app.Port,
		"params":    stationParams(app),
		"status":    "unknown",
	}
	if s.dock != nil {
		fields["status"] = s.dock.Status(ctx, app).State
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}

// stationParams are the answers given when the station was deployed — the
// version, the mod loader, the port. They are kept on the application because
// nothing else remembers them: the deploy renders them into a compose file and
// an env, and neither can be read back as the choices they were.
func stationParams(app *db.App) map[string]string {
	out := map[string]string{}
	if app.StationParams == "" {
		return out
	}
	if err := json.Unmarshal([]byte(app.StationParams), &out); err != nil {
		return map[string]string{}
	}
	return out
}

// stationFor is the station an application was deployed from, if it still has
// one. A station that was removed leaves the application exactly as it is,
// minus its tabs, so this failing is an ordinary state and not an error.
func (s *Server) stationFor(app *db.App) (station.Station, bool) {
	if app.StationID == "" {
		return station.Station{}, false
	}
	return s.station(app.StationID)
}
