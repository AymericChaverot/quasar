package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"quasar/internal/db"
	"quasar/internal/station/ui"
)

// The series capabilities: what a station has measured about its application,
// kept over time.
//
// Dispatched from Do in station_broker.go. Like the store, and for the same
// reason, they need no permission: a series is scoped to one application and
// one station, and there is nothing in it a station could reach that is not
// its own.

// seriesArgs are the arguments a series call takes some of.
type seriesArgs struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`

	// Hours is how far back a read goes. Nothing older than the retention
	// window is there to be read, so a script asking for a year gets what
	// exists rather than a refusal.
	Hours int64 `json:"hours"`
}

// defaultSeriesHours is what a read covers when it does not say: a day, which
// is the window the dashboard's own graphs have always drawn.
const defaultSeriesHours = 24

func (c *stationCall) series(capability string, raw json.RawMessage) (json.RawMessage, error) {
	var a seriesArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if capability != "series.names" {
		if a.Name == "" {
			return nil, errors.New("quasar.series needs the name of a series")
		}
		// The same shape a chart panel's source is held to at import, checked
		// here because this is the side a script is on. A document and a
		// script that disagreed about what a series is called would leave a
		// chart permanently empty and nothing anywhere saying why.
		if !ui.SeriesName.MatchString(a.Name) {
			return nil, fmt.Errorf("%q is not a series name: lowercase letters, digits and underscores, starting with a letter", a.Name)
		}
	}

	switch capability {
	case "series.record":
		// Not audited. A sample is the station doing what the operator
		// installed it to do, every minute, on its own application — an audit
		// log with fourteen hundred of those a day in it is one nobody reads
		// the rest of.
		if err := db.RecordStationSeries(c.srv.db, c.app.ID, c.doc.ID, a.Name, a.Value); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil

	case "series.read":
		hours := a.Hours
		if hours <= 0 {
			hours = defaultSeriesHours
		}
		points, err := db.StationSeries(c.srv.db, c.app.ID, c.doc.ID, a.Name,
			time.Now().UTC().Add(-time.Duration(hours)*time.Hour))
		if err != nil {
			return nil, err
		}
		// As {at, value} rather than a pair of arrays, because a script reads
		// p.value and an author reading the document should see which is
		// which. The time is ISO, which is what new Date() takes.
		out := make([]map[string]any, 0, len(points))
		for _, p := range points {
			out = append(out, map[string]any{
				"at":    p.TS.UTC().Format("2006-01-02T15:04:05Z"),
				"value": p.Value,
			})
		}
		return json.Marshal(out)
	}

	names, err := db.StationSeriesNames(c.srv.db, c.app.ID, c.doc.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(names)
}
