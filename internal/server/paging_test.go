package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
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

// pageCount rounds up, and an empty list is still one page.
func TestPageCount(t *testing.T) {
	for _, tc := range []struct{ total, want int }{{0, 1}, {1, 1}, {50, 1}, {51, 2}, {100, 2}, {101, 3}} {
		if got := pageCount(tc.total, 50); got != tc.want {
			t.Errorf("pageCount(%d, 50) = %d, want %d", tc.total, got, tc.want)
		}
	}
}

// labels is how a pager reads, left to right: the page on screen in
// brackets, a link that leads nowhere in parentheses.
func labels(p Pager) string {
	var out []string
	for _, l := range p.Links {
		switch {
		case l.Current:
			out = append(out, "["+l.Label+"]")
		case l.Gap:
			out = append(out, l.Label)
		case l.URL == "":
			out = append(out, "("+l.Label+")")
		default:
			out = append(out, l.Label)
		}
	}
	return strings.Join(out, " ")
}

// The first and last pages always have a link, and so do the two either side
// of the one on screen; the gaps between are marked.
func TestPagerNumbers(t *testing.T) {
	for _, tc := range []struct {
		page, pages int
		want        string
	}{
		{1, 1, "(← Newer) [1] (Older →)"},
		{1, 3, "(← Newer) [1] 2 3 Older →"},
		{1, 20, "(← Newer) [1] 2 3 … 20 Older →"},
		{10, 20, "← Newer 1 … 8 9 [10] 11 12 … 20 Older →"},
		{20, 20, "← Newer 1 … 18 19 [20] (Older →)"},
		{4, 20, "← Newer 1 2 3 [4] 5 6 … 20 Older →"},
	} {
		if got := labels(pagerFor("/audit", url.Values{}, tc.page, tc.pages)); got != tc.want {
			t.Errorf("page %d of %d reads %q, want %q", tc.page, tc.pages, got, tc.want)
		}
	}
}

// Every link carries the filters along, leaves out the empty ones, and writes
// the first page without a number; the page field carries them too.
func TestPagerLinks(t *testing.T) {
	q := url.Values{"q": {"deploy"}, "app": {""}, "page": {"2"}}
	p := pagerFor("/audit", q, 2, 3)
	if newer := p.Links[0]; newer.URL != "/audit?q=deploy" {
		t.Errorf("Newer = %q", newer.URL)
	}
	if older := p.Links[len(p.Links)-1]; older.URL != "/audit?page=3&q=deploy" {
		t.Errorf("Older = %q", older.URL)
	}
	if len(p.Filters) != 1 || p.Filters[0] != (PageFilter{"q", "deploy"}) {
		t.Errorf("the page field carries %+v, want only q=deploy", p.Filters)
	}
	if p.Target != "" || p.Links[0].Partial != "" {
		t.Error("a plain pager has no partial links")
	}

	in := pagerFor("/logs", url.Values{"app": {"a1"}}, 1, 2).withPartial("/partials/logs", url.Values{"app": {"a1"}}, "#log-results")
	older := in.Links[len(in.Links)-1]
	if older.Partial != "/partials/logs?app=a1&page=2" || older.URL != "/logs?app=a1&page=2" {
		t.Errorf("paged in place: %+v", older)
	}
	if in.Links[0].Partial != "" {
		t.Error("the arrow past the first page fetches something")
	}
	if in.Partial != "/partials/logs" || in.Target != "#log-results" {
		t.Errorf("the page field fetches %q into %q", in.Partial, in.Target)
	}
}
