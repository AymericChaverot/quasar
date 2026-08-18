package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"quasar/internal/catalog"
	"quasar/internal/db"
	"quasar/internal/station"
)

// Installing a station, which is deliberately not the new-application form.
//
// That form is a good one for the general case: it asks what to run, where to
// route it, what to put in its environment, and it assumes whoever is filling
// it in has a compose file or an image in mind. A station is the opposite
// situation — somebody else already decided all of that, and the only things
// left to answer are the ones the station itself asks. Sending both down the
// same page would make the second look like the first, and the whole point of
// a station is that it is not.
//
// So this page asks the station's own questions and nothing else. Everything
// the general form would have asked is worked out from the document, and is
// still there behind "Advanced options" for the case where somebody wants a
// different address or a second copy under another name.

// StationDeploy is the install page for one station.
type StationDeploy struct {
	Station station.Station
	Grants  []station.Grant

	// Values are the answers so far, so a form that comes back with a problem
	// comes back with what was typed in it.
	Values catalog.Values

	// Proposed is what the advanced fields would be left to work out, shown as
	// placeholders rather than filled in: they depend on the answers above,
	// and a value that goes stale the moment somebody changes a dropdown is
	// worse than no value.
	Proposed *db.App

	Error string
}

// Params are the questions the station asks, which is the whole of what this
// page is for.
func (d StationDeploy) Params() []catalog.Param { return d.Station.Deploy.Params }

// Value is what a parameter is currently set to.
func (d StationDeploy) Value(p catalog.Param) string {
	if v, ok := d.Values[p.Name]; ok && v != "" {
		return v
	}
	return p.Default
}

// handleStationDeployForm draws the install page.
func (s *Server) handleStationDeployForm(w http.ResponseWriter, r *http.Request) {
	st, ok := s.station(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	s.renderStationDeploy(w, r, st, pickedParams(r.URL.Query()), "")
}

// renderStationDeploy draws the page for one set of answers.
func (s *Server) renderStationDeploy(w http.ResponseWriter, r *http.Request,
	st station.Station, picked catalog.Values, problem string) {

	t := st.Template()
	values := t.Resolve(picked)
	proposed, _ := s.fillFrom(t, values)

	s.render(w, r, "station_deploy", map[string]any{
		"Title":  "Install " + st.Name,
		"Domain": s.cfg.Domain,
		"Deploy": StationDeploy{
			Station: st, Grants: st.Permissions.Summary(),
			Values: values, Proposed: proposed, Error: problem,
		},
	})
}

// fillFrom renders a station's deploy block into the application it describes,
// with the answers substituted, and returns the answers as they were kept.
//
// This is the catalogue's own path — the same Resolve, the same Fill, the same
// free-address search — because a station's deploy block is a catalogue entry
// and running it through a second implementation is how the two would come to
// disagree.
func (s *Server) fillFrom(t catalog.Template, values catalog.Values) (*db.App, string) {
	// The address has to be settled before the environment is rendered,
	// because {{URL}} resolves to it.
	sub := s.freeSubdomain(t.SubdomainFor(values))
	f := t.Fill(values, sub, appHost(&db.App{Subdomain: sub}, s.cfg.Domain))

	app := &db.App{
		Name:           f.Name,
		Subdomain:      f.Subdomain,
		DeployType:     f.DeployType,
		ImageRef:       f.ImageRef,
		ComposeYAML:    f.Compose,
		ComposeService: f.ComposeService,
		Port:           f.Port,
		DataMount:      f.DataMount,
		EnvContent:     f.Env,
	}
	kept, err := json.Marshal(values)
	if err != nil {
		return app, "{}"
	}
	return app, string(kept)
}

// handleStationDeploy installs one: the answers become an application, and the
// application remembers which station it came from.
func (s *Server) handleStationDeploy(w http.ResponseWriter, r *http.Request) {
	st, ok := s.station(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that form could not be read", http.StatusBadRequest)
		return
	}

	t := st.Template()
	values := t.Resolve(pickedParams(r.Form))
	app, kept := s.fillFrom(t, values)

	// The one field a station adds, and the answers its script reads back.
	// Both come from here rather than from the form: the id is the station
	// this page is for, and the answers have already been through Resolve, so
	// they are the choices the station offered and not arbitrary text.
	app.StationID = st.ID
	app.StationParams = kept

	// Everything the general form would have asked, for the one case where
	// somebody wants a different address or a second copy under another name.
	// Left empty, each of these keeps what the document worked out.
	form := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	if v := form("name"); v != "" {
		app.Name = v
	}
	if v := form("subdomain"); v != "" {
		app.Subdomain = strings.ToLower(v)
	}
	if v := form("custom_domains"); v != "" {
		app.CustomDomains = normalizeDomains(v)
	}
	if v := form("cpu_limit"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			app.CPULimit = f
		}
	}
	if v := form("mem_limit_mb"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			app.MemLimitMB = n
		}
	}

	if problem := s.createApp(w, r, app); problem != "" {
		s.renderStationDeploy(w, r, st, values, problem)
		return
	}
	s.audit(r, "station.deploy", st.ID, app.Name+" ("+app.Subdomain+")")
}
