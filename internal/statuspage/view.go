package statuspage

import (
	"fmt"
	"math"
	"time"
)

// View is the page, worded.
type View struct {
	Title, Description, Link string

	// Unavailable is a page that could not read the status at all.
	Unavailable bool

	Banner   *Banner
	Services []ServiceView
	Meters   []Meter

	ShowIncidents bool
	Incidents     []IncidentView

	// CSS is the sheet's address.
	CSS string

	Updated        string
	RefreshSeconds int
}

// Banner is the one line at the top saying how things are.
type Banner struct {
	Tone, Text, Detail string
}

// ServiceView is one service's row.
type ServiceView struct {
	Name       string
	Tone       string // ok, err, or none when the state is not published
	State      string
	Bars       []Bar
	UptimeText string
}

// Bar is one day.
type Bar struct {
	Tone, Title string
}

// Meter is one of the server's figures. Width is the bar's length in a
// hundred-wide drawing, drawn as an SVG attribute: the page allows no inline
// style.
type Meter struct {
	Label, Value, Tone string
	Width              float64
}

// IncidentView is one incident.
type IncidentView struct {
	Service, When, Duration, Note string
	Ongoing                       bool
}

func buildView(doc *Doc, err error, now time.Time) View {
	v := View{Title: "Status", RefreshSeconds: int(refreshEvery / time.Second)}
	if doc == nil {
		v.Unavailable = true
		v.Banner = &Banner{Tone: "none", Text: "Status unavailable", Detail: "The status could not be read just now. This page tries again on its own."}
		return v
	}
	v.Title, v.Description, v.Link = doc.Title, doc.Description, doc.Link
	if v.Title == "" {
		v.Title = "Status"
	}
	v.Updated = doc.UpdatedAt.UTC().Format("15:04 UTC")

	switch doc.Status {
	case "operational":
		v.Banner = &Banner{Tone: "ok", Text: "All systems operational"}
	case "partial_outage":
		v.Banner = &Banner{Tone: "warn", Text: "Some services are down"}
	case "major_outage":
		v.Banner = &Banner{Tone: "err", Text: "Services are down"}
	}

	for _, s := range doc.Services {
		sv := ServiceView{Name: s.Name, Tone: "none"}
		switch s.Status {
		case "operational":
			sv.Tone, sv.State = "ok", "Operational"
		case "down":
			sv.Tone, sv.State = "err", "Down"
		}
		for _, d := range s.Days {
			sv.Bars = append(sv.Bars, dayBar(d))
		}
		if s.Uptime != nil && s.Uptime.Days != nil {
			sv.UptimeText = percent(*s.Uptime.Days) + " over " + fmt.Sprint(len(s.Days)) + " days"
		}
		v.Services = append(v.Services, sv)
	}

	if s := doc.Server; s != nil {
		v.Meters = []Meter{meter("CPU", s.CPU), meter("Memory", s.Memory), meter("Disk", s.Disk)}
	}

	if doc.Incidents != nil {
		v.ShowIncidents = true
		for _, in := range *doc.Incidents {
			v.Incidents = append(v.Incidents, incidentView(in, now))
		}
	}
	return v
}

func dayBar(d Day) Bar {
	date := d.Date
	if t, err := time.Parse(time.DateOnly, d.Date); err == nil {
		date = t.Format("Mon 2 Jan 2006")
	}
	if d.Up == nil {
		return Bar{Tone: "none", Title: date + " · no checks"}
	}
	tone := "err"
	switch {
	case *d.Up >= 99.5:
		tone = "ok"
	case *d.Up >= 95:
		tone = "warn"
	}
	return Bar{Tone: tone, Title: date + " · " + percent(*d.Up)}
}

// percent words a share the way the dashboard does: whole when it is whole,
// and never rounded up into looking better.
func percent(v float64) string {
	switch {
	case v >= 100:
		return "100%"
	case v >= 99:
		return fmt.Sprintf("%.2f%%", math.Floor(v*100)/100)
	}
	return fmt.Sprintf("%.1f%%", math.Floor(v*10)/10)
}

func meter(label string, v float64) Meter {
	tone := "ok"
	switch {
	case v >= 85:
		tone = "err"
	case v >= 60:
		tone = "warn"
	}
	return Meter{Label: label, Value: fmt.Sprintf("%.0f%%", v), Tone: tone, Width: math.Max(0, math.Min(v, 100))}
}

func incidentView(in Incident, now time.Time) IncidentView {
	iv := IncidentView{Service: in.Service, Ongoing: in.EndedAt == nil}
	start := in.StartedAt.UTC()
	iv.When = start.Format("2 Jan · 15:04")
	if in.EndedAt != nil {
		end := in.EndedAt.UTC()
		if end.YearDay() == start.YearDay() && end.Year() == start.Year() {
			iv.When += " → " + end.Format("15:04")
		} else {
			iv.When += " → " + end.Format("2 Jan · 15:04")
		}
	}
	iv.When += " UTC"
	iv.Duration = duration(time.Duration(in.Seconds) * time.Second)
	switch {
	case iv.Ongoing && in.Restarted:
		iv.Note = "Ongoing. Restarted automatically, not answering yet."
	case iv.Ongoing:
		iv.Note = "Ongoing."
	case in.Restarted:
		iv.Note = "Restarted automatically."
	default:
		iv.Note = "Recovered on its own."
	}
	return iv
}

func duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h %02d min", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d d %d h", int(d.Hours())/24, int(d.Hours())%24)
}
