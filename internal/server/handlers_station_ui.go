package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"quasar/internal/db"
	"quasar/internal/station"
	"quasar/internal/station/ui"
	"quasar/internal/station/worker"
)

// A station's control surface, served panel by panel.
//
// Nothing here renders what a script wrote, because a script cannot write
// markup: it returns data, a panel says which component that data goes to, and
// the component is Quasar's own. What this file does is address the two halves
// — fetch a panel, run an action — and turn what came back into a partial.
//
// A station renders as one block with its own tab strip at the top of the
// application's page, above Build. Ordinary applications are untouched, there
// is no regression to look for, and the block reads as what it is: a control
// surface somebody wrote for this service, sitting above the machinery Quasar
// provides for every service.

// StationBlock is the station on an application's page.
type StationBlock struct {
	App *db.App
	Doc station.Station

	// Panels is every panel in the document, keyed by its id, so a refresh
	// event can name one.
	Panels map[string]ui.Panel
}

// Tabs is the strip, in the order the document declares it.
func (b StationBlock) Tabs() []ui.Tab { return b.Doc.UI.Tabs }

// Style is the station's own tokens, scoped to this block. Everything outside
// it — the navigation, the top bar, every section below — keeps the theme the
// operator chose, because a station that could repaint the chrome could draw a
// convincing login screen.
func (b StationBlock) Style() template.CSS { return b.Doc.UI.Theme.Tokens("#station") }

// stationBlock is the station block for an application's page, or nothing for
// an application that was not deployed from one — which is every application
// that existed before stations did.
func (s *Server) stationBlock(a *db.App) *StationBlock {
	doc, ok := s.stationFor(a)
	if !ok || len(doc.UI.Tabs) == 0 {
		return nil
	}
	block := &StationBlock{App: a, Doc: doc, Panels: map[string]ui.Panel{}}
	for _, tab := range doc.UI.Tabs {
		collectPanels(tab.Panels, block.Panels)
	}
	return block
}

// collectPanels indexes a tab's panels, nested ones included.
func collectPanels(panels []ui.Panel, into map[string]ui.Panel) {
	for _, p := range panels {
		into[p.ID] = p
		collectPanels(p.Panels, into)
	}
}

// stationPanel finds the application, its station and one of its panels, or
// writes the reason it could not.
func (s *Server) stationPanel(w http.ResponseWriter, r *http.Request) (*db.App, station.Station, ui.Panel, bool) {
	a := s.getApp(w, r)
	if a == nil {
		return nil, station.Station{}, ui.Panel{}, false
	}
	doc, ok := s.stationFor(a)
	if !ok {
		http.Error(w, "this application has no station", http.StatusNotFound)
		return nil, station.Station{}, ui.Panel{}, false
	}
	block := s.stationBlock(a)
	if block == nil {
		http.Error(w, "this station has no panels", http.StatusNotFound)
		return nil, station.Station{}, ui.Panel{}, false
	}
	panel, ok := block.Panels[r.PathValue("panel")]
	if !ok {
		http.Error(w, "this station has no such panel", http.StatusNotFound)
		return nil, station.Station{}, ui.Panel{}, false
	}
	return a, doc, panel, true
}

// handleStationPanelPartial fetches one panel: it calls the action its source
// names, hands what came back to the component, and returns the component.
func (s *Server) handleStationPanelPartial(w http.ResponseWriter, r *http.Request) {
	a, doc, panel, ok := s.stationPanel(w, r)
	if !ok {
		return
	}

	// Two components read something other than the script: a log pane reads a
	// container, an iframe points at one. Both are resolved here, because an
	// address on the application's own network means nothing in a browser and
	// a station never gets to write one.
	switch panel.Type {
	case "log":
		s.renderPartial(w, "station_panel", s.logPanel(r, a, doc, panel))
		return
	case "iframe":
		s.renderPartial(w, "station_panel", embedPanel(a, panel))
		return
	}

	// Static content never runs anything, which is the point of having it:
	// a heading, a note, a table of what the document itself says.
	if panel.Source.Static != nil {
		s.renderPartial(w, "station_panel", ui.Render(a.ID, panel, staticJSON(panel.Source.Static)))
		return
	}
	if panel.Source.Action == "" {
		s.renderPartial(w, "station_panel", ui.Render(a.ID, panel, nil))
		return
	}

	out, err := s.runStation(r.Context(), r, a, doc, CallSource, panel.Source.Action, map[string]any{})
	if err != nil {
		s.renderPartial(w, "station_panel", ui.Failed(a.ID, panel, stationProblem(panel.Source.Action, err)))
		return
	}
	result := ui.ParseResult(out.Value)
	if result.Error != "" {
		s.renderPartial(w, "station_panel", ui.Failed(a.ID, panel, result.Error))
		return
	}
	s.renderPartial(w, "station_panel", ui.Render(a.ID, panel, result.Data))
}

