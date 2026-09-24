package server

import (
	"net/http"
	"net/url"
	"strconv"
)

// Pager is what a paged list draws under itself: the page it is on, and where
// the pages either side of it are. A link is empty where there is no page.
//
// Each link is a full page URL, so it works as a plain link, can be opened in
// a new tab and survives a reload. A list fetched into a page by htmx also
// gives the partial's URL and the element it replaces, and moves through
// pages in place, while the address bar still follows it.
type Pager struct {
	Page         int
	Newer, Older string
	// NewerPartial and OlderPartial are set, with Target, for a list the page
	// fetches on its own.
	NewerPartial, OlderPartial string
	Target                     string
}

// maxPage keeps a hand-typed ?page= from asking the database to skip past
// every row it has, one at a time. No list here holds this many pages.
const maxPage = 10_000

// pageOf reads the page a request asks for: 1 when it names none, or names
// one that is not a page.
func pageOf(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || n < 1 {
		return 1
	}
	return min(n, maxPage)
}

// pagerFor builds the links around page, given whether a page older than it
// exists. query holds the list's filters, which every link carries along.
func pagerFor(path string, query url.Values, page int, hasOlder bool) Pager {
	p := Pager{Page: page}
	if page > 1 {
		p.Newer = pageURL(path, query, page-1)
	}
	if hasOlder {
		p.Older = pageURL(path, query, page+1)
	}
	return p
}

// withPartial has the pager's links fetch partialPath into target in place.
func (p Pager) withPartial(partialPath string, query url.Values, target string) Pager {
	p.Target = target
	if p.Newer != "" {
		p.NewerPartial = pageURL(partialPath, query, p.Page-1)
	}
	if p.Older != "" {
		p.OlderPartial = pageURL(partialPath, query, p.Page+1)
	}
	return p
}

// pageURL is path with query and page. The first page is written without
// one, so it keeps the address it had before there were pages.
func pageURL(path string, query url.Values, page int) string {
	q := url.Values{}
	for k, v := range query {
		if k != "page" && len(v) > 0 && v[0] != "" {
			q.Set(k, v[0])
		}
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
