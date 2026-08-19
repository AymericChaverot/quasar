package server

import (
	"log"
	"net/http"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/station"
)

// What the Stations pages read.
//
// Everything here turns stored rows into the handful of facts a template puts
// on screen, and all of it is shared: the Stations page, the Settings page and
// the deploy form each reach an installed station through the lookups below.

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

	appCount int
}

// Apps is how many applications were deployed from this station, which is what
// makes removing one a decision rather than a click.
func (v StationView) Apps() int { return v.appCount }

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
		v.appCount = db.CountAppsForStation(s.db, row.StationID)
		out = append(out, v)
	}
	return out
}

// stations are the installed stations an operator can deploy from: enabled,
// and still reading as a station.
//
// A broken one is skipped rather than allowed to fail the page. Nothing here
// was not already validated when it was approved, so reaching the log line
// means a document changed out from under the check — and the settings page is
// where an operator would find that out, not the page they were trying to
// deploy from.
func (s *Server) stations() []station.Station {
	rows, err := db.ListStations(s.db)
	if err != nil {
		log.Printf("station: reading installed stations: %v", err)
		return nil
	}
	var out []station.Station
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		st, errs := checkStation(row.YAML)
		if len(errs) > 0 {
			log.Printf("station: %q will not read, leaving it out: %v", row.StationID, errs[0])
			continue
		}
		out = append(out, st)
	}
	return out
}

// station finds one installed station by its id.
func (s *Server) station(id string) (station.Station, bool) {
	row := db.GetStationByStationID(s.db, id)
	if row == nil || !row.Enabled {
		return station.Station{}, false
	}
	st, errs := checkStation(row.YAML)
	if len(errs) > 0 {
		return station.Station{}, false
	}
	return st, true
}

// StationCard is one station on the Stations page: the document it reads as,
// and whether somebody starred it.
type StationCard struct {
	station.Station
	Favorite bool
}

// stationCards are the installed stations as the page lists them.
//
// A broken one is skipped for the same reason it is skipped everywhere else,
// and the settings page is where an operator is told about it.
func (s *Server) stationCards() []StationCard {
	rows, err := db.ListStations(s.db)
	if err != nil {
		log.Printf("station: reading installed stations: %v", err)
		return nil
	}
	var out []StationCard
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		st, errs := checkStation(row.YAML)
		if len(errs) > 0 {
			log.Printf("station: %q will not read, leaving it out: %v", row.StationID, errs[0])
			continue
		}
		out = append(out, StationCard{Station: st, Favorite: row.Favorite})
	}
	return out
}

// stationLists is what the Stations page draws: everything, and the starred
// ones again at the top.
//
// The starred ones are repeated rather than moved. A list that quietly loses
// entries as they are starred is a list somebody stops trusting to be the whole
// of what is installed, and "All Stations" has to mean all of them.
func (s *Server) stationLists(r *http.Request) map[string]any {
	cards := s.stationCards()
	favorites := make([]StationCard, 0, len(cards))
	for _, c := range cards {
		if c.Favorite {
			favorites = append(favorites, c)
		}
	}
	_, _, role, _ := s.currentUser(r)
	return map[string]any{
		"Stations":  cards,
		"Favorites": favorites,
		"IsAdmin":   role == auth.RoleAdmin,
	}
}

func (s *Server) stationsData(r *http.Request) map[string]any {
	return map[string]any{
		"Title":    "Stations",
		"Stations": s.stationViews(),
		"Saved":    r.URL.Query().Get("msg"),
	}
}
