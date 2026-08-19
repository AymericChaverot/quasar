package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"quasar/internal/db"
	"quasar/internal/station"
)

// The Stations page, and Settings → Stations: browsing what is installed, and
// reading a document to see what it would ask for.
//
// Importing is deliberately two steps rather than one. A station is a program
// somebody else wrote, running with capabilities on a machine the operator
// owns, and the whole permission model rests on those capabilities having been
// read and accepted — so the document is parsed, its permissions are rendered
// in plain words, and only a second submit carrying the hash of what was shown
// stores anything. A one-step import would make the approval screen a thing to
// click past, which is worth less than no approval screen at all.
//
// What that second submit does is in station_install.go.

// handleStationsPage is the Stations entry in the navigation: a card per
// installed station, kept well away from the sixty-odd catalogue entries.
//
// That separation is the whole point of the distinction the format draws. The
// catalogue is where an operator browses software; this is where they browse
// programs somebody wrote for them, and mixing the two would bury the second
// in the first.
func (s *Server) handleStationsPage(w http.ResponseWriter, r *http.Request) {
	data := s.stationLists(r)
	data["Title"] = "Stations"
	data["Domain"] = s.cfg.Domain
	s.render(w, r, "stations", data)
}

// handleStationFavorite stars a station, or takes the star off it. It answers
// with both lists rather than with the one card, because starring one moves it
// into a list that may not have existed a moment ago.
func (s *Server) handleStationFavorite(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	on, err := db.ToggleStationFavorite(s.db, id)
	if err != nil {
		http.Error(w, "that station could not be starred", http.StatusInternalServerError)
		return
	}
	if on {
		s.audit(r, "station.favorite", id, "starred")
	} else {
		s.audit(r, "station.favorite", id, "unstarred")
	}
	s.renderPartial(w, "stations_lists", s.stationLists(r))
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
