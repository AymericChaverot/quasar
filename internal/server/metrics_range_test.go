package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"quasar/internal/chart"
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
