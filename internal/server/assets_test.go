package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"quasar/web"
)

// Every script and sheet a template asks for is in the binary. A page that
// names a file the build does not carry loads without it, silently.
func TestEveryReferencedStaticFileIsEmbedded(t *testing.T) {
	ref := regexp.MustCompile(`(?:src|href)="/static/([^"?#{]+)"`)
	err := fs.WalkDir(web.Files, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(web.Files, path)
		if err != nil {
			return err
		}
		for _, m := range ref.FindAllStringSubmatch(string(body), -1) {
			if _, err := fs.Stat(web.Files, "static/"+m[1]); err != nil {
				t.Errorf("%s asks for /static/%s, which is not embedded", path, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The scripts the dashboard runs come from the dashboard: none from a CDN.
// Nor any stylesheet: Tailwind is compiled into app.css ahead of time.
func TestNoScriptsFromElsewhere(t *testing.T) {
	external := regexp.MustCompile(`<(?:script|link)[^>]+(?:src|href)="https?://([^"]+)"`)
	err := fs.WalkDir(web.Files, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, _ := fs.ReadFile(web.Files, path)
		for _, m := range external.FindAllStringSubmatch(string(body), -1) {
			t.Errorf("%s loads %s from outside the dashboard", path, m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Vendored files carry their version in their name and are cached for good.
func TestVendoredFilesAreImmutable(t *testing.T) {
	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	h := http.StripPrefix("/static/", staticAssets(static))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/static/vendor/htmx-2.0.11.min.js", nil))
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("htmx: %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("htmx served as %q", ct)
	}
}