// logPanel points Quasar's own log pane at the container behind a named
// service. The pane, the stream and the follow-the-tail behaviour are the ones
// every other page uses; what a station chose is which service to look at.
func (s *Server) logPanel(r *http.Request, a *db.App, doc station.Station, panel ui.Panel) ui.PanelView {
	if !doc.Permissions.AllowsLogs(panel.Service) {
		return ui.Failed(a.ID, panel, denied("reading the logs of "+panel.Service, "logs").Error())
	}
	if s.dock == nil {
		return ui.Failed(a.ID, panel, "this dashboard has no connection to Docker")
	}
	name, err := s.dock.ServiceHost(r.Context(), a, panel.Service)
	if err != nil {
		return ui.Failed(a.ID, panel, err.Error())
	}
	return ui.Streaming(a.ID, panel, fmt.Sprintf("/apps/%s/containers/%s/logs", a.ID, name))
}

// embedPanel points an iframe at this application's own service, through
// Quasar. A container's address is routable from this server and from nowhere
// else, so the page cannot load it directly — and going through here is also
// where the permission gets checked.
func embedPanel(a *db.App, panel ui.Panel) ui.PanelView {
	if _, ok := ui.ParseServiceSrc(panel.Src); ok {
		return ui.Embedded(a.ID, panel, fmt.Sprintf("/apps/%s/station/embed/%s/", a.ID, panel.ID))
	}
	// An ordinary address, which the browser can fetch for itself. Validation
	// has already held it to https.
	return ui.Embedded(a.ID, panel, panel.Src)
}

// handleStationEmbed proxies one of the application's own services onto the
// page.
//
// Everything about where it goes comes from the document: the service and the
// port are read out of the panel, checked against net.internal, and resolved
// to a container. Nothing in the URL decides the target — only which panel is
// being drawn, and how far into it the browser has navigated.
func (s *Server) handleStationEmbed(w http.ResponseWriter, r *http.Request) {
	a, doc, panel, ok := s.stationPanel(w, r)
	if !ok {
		return
	}
	ref, ok := ui.ParseServiceSrc(panel.Src)
	if !ok || panel.Type != "iframe" {
		http.Error(w, "this panel embeds nothing", http.StatusNotFound)
		return
	}
	if !doc.Permissions.AllowsInternal(ref.Service, ref.Port) {
		http.Error(w, denied("embedding "+ref.Service, "net.internal").Error(), http.StatusForbidden)
		return
	}
	if s.dock == nil {
		http.Error(w, "this dashboard has no connection to Docker", http.StatusServiceUnavailable)
		return
	}
	host, err := s.dock.ServiceHost(r.Context(), a, ref.Service)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, strconv.Itoa(ref.Port))}
	prefix := fmt.Sprintf("/apps/%s/station/embed/%s", a.ID, panel.ID)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, "this service did not answer: "+err.Error(), http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		// The embedded page is somebody else's, served from this origin. It
		// gets no frame of its own and no scripts from anywhere but where it
		// came from.
		resp.Header.Del("X-Frame-Options")
		resp.Header.Del("Content-Security-Policy")
		resp.Header.Set("Content-Security-Policy", "frame-ancestors 'self'; sandbox allow-scripts allow-same-origin allow-forms")
		return nil
	}
	http.StripPrefix(prefix, proxy).ServeHTTP(w, r)
}

