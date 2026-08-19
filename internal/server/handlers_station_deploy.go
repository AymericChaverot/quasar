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

	// Advanced is whatever was typed into the general form's fields, which is
	// nothing almost every time. Kept separately from Proposed because the two
	// mean opposite things: this is what somebody insisted on, that is what the
	// document worked out.
	Advanced StationAdvanced

	// Proposed is what the advanced fields would be left to work out, shown as
	// placeholders rather than filled in: they depend on the answers above,
	// and a value that goes stale the moment somebody changes a dropdown is
	// worse than no value.
	Proposed *db.App

	// Recap draws the page as the summary before the last button rather than
	// as the form.
	//
	// It exists because "Install and deploy" is not a small button. It creates
	// an application, takes an address on the operator's domain, writes an
	// environment and starts pulling an image, and the person pressing it is
	// often the one who has least idea what any of that means. A page that
	// says, in a dozen words, what is about to exist and where — with a way
	// back to the answers that decided it — is the difference between a
	// decision and a click.
	Recap bool

	Error string
}

// StationAdvanced is what the general form's fields were filled in with, empty
// almost every time. It is a type rather than five strings threaded through
// three functions because all five travel together everywhere they go: onto
// the recap as hidden fields, back onto the form as values, and into the
// application at the end.
type StationAdvanced struct {
	Name          string
	Subdomain     string
	CustomDomains string
	CPULimit      string
	MemLimitMB    string
}

// Any reports whether anything was entered, which is what decides whether the
// disclosure holding these opens by itself when somebody comes back to change
// something. A fold that hid the value somebody had just typed would be a fold
// that lost it, as far as they could tell.
func (a StationAdvanced) Any() bool {
	return a != StationAdvanced{}
}

// readStationAdvanced reads the general form's answers off a submission.
func readStationAdvanced(r *http.Request) StationAdvanced {
	form := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	return StationAdvanced{
		Name:          form("name"),
		Subdomain:     form("subdomain"),
		CustomDomains: form("custom_domains"),
		CPULimit:      form("cpu_limit"),
		MemLimitMB:    form("mem_limit_mb"),
	}
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

// RecapName and RecapSubdomain are what the application will actually be
// called and where it will actually live: whatever was typed into the advanced
// fields, or what the document worked out when they were left alone.
//
// The recap resolves them rather than showing both, because a summary that
// listed a proposed name and an override beside it would be asking the reader
// to work out which one wins — which is the question the summary exists to
// answer.
func (d StationDeploy) RecapName() string {
	if d.Advanced.Name != "" {
		return d.Advanced.Name
	}
	if d.Proposed != nil {
		return d.Proposed.Name
	}
	return d.Station.Name
}

func (d StationDeploy) RecapSubdomain() string {
	if d.Advanced.Subdomain != "" {
		return strings.ToLower(d.Advanced.Subdomain)
	}
	if d.Proposed != nil {
		return d.Proposed.Subdomain
	}
	return ""
}

// handleStationDeployForm draws the install page.
func (s *Server) handleStationDeployForm(w http.ResponseWriter, r *http.Request) {
	st, ok := s.station(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	s.renderStationDeploy(w, r, st, pickedParams(r.URL.Query()), StationAdvanced{}, false, "")
}

// handleStationDeployReview draws the recap: what is about to be created, where
// it will live, and the answers that decided both — with the way back to them.
//
// Nothing has happened at this point. That is the whole value of the screen:
// the next button creates an application, takes an address on the operator's
// domain and starts pulling an image, and reading a dozen words first is what
// makes pressing it a decision rather than the end of a form.
func (s *Server) handleStationDeployReview(w http.ResponseWriter, r *http.Request) {
	st, ok := s.station(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that form could not be read", http.StatusBadRequest)
		return
	}
	s.renderStationDeploy(w, r, st, pickedParams(r.Form), readStationAdvanced(r), true, "")
}

// handleStationDeployEdit is the way back from the recap, with everything that
// was answered still answered.
func (s *Server) handleStationDeployEdit(w http.ResponseWriter, r *http.Request) {
	st, ok := s.station(r.PathValue("id"))
	if !ok {
		http.Error(w, "no such station", http.StatusNotFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "that form could not be read", http.StatusBadRequest)
		return
	}
	s.renderStationDeploy(w, r, st, pickedParams(r.Form), readStationAdvanced(r), false, "")
}

// renderStationDeploy draws the page for one set of answers, as the form or as
// the recap of it.
func (s *Server) renderStationDeploy(w http.ResponseWriter, r *http.Request,
	st station.Station, picked catalog.Values, advanced StationAdvanced, recap bool, problem string) {

	t := st.Template()
	values := t.Resolve(picked)
	proposed, _ := s.fillFrom(t, values)

	s.render(w, r, "station_deploy", map[string]any{
		"Title":  "Install " + st.Name,
		"Domain": s.cfg.Domain,
		"Deploy": StationDeploy{
			Station: st, Grants: st.Permissions.Summary(),
			Values: values, Advanced: advanced, Proposed: proposed,
			Recap: recap, Error: problem,
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
	advanced := readStationAdvanced(r)
	if advanced.Name != "" {
		app.Name = advanced.Name
	}
	if advanced.Subdomain != "" {
		app.Subdomain = strings.ToLower(advanced.Subdomain)
	}
	if advanced.CustomDomains != "" {
		app.CustomDomains = normalizeDomains(advanced.CustomDomains)
	}
	if v := advanced.CPULimit; v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 {
			app.CPULimit = f
		}
	}
	if v := advanced.MemLimitMB; v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			app.MemLimitMB = n
		}
	}

	// A failure lands back on the recap rather than on the form: the answers
	// were right a moment ago, and what has to be read is what went wrong.
	if problem := s.createApp(w, r, app); problem != "" {
		s.renderStationDeploy(w, r, st, values, advanced, true, problem)
		return
	}
	s.audit(r, "station.deploy", st.ID, app.Name+" ("+app.Subdomain+")")
}
