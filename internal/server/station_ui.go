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
	"path"
	"path/filepath"
	"quasar/internal/chart"
	"slices"
	"strconv"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/files"
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
		s.renderPartial(w, "station_panel", s.embedPanel(r, a, panel))
		return
	case "chart":
		// A chart reading series reads Quasar's own record of what this
		// station measured, so nothing runs: no worker starts, no script is
		// loaded, and a panel refreshing every thirty seconds costs a query.
		if len(panel.Source.Series) > 0 {
			s.renderPartial(w, "station_panel", s.chartPanel(a, doc, panel))
			return
		}
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
		s.renderPartial(w, "station_panel", s.panelFailed(r, a, panel, stationProblem(panel.Source.Action, err)))
		return
	}
	result := ui.ParseResult(out.Value)
	// A script saying it has nothing yet is not a script that failed. Only it
	// knows that the process is up and the port is not answering, and the
	// panel it wrote should be a spinner rather than a red card while that is
	// true.
	if result.Waiting != "" {
		s.renderPartial(w, "station_panel", ui.Waiting(a.ID, panel, result.Waiting))
		return
	}
	if result.Error != "" {
		s.renderPartial(w, "station_panel", s.panelFailed(r, a, panel, result.Error))
		return
	}
	s.renderPartial(w, "station_panel", ui.Render(a.ID, panel, result.Data))
}

// panelFailed decides which of the two failures this is.
//
// A panel that could not be drawn because the application is not up yet is not
// a broken panel, and drawing it as one is actively misleading: somebody who
// pressed Deploy forty seconds ago and is met with a red card concludes they
// broke it, and the true answer — nothing is wrong, this needs the container —
// is the one thing the card does not say. So the state of the application is
// consulted before the failure is believed, and while it is anything other
// than running the panel says it is waiting and asks again shortly.
func (s *Server) panelFailed(r *http.Request, a *db.App, panel ui.Panel, problem string) ui.PanelView {
	if why := s.stationWaitReason(r, a); why != "" {
		return ui.Waiting(a.ID, panel, why)
	}
	return ui.Failed(a.ID, panel, problem)
}

// stationWaitReason is why a panel has nothing to show yet, or nothing at all
// for an application that is up — where a failure is a real failure and saying
// otherwise would hide the author's bug behind a spinner that never stops.
//
// A stopped application counts as waiting rather than as an error on purpose.
// It is a spinner that will still be spinning in an hour, which sounds wrong
// until you notice what it buys: pressing Start in the status bar directly
// above brings every panel on the page to life on its own, which is exactly
// what somebody who pressed Start expects to happen.
func (s *Server) stationWaitReason(r *http.Request, a *db.App) string {
	if s.dock == nil {
		return ""
	}
	switch s.dock.Status(r.Context(), a).State {
	case "deploying":
		return "Waiting for " + a.Name + " to finish deploying"
	case "stopped", "not deployed":
		return "Waiting for " + a.Name + " to start"
	}
	return ""
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
		// The commonest reason a service has no container is that it does not
		// have one yet, which is what the whole page is waiting for.
		return s.panelFailed(r, a, panel, err.Error())
	}
	return ui.Streaming(a.ID, panel, fmt.Sprintf("/apps/%s/containers/%s/logs", a.ID, name))
}

// chartPanel draws what this station has recorded about this application.
//
// It needs no permission and asks the station nothing: a series belongs to the
// pair of them, Quasar wrote every point in it, and reading it back is reading
// its own table. A series the station has not recorded yet is an empty chart
// rather than a failure — that is what every series looks like for its first
// few minutes, and a red card would be wrong about it.
func (s *Server) chartPanel(a *db.App, doc station.Station, panel ui.Panel) ui.PanelView {
	window, err := chart.ParseRange(panel.Range)
	if err != nil {
		return ui.Failed(a.ID, panel, err.Error())
	}
	since := time.Now().Add(-window)

	series := make([]chart.Series, 0, len(panel.Source.Series))
	for _, name := range panel.Source.Series {
		points, err := db.StationSeries(s.db, a.ID, doc.ID, name, since)
		if err != nil {
			return ui.Failed(a.ID, panel, fmt.Sprintf("reading the series %q: %v", name, err))
		}
		out := make([]chart.Point, 0, len(points))
		for _, p := range points {
			out = append(out, chart.Point{At: p.TS, Value: p.Value})
		}
		series = append(series, chart.Series{Label: name, Points: out})
	}
	v := chart.Build(panel.Kind, series, panel.Unit, panel.Max)
	// What the SVG is announced as. The panel's own title where it has one,
	// since that is what a sighted reader has just read above it.
	v.Label = panel.Title
	if v.Label == "" {
		v.Label = "Chart"
	}
	return ui.Charted(a.ID, panel, v)
}