// handleStationAction runs an action and applies what it returned.
//
// The response body is the message, and the panels to re-fetch travel as
// events in a header: a panel already knows how to fetch itself, and having
// the action render every affected panel too would be two paths to the same
// markup.
func (s *Server) handleStationAction(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	doc, ok := s.stationFor(a)
	if !ok {
		http.Error(w, "this application has no station", http.StatusNotFound)
		return
	}
	name := r.PathValue("action")
	if !stationOffers(doc, name) {
		http.Error(w, "this station has no such action", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		s.renderPartial(w, "station_message", ui.Result{Error: "that form could not be read"})
		return
	}
	input := map[string]any{}
	for key, values := range r.PostForm {
		if len(values) > 0 {
			input[key] = values[0]
		}
	}

	// An action the document declared long runs detached from this request,
	// and the answer is a pane to watch it in rather than a result: a browser
	// that gives up waiting has not cancelled a mod download.
	if slices.Contains(doc.UI.LongActions(), name) {
		job, fresh := s.startLongAction(a, doc, name, input)
		if !fresh {
			s.audit(r, "station.action", doc.ID+" on "+a.Name, name+" was already running")
		} else {
			s.audit(r, "station.action", doc.ID+" on "+a.Name, name+" (background)")
		}
		s.renderPartial(w, "station_job", job.view(a.ID, name))
		return
	}

	out, err := s.runStation(r.Context(), r, a, doc, CallAction, name, input)
	if err != nil {
		s.renderPartial(w, "station_message", ui.Result{Error: stationProblem(name, err)})
		return
	}
	result := ui.ParseResult(out.Value)
	if result.Error == "" {
		if events := refreshEvents(result); events != "" {
			w.Header().Set("HX-Trigger", events)
		}
	}
	s.renderPartial(w, "station_message", result)
}

// handleStationJob draws a long action's pane: what it has written so far, and
// whether it is still writing.
//
// It survives a page reload because the job does not live in the request that
// started it — reopening the application's page and pressing the same button
// finds the job already running and shows it, rather than starting a second.
func (s *Server) handleStationJob(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	action := r.PathValue("action")
	job := s.jobs.get(a.ID, action)
	if job == nil {
		http.Error(w, "nothing is running", http.StatusNotFound)
		return
	}
	s.renderPartial(w, "station_job", job.view(a.ID, action))
}

// stationOffers reports whether the document reaches this action from anywhere.
// A name arrives in a URL, and a URL is typed by anybody: without this, an
// admin could run any function the script happens to export, including the
// helpers its author never meant to be an action.
func stationOffers(doc station.Station, name string) bool {
	for _, a := range doc.UI.Actions() {
		if a == name {
			return true
		}
	}
	return false
}

// refreshEvents is the header a panel listens for. One event per panel named,
// so a table refreshes and the form beside it does not flicker.
func refreshEvents(result ui.Result) string {
	var events []string
	for _, id := range result.Refresh {
		events = append(events, stationRefreshEvent(id))
	}
	if result.Navigate != "" {
		events = append(events, "quasar:station-tab-"+result.Navigate)
	}
	return strings.Join(events, ", ")
}

// stationRefreshEvent is the event name a panel is re-fetched by, in one place
// so the header and the template cannot disagree about it.
func stationRefreshEvent(panelID string) string { return "quasar:station-refresh-" + panelID }

// stationProblem turns a failed call into what the panel says.
//
// The three cases read differently on purpose. A script that threw is the
// author's bug and keeps its own words. A budget that ran out is a fact about
// this call. Anything else is Quasar's own failure and should not be dressed
// up as the station's.
func stationProblem(action string, err error) string {
	var script *worker.ScriptError
	if errors.As(err, &script) {
		return script.Message
	}
	var failure *worker.Failure
	if errors.As(err, &failure) {
		return fmt.Sprintf("%s: %s", action, failure.Error())
	}
	return fmt.Sprintf("%s could not be run: %v", action, err)
}

// staticJSON is content written into the document, on its way to a component
// that reads JSON like any other. YAML gave it to us as maps and slices, so
// there is nothing here that can fail in a way worth reporting.
func staticJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return out
}
