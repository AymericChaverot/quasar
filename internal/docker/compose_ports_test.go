package docker

import (
	"encoding/json"
	"strings"
	"testing"
)

// Compose reports a published port as a string today and as a number in older
// versions, and a range is one entry rather than many — reading any of them
// wrong turns the check into either silence or a false refusal.
func TestPublishedRange(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		lo, hi int
	}{
		{"string, as recent compose writes it", "80", 80, 80},
		{"number, as older compose writes it", float64(80), 80, 80},
		{"range", "8000-8010", 8000, 8010},
		{"range covering the edge port", "79-81", 79, 81},
		{"not published at all", nil, 0, 0},
		{"empty", "", 0, 0},
		{"nonsense", "http", 0, 0},
		{"reversed range falls back to its first port", "90-80", 90, 90},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi := publishedRange(tc.value)
			if lo != tc.lo || hi != tc.hi {
				t.Errorf("publishedRange(%#v) = %d, %d, want %d, %d", tc.value, lo, hi, tc.lo, tc.hi)
			}
		})
	}
}

// The decision the check makes, over the shape compose actually emits.
func TestComposeModelPortConflicts(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantSvc string // the service that must be named, "" when the stack is fine
	}{
		{
			name:    "publishes the HTTP port Traefik owns",
			config:  `{"services":{"nginx":{"ports":[{"target":80,"published":"80"}]}}}`,
			wantSvc: "nginx",
		},
		{
			name:    "publishes HTTPS",
			config:  `{"services":{"proxy":{"ports":[{"target":443,"published":"443"}]}}}`,
			wantSvc: "proxy",
		},
		{
			name:    "a range that swallows port 80",
			config:  `{"services":{"edge":{"ports":[{"published":"78-82"}]}}}`,
			wantSvc: "edge",
		},
		{
			// Container ports are none of Traefik's business — only the host
			// side collides, and a stack routed properly publishes nothing.
			name:   "container ports only",
			config: `{"services":{"web":{"ports":[{"target":80}]},"api":{"ports":[]}}}`,
		},
		{
			name:   "publishes a port nobody else wants",
			config: `{"services":{"db":{"ports":[{"target":5432,"published":"5432"}]}}}`,
		},
		{
			name:   "no ports at all",
			config: `{"services":{"web":{},"worker":{}}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var model composeModel
			if err := json.Unmarshal([]byte(tc.config), &model); err != nil {
				t.Fatal(err)
			}
			got := findPortConflict(model).Service
			if got != tc.wantSvc {
				t.Errorf("conflicting service = %q, want %q", got, tc.wantSvc)
			}
		})
	}
}

// Two offending services must not make the message alternate between deploys,
// which is what Go's map order would do.
func TestComposePortConflictIsDeterministic(t *testing.T) {
	const config = `{"services":{"zeta":{"ports":[{"published":"80"}]},"alpha":{"ports":[{"published":"443"}]}}}`
	for i := 0; i < 20; i++ {
		var model composeModel
		if err := json.Unmarshal([]byte(config), &model); err != nil {
			t.Fatal(err)
		}
		if got := findPortConflict(model).Service; got != "alpha" {
			t.Fatalf("named %q, want alpha every time", got)
		}
	}
}

// The refusal has to be actionable: the operator needs the service name and
// what to do instead, not just "port already allocated". What to do differs by
// why the rewrite left the binding there, so each reason gets its own way out.
func TestComposePortMessageSaysWhatToDo(t *testing.T) {
	c := &Client{network: "traefik-net"}
	var model composeModel
	if err := json.Unmarshal([]byte(`{"services":{"nginx":{"ports":[{"published":"80"}]}}}`), &model); err != nil {
		t.Fatal(err)
	}
	conflict := findPortConflict(model)

	tests := []struct {
		name string
		rep  ComposeAdaptation
		want []string
	}{
		{"nothing is known about the file", ComposeAdaptation{},
			[]string{"nginx", "80", "traefik-net", "certresolver"}},
		{"the author wired it up themselves", ComposeAdaptation{Author: true, Service: "nginx"},
			[]string{"nginx", "its own Traefik labels"}},
		{"no service could be singled out", ComposeAdaptation{Ambiguous: true},
			[]string{"nginx", "Routing panel"}},
		{"routed at a service that is gone", ComposeAdaptation{Gone: true, Choice: "renamed"},
			[]string{"renamed", "Routing panel"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := c.portConflictError(conflict, tc.rep).Error()
			for _, want := range tc.want {
				if !strings.Contains(msg, want) {
					t.Errorf("message does not mention %q:\n%s", want, msg)
				}
			}
		})
	}
}
