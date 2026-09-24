package server

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
)

// Pager is what a paged list draws under itself: how many pages there are,
// the one on screen, a link to each page near it, and a field to type any
// other into.
//
// Each link is a full page URL, so it works as a plain link, can be opened in
// a new tab and survives a reload. A list fetched into a page by htmx also
// gives the partial's URL and the element it replaces, and moves through
// pages in place, while the address bar still follows it.
type Pager struct {
	Page, Pages int
	Links       []PageLink
	// Path is where the page field sends its number, and Filters what it
	// carries along with it, so the jump keeps the search that is on screen.
	Path    string
	Filters []PageFilter
	// Partial and Target are set for a list the page fetches on its own:
	// Partial is Path's counterpart for the field, Target what it replaces.
	Partial string
	Target  string
}

// PageLink is one entry on the pager: an arrow, a page number, or the gap
// between two runs of numbers. URL is empty where the link leads nowhere —
// the arrow past either end, and the page already on screen.
type PageLink struct {
	Label   string
	Page    int
	URL     string
	Partial string
	Current bool
	Gap     bool
}

// PageFilter is a filter the page field carries as a hidden input.
type PageFilter struct{ Name, Value string }

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

// pageCount is how many pages of size hold total entries; an empty list is
// still one (empty) page.
func pageCount(total, size int) int {
	return max(1, (total+size-1)/size)
}

// pagerFor builds the pager for page out of pages. query holds the list's
// filters, which every link carries along.
func pagerFor(path string, query url.Values, page, pages int) Pager {
	p := Pager{Page: page, Pages: pages, Path: path}
	for _, k := range sortedKeys(query) {
		if v := query.Get(k); k != "page" && v != "" {
			p.Filters = append(p.Filters, PageFilter{k, v})
		}
	}
	link := func(label string, n int) PageLink {
		l := PageLink{Label: label, Page: n, Current: n == page}
		if n >= 1 && n <= pages && n != page {
			l.URL = pageURL(path, query, n)
		}
		return l
	}
	p.Links = append(p.Links, link("← Newer", page-1))
	last := 0
	for _, n := range pagesAround(page, pages) {
		if n > last+1 {
			p.Links = append(p.Links, PageLink{Label: "…", Gap: true})
		}
		p.Links = append(p.Links, link(strconv.Itoa(n), n))
		last = n
	}
	p.Links = append(p.Links, link("Older →", page+1))
	return p
}

// pagesAround is the page numbers worth a link of their own: the first and
// the last, and two either side of the current one. The rest are reached with
// the arrows or typed in.
func pagesAround(page, pages int) []int {
	var out []int
	for n := 1; n <= pages; n++ {
		if n == 1 || n == pages || (n >= page-2 && n <= page+2) {
			out = append(out, n)
		}
	}
	return out
}

// withPartial has the pager fetch partialPath into target in place.
func (p Pager) withPartial(partialPath string, query url.Values, target string) Pager {
	p.Partial, p.Target = partialPath, target
	links := make([]PageLink, len(p.Links))
	for i, l := range p.Links {
		if l.URL != "" {
			l.Partial = pageURL(partialPath, query, l.Page)
		}
		links[i] = l
	}
	p.Links = links
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

func sortedKeys(v url.Values) []string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
