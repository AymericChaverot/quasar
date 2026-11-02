package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"quasar/internal/chart"
	"quasar/internal/db"
	"quasar/internal/monitor"
)

// The picker may only offer windows the samples behind them survive. The
// retention window lives in another package and moves for reasons of its own —
// disk, mostly — and the day it comes down to two, a chart still offering a
// week would draw one day of line and six days of empty plot, which reads as
// six days of outage.
func TestEveryOfferedWindowFitsInsideRetention(t *testing.T) {
	for _, spec := range metricsRanges {
		window, err := chart.ParseRange(spec)
		if err != nil {
			t.Errorf("the picker offers %q, which is not a window a chart can read: %v", spec, err)
			continue
		}
		if window > monitor.HistoryRetention {
			t.Errorf("the picker offers %s of history; only %s of samples is kept", window, monitor.HistoryRetention)
		}
	}
}

// A query string nobody wrote on purpose redraws the usual chart. This answers
// a partial that polls every sixty seconds: an error here is a hole in the page
// once a minute, where the default is a chart that is merely not the one that
// was asked for.
func TestAnUnknownWindowFallsBackToTheDefault(t *testing.T) {
	for _, query := range []string{"", "?range=", "?range=90d", "?range=nonsense", "?range=24h%00"} {
		spec, window := metricsWindow(httptest.NewRequest("GET", "/partials/metrics"+query, nil))
		if spec != defaultRange || window != 24*time.Hour {
			t.Errorf("%q gave %q (%s); want the default %q", query, spec, window, defaultRange)
		}
	}
	for spec, want := range map[string]time.Duration{"1h": time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour} {
		if got, window := metricsWindow(httptest.NewRequest("GET", "/partials/metrics?range="+spec, nil)); got != spec || window != want {
			t.Errorf("a window the picker offers was not honoured: %q gave %q (%s)", spec, got, window)
		}
	}
}

// Which card gets a whole row falls out of how many were asked for, not out of
// which measurement it is. Unticking one has to close the gap rather than leave
// a card against an empty column, and the card that spans has to be drawn to
// the wider viewBox or it comes out taller than the ones above it.
func TestTheOddCardOutTakesTheWholeRow(t *testing.T) {
	pts := []db.MetricPoint{{TS: time.Now(), V1: 1, V2: 2, V3: 3}}

	for _, c := range []struct {
		show []string
		want []bool // Wide, card by card
	}{
		{nil, nil}, // the picker with every box unticked
		{[]string{"cpu", "disk"}, []bool{false, false}}, // the disk takes memory's place
		{[]string{"disk"}, []bool{true}},                // one card, across the top
		{[]string{"memory"}, []bool{true}},              // and it is not about which one
		{[]string{"nothing_by_that_name"}, nil},         // and asking for nothing draws nothing
	} {
		q := url.Values{"show": c.show}
		q.Set(showMarker, "1")
		r := httptest.NewRequest("GET", "/partials/metrics?"+q.Encode(), nil)
		cards := metricsCards(pts, chosen(r, ServerPicker, ServerMeasurements), "24h")
		if len(cards) != len(c.want) {
			t.Errorf("show=%v drew %d cards, want %d", c.show, len(cards), len(c.want))
			continue
		}
		for i, want := range c.want {
			if cards[i].Wide != want {
				t.Errorf("show=%v: card %d (%s) Wide=%v, want %v", c.show, i, cards[i].Label, cards[i].Wide, want)
			}
			// The viewBox has to follow the decision, or the wide card is the
			// same drawing stretched and reads a size larger than its
			// neighbours.
			if wide := cards[i].Chart.W > 640; wide != want {
				t.Errorf("show=%v: card %d is %v wide but its viewBox is %v", c.show, i, want, cards[i].Chart.W)
			}
		}
	}
}

// Unticking a box closes the gap; it does not reorder what is left.
func TestTheCardsKeepThePageOrder(t *testing.T) {
	r := httptest.NewRequest("GET", "/partials/metrics?picker=1&show=disk&show=cpu", nil)
	got := chosen(r, ServerPicker, ServerMeasurements)
	if len(got) != 2 || got[0].Key != "cpu" || got[1].Key != "disk" {
		t.Errorf("the query string's order won: %v", got)
	}
}

// The picker's selection is remembered the way the theme is, and each picker
// remembers its own: turning the memory chart off on the dashboard is not a
// statement about an application's storage.
func TestEachPickerRemembersItsOwnSelection(t *testing.T) {
	// The cookie is written by remember and read by chosen; neither needs a
	// database, and going through the handler for it would only be asking
	// SQLite to confirm what these two say.
	var s Server

	// A choice made in the picker comes back as a cookie.
	q := url.Values{"show": {"cpu", "disk"}, showMarker: {"1"}}
	rec := httptest.NewRecorder()
	picked := httptest.NewRequest("GET", "/partials/metrics?"+q.Encode(), nil)
	s.remember(rec, picked, ServerPicker, chosen(picked, ServerPicker, ServerMeasurements))

	jar := rec.Result().Cookies()
	if len(jar) != 1 || jar[0].Name != showCookie(ServerPicker) || jar[0].Value != "cpu,disk" {
		t.Fatalf("the choice was not remembered: %v", jar)
	}

	// And a later request with no picker on it reads that cookie back rather
	// than falling back to everything.
	next := httptest.NewRequest("GET", "/partials/metrics", nil)
	next.AddCookie(jar[0])
	if got := chosen(next, ServerPicker, ServerMeasurements); len(got) != 2 || got[0].Key != "cpu" || got[1].Key != "disk" {
		t.Errorf("the remembered choice was not read back: %v", got)
	}
	// It says nothing about the other pickers.
	if got := chosen(next, AppPicker, AppMeasurements); len(got) != len(AppMeasurements) {
		t.Errorf("one picker's cookie changed another's: %v", got)
	}

	// Unticking everything is a choice too, and it survives.
	empty := httptest.NewRequest("GET", "/partials/metrics?"+url.Values{showMarker: {"1"}}.Encode(), nil)
	rec = httptest.NewRecorder()
	s.remember(rec, empty, ServerPicker, chosen(empty, ServerPicker, ServerMeasurements))
	set := rec.Result().Cookies()
	if len(set) != 1 || set[0].Value != "" {
		t.Fatalf("unticking everything was not remembered: %v", set)
	}
	back := httptest.NewRequest("GET", "/partials/metrics", nil)
	back.AddCookie(set[0])
	if got := chosen(back, ServerPicker, ServerMeasurements); len(got) != 0 {
		t.Errorf("an empty selection came back as %v", got)
	}
}

// The picker draws exactly the windows the handler honours, with the default
// ticked: a chip the handler did not know would quietly redraw the default.
func TestThePickerOffersEveryWindowWithTheDefaultTicked(t *testing.T) {
	var b strings.Builder
	if err := testServer(t).pages["dashboard"].ExecuteTemplate(&b, "metrics_range",
		map[string]any{"ID": ServerPicker, "URL": "/partials/metrics"}); err != nil {
		t.Fatal(err)
	}
	html := b.String()
	if got := strings.Count(html, `name="range"`); got != len(metricsRanges) {
		t.Errorf("the picker draws %d windows, the handler offers %d", got, len(metricsRanges))
	}
	for _, spec := range metricsRanges {
		want := `value="` + spec + `"`
		if spec == defaultRange {
			want += " checked"
		}
		if !strings.Contains(html, want) {
			t.Errorf("the picker is missing %s", want)
		}
	}
}
