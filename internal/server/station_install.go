package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"quasar/internal/db"
	"quasar/internal/station"
)

// Storing a station once its permissions have been accepted: a first install,
// a new revision of one already here, and going back when a revision turns out
// to have been a mistake.
//
// Updating has the same rule as importing, with the teeth in a different
// place: a re-fetched revision asking for exactly what was already accepted is
// applied, and one asking for more is stored and held. The station keeps
// running the revision the operator approved until they approve the new one.
// Without that, a station imported by URL could be handed net.external by
// whoever controls the address, at a moment nobody was looking.
//
// The screen that approval is given on is in station_pages.go.

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