// embedPanel points an iframe at this application's own service, through
// Quasar. A container's address is routable from this server and from nowhere
// else, so the page cannot load it directly — and going through here is also
// where the permission gets checked.
//
// The service is resolved here rather than left to the frame, so that a page
// which is not up yet is a spinner on Quasar's own card instead of whatever a
// browser draws for a bad gateway inside an iframe.
func (s *Server) embedPanel(r *http.Request, a *db.App, panel ui.Panel) ui.PanelView {
	ref, ok := ui.ParseServiceSrc(panel.Src)
	if !ok {
		// An ordinary address, which the browser can fetch for itself.
		// Validation has already held it to https.
		return ui.Embedded(a.ID, panel, panel.Src)
	}
	if s.dock != nil {
		if _, err := s.dock.ServiceHost(r.Context(), a, ref.Service); err != nil {
			return s.panelFailed(r, a, panel, err.Error())
		}
	}
	return ui.Embedded(a.ID, panel, fmt.Sprintf("/apps/%s/station/embed/%s/", a.ID, panel.ID))
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
		http.Error(w, "this service did not answer: "+station.UnresolvedInternal(host, err).Error(),
			http.StatusBadGateway)
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
		// A progress pane is not a toast: it belongs on the page, where it can
		// be watched and where a reload finds it again. The swap is restated
		// because the button that got here appends — toasts stack — and a pane
		// that appended would leave the last three runs of the same action
		// stacked up the page.
		w.Header().Set("HX-Retarget", "#station-jobs")
		w.Header().Set("HX-Reswap", "innerHTML")
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
		result = s.allowDownload(a, doc, result)
	}
	if result.Error == "" {
		if events := stationTriggers(a, result); events != "" {
			w.Header().Set("HX-Trigger", events)
		}
	}
	s.renderPartial(w, "station_message", result)
}

// allowDownload holds a file an action offered to the permission that would
// have let the script read it, and takes the offer away when it does not cover
// it.
//
// The check is here rather than only on the route because this is where the
// author finds out: a station handing over a path it may not read should say so
// at the moment the action returns, in the same words every other refusal uses,
// rather than producing a link that fails when somebody clicks it.
func (s *Server) allowDownload(a *db.App, doc station.Station, result ui.Result) ui.Result {
	if result.Download == "" {
		return result
	}
	if !doc.Permissions.AllowsPath(result.Download) {
		return ui.Result{Error: denied("handing over "+result.Download, "files").Error()}
	}
	if _, err := s.stationFile(a, result.Download); err != nil {
		return ui.Result{Error: err.Error()}
	}
	return result
}

// handleStationDownload hands over one file out of the application's own
// folder, if the station that asked for it may read it.
//
// The path arrives in the URL rather than being remembered from the action that
// offered it, and the permission is what makes that safe: this is the same
// check quasar.files.read goes through, so a URL somebody typed reaches exactly
// what the station could have read for itself and nothing else. The route is
// admin-only like the storage explorer, for the same reason — what lives in
// these folders is the application's data.
func (s *Server) handleStationDownload(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	doc, ok := s.stationFor(a)
	if !ok {
		http.Error(w, "this application has no station", http.StatusNotFound)
		return
	}
	rel := r.URL.Query().Get("path")
	if !doc.Permissions.AllowsPath(rel) {
		http.Error(w, denied("handing over "+rel, "files").Error(), http.StatusForbidden)
		return
	}

	root, err := s.stationFile(a, rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	f, info, err := root.Open(files.Clean(rel))
	if err != nil {
		http.Error(w, listingError(err), http.StatusNotFound)
		return
	}
	defer f.Close()

	name := path.Base(rel)
	s.audit(r, "station.download", doc.ID+" on "+a.Name, rel)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// stationFile is the application's folder, once it is known that rel names a
// file inside it that can be read. What it reports is what the toast says, so
// it is worded for whoever pressed the button rather than for a log.
func (s *Server) stationFile(a *db.App, rel string) (files.Root, error) {
	root, err := files.NewRoot(filepath.Join(s.cfg.AppsDir, a.ID))
	if err != nil {
		return files.Root{}, errors.New("this application has no folder to read")
	}
	info, err := root.Stat(files.Clean(rel))
	switch {
	case err != nil:
		return files.Root{}, fmt.Errorf("%s: there is no such file to hand over", rel)
	case info.IsDir():
		return files.Root{}, fmt.Errorf("%s is a folder; a download is one file", rel)
	}
	return root, nil
}

// stationDownloadURL is where the browser fetches a file an action offered.
func stationDownloadURL(appID, rel string) string {
	return "/apps/" + appID + "/station/download?path=" + url.QueryEscape(rel)
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

// stationTriggers is the whole HX-Trigger header for one result: the panels to
// re-fetch, the tab to switch to, and the file to fetch if the action offered
// one.
//
// A download carries an address, so the header has to be the JSON form htmx
// also reads. Only then: the plain list is what almost every action produces
// and it is the readable one in a response somebody is looking at in devtools.
func stationTriggers(a *db.App, result ui.Result) string {
	events := refreshEvents(result)
	if result.Download == "" {
		return events
	}

	trigger := map[string]any{
		stationDownloadEvent(): map[string]string{"url": stationDownloadURL(a.ID, result.Download)},
	}
	for _, name := range strings.Split(events, ", ") {
		if name != "" {
			trigger[name] = nil
		}
	}
	out, err := json.Marshal(trigger)
	if err != nil {
		return events
	}
	return string(out)
}

// stationDownloadEvent is what the block's script listens for to start a
// download, in one place so the header and the script cannot disagree.
func stationDownloadEvent() string { return "quasar:station-download" }

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

// stationRefreshAllEvent re-fetches every panel of a station at once, and is
// what the button at the top of the block dispatches.
//
// It is a second event every panel also listens for rather than a loop firing
// each panel's own: the button has no list of panels and should not need one,
// since panels appear and disappear as tabs and grids are drawn. A station
// refreshes itself; the page around it does not move.
func stationRefreshAllEvent() string { return "quasar:station-refresh" }

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
