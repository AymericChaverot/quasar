package server

import (
	"net/http/httptest"
	"net/url"
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
		if window > monitor.Retention {
			t.Errorf("the picker offers %s of history; only %s of samples is kept", window, monitor.Retention)
		}
	}
}

// A query string nobody wrote on purpose redraws the usual chart. This answers
// a partial that polls every sixty seconds: an error here is a hole in the page
// once a minute, where the default is a chart that is merely not the one that
// was asked for.
func TestAnUnknownWindowFallsBackToTheDefault(t *testing.T) {
	for _, query := range []string{"", "?range=", "?range=30d", "?range=nonsense", "?range=24h%00"} {
		spec, window := metricsWindow(httptest.NewRequest("GET", "/partials/metrics"+query, nil))
		if spec != metricsRanges[0] || window != 24*time.Hour {
			t.Errorf("%q gave %q (%s); want the default %q", query, spec, window, metricsRanges[0])
		}
	}
	if spec, window := metricsWindow(httptest.NewRequest("GET", "/partials/metrics?range=7d", nil)); spec != "7d" || window != 7*24*time.Hour {
		t.Errorf("a window the picker offers was not honoured: %q (%s)", spec, window)
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
		{nil, []bool{false, false, true}},                              // all three: the disk spans
		{[]string{"cpu", "disk"}, []bool{false, false}},                // the disk takes memory's place
		{[]string{"disk"}, []bool{true}},                               // one card, across the top
		{[]string{"memory"}, []bool{true}},                             // and it is not about which one
		{[]string{"nothing_by_that_name"}, []bool{false, false, true}}, // asking for none is asking for all
	} {
		r := httptest.NewRequest("GET", "/partials/metrics?"+url.Values{"show": c.show}.Encode(), nil)
		cards := metricsCards(pts, chosen(r, ServerMeasurements), "24h")
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
	r := httptest.NewRequest("GET", "/partials/metrics?show=disk&show=cpu", nil)
	got := chosen(r, ServerMeasurements)
	if len(got) != 2 || got[0].Key != "cpu" || got[1].Key != "disk" {
		t.Errorf("the query string's order won: %v", got)
	}
}
