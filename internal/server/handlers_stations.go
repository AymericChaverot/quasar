package server

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"quasar/internal/db"
	"quasar/internal/station"
)

// Settings → Stations: installing a station, updating one, and going back when
// an update turns out to have been a mistake.
//
// Importing is deliberately two steps rather than one. A station is a program
// somebody else wrote, running with capabilities on a machine the operator
// owns, and the whole permission model rests on those capabilities having been
// read and accepted — so the document is parsed, its permissions are rendered
// in plain words, and only a second submit carrying the hash of what was shown
// stores anything. A one-step import would make the approval screen a thing to
// click past, which is worth less than no approval screen at all.
//
// Updating has the same rule with the teeth in a different place: a re-fetched
// revision asking for exactly what was already accepted is applied, and one
// asking for more is stored and held. The station keeps running the revision
// the operator approved until they approve the new one. Without that, a
// station imported by URL could be handed net.external by whoever controls the
// address, at a moment nobody was looking.

// StationView is one installed station as the page shows it: the row, plus
// what reading its documents says about them. A station whose document no
// longer parses is still listed — this page is the only place that would ever
// say so.
type StationView struct {
	*db.Station

	// Doc is the approved revision, read back: the row is the text, this is
	// what the text says.
	Doc      station.Station
	Grants   []station.Grant
	Problems []string

	// Pending is a revision that has been fetched and is waiting, with its
	// permissions marked against what was accepted. Only the marked ones
	// matter, and they are the reason the revision is not running.
	Pending        station.Station
	PendingGrants  []station.Grant
	PendingDropped []station.Grant

	// Prev is the revision one click back, for a station whose last update
	// broke a panel.
	Prev station.Station
}

// Held reports a station running one revision with another waiting.
func (v StationView) Held() bool { return v.PendingYAML != "" }

// Revertible reports a station with somewhere to go back to.
func (v StationView) Revertible() bool { return v.PrevYAML != "" }

func (s *Server) stationViews() []StationView {
	rows, err := db.ListStations(s.db)
	if err != nil {
		log.Printf("station: listing: %v", err)
		return nil
	}
	out := make([]StationView, 0, len(rows))
	for _, row := range rows {
		v := StationView{Station: row}

		doc, errs := checkStation(row.YAML)
		v.Doc = doc
		v.Grants = doc.Permissions.Summary()
		for _, err := range errs {
			v.Problems = append(v.Problems, err.Error())
		}

		if row.PendingYAML != "" {
			pending, _ := checkStation(row.PendingYAML)
			v.Pending = pending
			v.PendingGrants = pending.Permissions.AddedSince(doc.Permissions)
			v.PendingDropped = pending.Permissions.DroppedSince(doc.Permissions)
		}
		if row.PrevYAML != "" {
			v.Prev, _ = checkStation(row.PrevYAML)
		}
		out = append(out, v)
	}
	return out
}

// stationReview is a document that has been read and is waiting to be
// accepted. It carries the text forward so that accepting stores exactly what
// was shown, and the hash so that a document swapped in between the two steps
// cannot be installed under an approval given for something else.
type stationReview struct {
	Station   station.Station
	Grants    []station.Grant
	Hash      string
	YAML      string
	SourceURL string
}

func (s *Server) stationsData(r *http.Request) map[string]any {
	return map[string]any{
		"Title":    "Stations",
		"Stations": s.stationViews(),
		"Saved":    r.URL.Query().Get("msg"),
	}
}

func (s *Server) handleStations(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "stations_settings", s.stationsData(r))
}

