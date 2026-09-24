package server

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPageOf(t *testing.T) {
	for raw, want := range map[string]int{
		"":               1,
		"?page=3":        3,
		"?page=0":        1,
		"?page=-2":       1,
		"?page=abc":      1,
		"?page=9e99":     1,
		"?page=99999999": maxPage,
	} {
		if got := pageOf(httptest.NewRequest("GET", "/audit"+raw, nil)); got != want {
			t.Errorf("pageOf(%q) = %d, want %d", raw, got, want)
		}
	}
}

// The links carry the filters along, leave out the ones that are empty, and
// write the first page without a number.
func TestPagerLinks(t *testing.T) {
	q := url.Values{"q": {"deploy"}, "app": {""}, "page": {"2"}}
	p := pagerFor("/audit", q, 2, true)
	if p.Newer != "/audit?q=deploy" {
		t.Errorf("Newer = %q", p.Newer)
	}
	if p.Older != "/audit?page=3&q=deploy" {
		t.Errorf("Older = %q", p.Older)
	}
	if p.Target != "" || p.OlderPartial != "" {
		t.Error("a plain pager has no partial links")
	}

	last := pagerFor("/logs", url.Values{}, 1, false).withPartial("/partials/logs", url.Values{}, "#log-results")
	if last.Newer != "" || last.Older != "" || last.NewerPartial != "" || last.OlderPartial != "" {
		t.Errorf("a single page links nowhere: %+v", last)
	}

	mid := pagerFor("/logs", url.Values{"app": {"a1"}}, 1, true).withPartial("/partials/logs", url.Values{"app": {"a1"}}, "#log-results")
	if mid.OlderPartial != "/partials/logs?app=a1&page=2" || mid.Older != "/logs?app=a1&page=2" {
		t.Errorf("paged in place: %+v", mid)
	}
}
