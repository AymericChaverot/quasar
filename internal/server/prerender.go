package server

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

// A page's panels, drawn with the page.
//
// Every panel that refreshes itself used to arrive empty — a grey placeholder
// of a guessed height — and be fetched once the page was up. Whatever the
// guess, the real thing was taller or shorter, and the page jumped under the
// reader as each one landed. Now the page asks for its panels itself, all at
// once, before it is sent, and draws them in place: the first paint is the
// finished page, and the polls only ever refresh what is already there.
//
// Each panel is asked for at the same address its poll uses, through the same
// routes and the same session, so what is drawn first and what replaces it
// are the same markup by construction. A panel that is slow to answer — one
// that resolves DNS, say — is not waited for past prerenderBudget: the page
// goes without it, and it arrives the way it always did.

// prerenderBudget is how long a page waits for its panels.
const prerenderBudget = 800 * time.Millisecond

// prerender fetches the panels at the given addresses, by name, as the request
// r would have. A panel missing from the result was too slow or failed, and is
// left to fetch itself.
func (s *Server) prerender(r *http.Request, panels map[string]string) map[string]template.HTML {
	ctx, cancel := context.WithTimeout(r.Context(), prerenderBudget)
	defer cancel()

	type drawn struct {
		name string
		html template.HTML
	}
	results := make(chan drawn, len(panels))
	var wg sync.WaitGroup
	for name, addr := range panels {
		u, err := url.Parse(addr)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := r.Clone(ctx)
			req.Method = http.MethodGet
			req.URL = u
			req.RequestURI = u.RequestURI()
			req.Body = http.NoBody
			req.ContentLength = 0
			req.Header.Set("HX-Request", "true")
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, req)
			if w.Code == http.StatusOK && ctx.Err() == nil {
				// The panel's own handler rendered it through html/template.
				results <- drawn{name, template.HTML(w.Body.String())} //nolint:gosec // trusted, escaped output of our own templates
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	out := make(map[string]template.HTML, len(panels))
	for {
		select {
		case d, ok := <-results:
			if !ok {
				return out
			}
			out[d.name] = d.html
		case <-ctx.Done():
			return out
		}
	}
}