func redirectStations(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/settings/stations?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// renderStationsError re-renders the page with what went wrong and what was
// typed. A rejected document is not thrown away: the errors are a list of
// things to fix in text somebody may have spent a while writing, and both have
// to survive the round trip.
func (s *Server) renderStationsError(w http.ResponseWriter, r *http.Request, f stationForm, errs []error) {
	msgs := make([]string, len(errs))
	for i, err := range errs {
		msgs[i] = err.Error()
	}
	data := s.stationsData(r)
	data["Errors"] = msgs
	data["Draft"] = f
	s.render(w, r, "stations_settings", data)
}

// stationForm is a submitted document, kept whole so a rejected one can be
// handed back to the form it came from.
type stationForm struct {
	ID        int64
	SourceURL string
	YAML      string

	// Accepted is the permissions hash the review screen showed. It is what
	// makes the second submit an approval of something in particular.
	Accepted string
}

func readStationForm(r *http.Request) stationForm {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return stationForm{
		ID:        id,
		SourceURL: strings.TrimSpace(r.FormValue("source_url")),
		YAML:      strings.ReplaceAll(r.FormValue("yaml"), "\r\n", "\n"),
		Accepted:  strings.TrimSpace(r.FormValue("accepted")),
	}
}

// checkStation parses and validates a submitted document. Storing one that
// does not parse would install a station nothing can run, and finding that out
// is the operator's problem at the moment they wanted it.
func checkStation(doc string) (station.Station, []error) {
	if strings.TrimSpace(doc) == "" {
		return station.Station{}, []error{errors.New("There is nothing in the document.")}
	}
	st, err := station.Parse(doc)
	if err != nil {
		return station.Station{}, []error{err}
	}
	return st, st.Validate()
}

// handleStationReview reads a pasted document and shows what it would be
// allowed to do. Nothing is stored.
func (s *Server) handleStationReview(w http.ResponseWriter, r *http.Request) {
	s.review(w, r, readStationForm(r))
}

// handleStationFetch imports from a URL: fetched once, now, and shown for
// approval like any other document. Nothing here happens on a timer — a
// station is a program this server will run, and one that quietly rewrote
// itself between a look and a deploy would be a bad thing to have built.
func (s *Server) handleStationFetch(w http.ResponseWriter, r *http.Request) {
	f := readStationForm(r)
	doc, err := fetchDocument(f.SourceURL, "station", maxStationBytes)
	if err != nil {
		s.renderStationsError(w, r, f, []error{err})
		return
	}
	f.YAML = doc
	s.review(w, r, f)
}

// review is the screen between reading a document and installing it.
func (s *Server) review(w http.ResponseWriter, r *http.Request, f stationForm) {
	st, errs := checkStation(f.YAML)
	if len(errs) > 0 {
		s.renderStationsError(w, r, f, errs)
		return
	}
	if err := idAvailable(s.db, st.ID); err != nil {
		s.renderStationsError(w, r, f, []error{err})
		return
	}

	data := s.stationsData(r)
	data["Review"] = stationReview{
		Station:   st,
		Grants:    st.Permissions.Summary(),
		Hash:      st.Permissions.Hash(),
		YAML:      f.YAML,
		SourceURL: f.SourceURL,
	}
	s.render(w, r, "stations_settings", data)
}

// idAvailable refuses an id another station already holds.
//
// A catalogue entry reusing an id replaces the entry it names, and that is the
// useful reading there: an entry describes third-party software two people may
// legitimately both describe. A station is a program, and one program replacing
// another because they picked the same name is not a feature. Updating an
// installed station goes through its own re-fetch, which knows which station it
// is updating.
func idAvailable(database *sql.DB, id string) error {
	held := db.GetStationByStationID(database, id)
	if held == nil {
		return nil
	}
	return fmt.Errorf("The id %q is already held by the station %q. Re-fetch that one to update it, "+
		"or remove it first if this is meant to take its place.", id, held.Name)
}

// handleStationInstall stores a station whose permissions have been accepted,
// whether that is a first install or a new revision of one already here.
//
// The document is read again here rather than trusted from the review: what is
// installed has to be what the hash was computed over, or the approval screen
// would be decoration.
func (s *Server) handleStationInstall(w http.ResponseWriter, r *http.Request) {
	f := readStationForm(r)
	st, errs := checkStation(f.YAML)
	if len(errs) > 0 {
		s.renderStationsError(w, r, f, errs)
		return
	}
	hash := st.Permissions.Hash()
	if f.Accepted != hash {
		s.renderStationsError(w, r, f, []error{errors.New(
			"This document is not the one whose permissions were shown. Read it again before installing it.")})
		return
	}

	if err := idAvailable(s.db, st.ID); err != nil {
		s.renderStationsError(w, r, f, []error{err})
		return
	}

	row := &db.Station{
		StationID: st.ID,
		Name:      st.Name,
		SourceURL: f.SourceURL,
		YAML:      f.YAML,
		PermsHash: hash,
		Enabled:   true,
	}
	if _, err := db.InsertStation(s.db, row); err != nil {
		s.renderStationsError(w, r, f, []error{err})
		return
	}
	s.audit(r, "station.import", st.ID, versionAndSource(st.Version, f.SourceURL))
	s.audit(r, "station.permissions.grant", st.ID, grantDetail(st))
	redirectStations(w, r, "Station “"+st.Name+"” installed.")
}

// handleStationRefetch reads the address a station was imported from again.
//
// A revision asking for exactly what was accepted is applied on the spot,
// which is the point of the whole arrangement: fix the mod manager once and
// three servers get the fix. A revision asking for anything more is stored and
// held, and the station keeps running what it was running.
func (s *Server) handleStationRefetch(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetStation(s.db, id)
	if row == nil {
		redirectStations(w, r, "That station is gone.")
		return
	}
	if row.SourceURL == "" {
		redirectStations(w, r, "“"+row.Name+"” was pasted in rather than fetched, so there is nowhere to re-fetch it from.")
		return
	}

	doc, err := fetchDocument(row.SourceURL, "station", maxStationBytes)
	if err != nil {
		s.renderStationsError(w, r, stationForm{ID: id, SourceURL: row.SourceURL}, []error{err})
		return
	}
	st, errs := checkStation(doc)
	if len(errs) > 0 {
		s.renderStationsError(w, r, stationForm{ID: id, SourceURL: row.SourceURL, YAML: doc}, errs)
		return
	}
	if st.ID != row.StationID {
		s.renderStationsError(w, r, stationForm{ID: id, SourceURL: row.SourceURL, YAML: doc}, []error{fmt.Errorf(
			"That address now serves a station with the id %q, not %q. It is a different station, so nothing was changed.",
			st.ID, row.StationID)})
		return
	}

	if doc == row.YAML {
		redirectStations(w, r, "“"+row.Name+"” is already the revision at that address.")
		return
	}

	hash := st.Permissions.Hash()
	if hash != row.PermsHash {
		row.PendingYAML, row.PendingHash = doc, hash
		if err := db.UpdateStation(s.db, row); err != nil {
			s.renderStationsError(w, r, stationForm{ID: id}, []error{err})
			return
		}
		s.audit(r, "station.hold", row.StationID, "version "+st.Version+" asks for more than was accepted")
		redirectStations(w, r, "Revision "+st.Version+" of “"+row.Name+"” asks for more than you accepted. "+
			"It is waiting below; the running revision is unchanged.")
		return
	}

	row.PrevYAML, row.YAML = row.YAML, doc
	row.Name, row.PendingYAML, row.PendingHash = st.Name, "", ""
	if err := db.UpdateStation(s.db, row); err != nil {
		s.renderStationsError(w, r, stationForm{ID: id}, []error{err})
		return
	}
	s.audit(r, "station.update", row.StationID, "version "+st.Version+" from "+row.SourceURL)
	redirectStations(w, r, "“"+row.Name+"” updated to "+st.Version+". It asks for nothing you had not already accepted.")
}

// handleStationAccept promotes the waiting revision, permissions and all.
func (s *Server) handleStationAccept(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetStation(s.db, id)
	if row == nil || row.PendingYAML == "" {
		redirectStations(w, r, "There is no revision waiting.")
		return
	}
	st, errs := checkStation(row.PendingYAML)
	if len(errs) > 0 {
		s.renderStationsError(w, r, stationForm{ID: id, YAML: row.PendingYAML}, errs)
		return
	}

	row.PrevYAML, row.YAML, row.PermsHash = row.YAML, row.PendingYAML, row.PendingHash
	row.Name, row.PendingYAML, row.PendingHash = st.Name, "", ""
	if err := db.UpdateStation(s.db, row); err != nil {
		s.renderStationsError(w, r, stationForm{ID: id}, []error{err})
		return
	}
	s.audit(r, "station.update", row.StationID, "version "+st.Version)
	s.audit(r, "station.permissions.grant", row.StationID, grantDetail(st))
	redirectStations(w, r, "Station “"+row.Name+"” updated to "+st.Version+".")
}

// handleStationDiscard throws the waiting revision away. The next re-fetch
// will find it again if it is still being served.
func (s *Server) handleStationDiscard(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetStation(s.db, id)
	if row == nil || row.PendingYAML == "" {
		redirectStations(w, r, "There is no revision waiting.")
		return
	}
	row.PendingYAML, row.PendingHash = "", ""
	if err := db.UpdateStation(s.db, row); err != nil {
		s.renderStationsError(w, r, stationForm{ID: id}, []error{err})
		return
	}
	s.audit(r, "station.discard", row.StationID, "")
	redirectStations(w, r, "Discarded the revision waiting for “"+row.Name+"”.")
}

// handleStationRevert puts the previous revision back, and keeps the one it
// replaced so the revert is itself revertible. Every revision here has been
// approved before, so going back asks nothing of the operator — which is the
// point: an update that broke a panel is one click, and the application never
// stopped.
func (s *Server) handleStationRevert(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetStation(s.db, id)
	if row == nil || row.PrevYAML == "" {
		redirectStations(w, r, "There is no earlier revision to go back to.")
		return
	}
	st, errs := checkStation(row.PrevYAML)
	if len(errs) > 0 {
		s.renderStationsError(w, r, stationForm{ID: id, YAML: row.PrevYAML}, errs)
		return
	}

	row.YAML, row.PrevYAML = row.PrevYAML, row.YAML
	row.Name, row.PermsHash = st.Name, st.Permissions.Hash()
	if err := db.UpdateStation(s.db, row); err != nil {
		s.renderStationsError(w, r, stationForm{ID: id}, []error{err})
		return
	}
	s.audit(r, "station.revert", row.StationID, "back to version "+st.Version)
	redirectStations(w, r, "Station “"+row.Name+"” is back on "+st.Version+".")
}

// versionAndSource is what the audit entry says an import brought in.
func versionAndSource(version, source string) string {
	if source == "" {
		return "version " + version
	}
	return "version " + version + " from " + source
}

// grantDetail is the accepted permissions on one line, so the audit log
// answers "what was this allowed to do" without anybody having to go and read
// the document as it stands today.
func grantDetail(st station.Station) string {
	grants := st.Permissions.Summary()
	if len(grants) == 0 {
		return "nothing"
	}
	parts := make([]string, len(grants))
	for i, g := range grants {
		parts[i] = g.Title
		if g.Detail != "" {
			parts[i] += " (" + g.Detail + ")"
		}
	}
	return strings.Join(parts, "; ")
}

func (s *Server) handleStationToggle(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetStation(s.db, id)
	if row == nil {
		redirectStations(w, r, "That station is gone.")
		return
	}
	on := !row.Enabled
	if err := db.SetStationEnabled(s.db, id, on); err != nil {
		redirectStations(w, r, "Could not change that: "+err.Error())
		return
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	s.audit(r, "station.toggle", row.StationID, state)
	redirectStations(w, r, "Station “"+row.Name+"” "+state+".")
}

func (s *Server) handleStationDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row := db.GetStation(s.db, id)
	if row == nil {
		redirectStations(w, r, "That station is gone.")
		return
	}
	if err := db.DeleteStation(s.db, id); err != nil {
		redirectStations(w, r, "Could not delete that: "+err.Error())
		return
	}
	s.audit(r, "station.delete", row.StationID, row.Name)
	// A station that is removed leaves a perfectly normal application behind:
	// the same containers, storage, logs and backups, minus the tabs somebody
	// wrote for it.
	redirectStations(w, r, "Station “"+row.Name+"” removed. Applications deployed from it keep running.")
}

// maxStationBytes caps what a fetch will read. A station is a page or two of
// YAML plus whatever it embeds — a typeface, an icon, a banner — and the cap
// is set where a document stops being one somebody would read before trusting.
const maxStationBytes = 2 << 20
