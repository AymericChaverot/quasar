package server

import (
	"bufio"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"quasar/web"
)

func TestCompressible(t *testing.T) {
	for ct, want := range map[string]bool{
		"text/html; charset=utf-8": true,
		"text/css":                 true,
		"application/json":         true,
		"text/event-stream":        false,
		"font/woff2":               false,
		"application/gzip":         false,
		"image/png":                false,
	} {
		if got := compressible(ct); got != want {
			t.Errorf("compressible(%q) = %v, want %v", ct, got, want)
		}
	}
}

// A page goes out gzipped to a browser that reads gzip, and as it is to one
// that does not.
func TestPagesAreCompressed(t *testing.T) {
	big := strings.Repeat("<p>a line of the page</p>\n", 200)
	h := compressed(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, big)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "gzip" || w.Body.Len() >= len(big) {
		t.Fatalf("not compressed: encoding %q, %d bytes", w.Header().Get("Content-Encoding"), w.Body.Len())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); string(body) != big {
		t.Error("the page did not survive the round trip")
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Header().Get("Content-Encoding") != "" || w.Body.String() != big {
		t.Error("a browser that asked for no encoding got one")
	}
}

// An event stream reaches the browser line by line, as it is written, and
// uncompressed: the first event must arrive while the handler is still open.
func TestEventStreamsAreNotHeldBack(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(compressed(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
	})))
	defer srv.Close()
	defer close(release)

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	// The transport's own gzip handling is off, so the encoding is seen as sent.
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	resp, err := client.Do(req) //nolint:bodyclose // closed below; read in a goroutine the linter does not follow
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Errorf("event stream encoded as %q", enc)
	}
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "data: first\n" {
			t.Errorf("first line %q", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first event was held back")
	}
}

// Static files still revalidate to a 304, and fonts are sent as they are.
func TestStaticFilesUnderCompression(t *testing.T) {
	h := compressed(http.StripPrefix("/static/", staticAssets(mustSub(t))))

	req := httptest.NewRequest("GET", "/static/js/confirm.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	tag := w.Header().Get("ETag")
	if w.Code != 200 || tag == "" || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("script: %d, ETag %q, encoding %q", w.Code, tag, w.Header().Get("Content-Encoding"))
	}
	req = httptest.NewRequest("GET", "/static/js/confirm.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("If-None-Match", tag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Errorf("revalidation answered %d, want 304", w.Code)
	}

	req = httptest.NewRequest("GET", "/static/fonts/space-grotesk-latin-wght-normal.woff2", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "" {
		t.Error("a font was compressed again")
	}
}

func mustSub(t *testing.T) fs.FS {
	t.Helper()
	static, err := fs.Sub(web.Files, "static")
	if err != nil {
		t.Fatal(err)
	}
	return static
}

// The terminal's websocket takes over the connection through the compressor,
// and a browser always offers gzip on the request that opens one.
func TestWebsocketsPassThroughCompression(t *testing.T) {
	srv := httptest.NewServer(compressed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer c.CloseNow()
		_ = c.Write(r.Context(), websocket.MessageText, []byte("hello"))
	})))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Accept-Encoding": {"gzip"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	defer c.CloseNow()
	if _, msg, err := c.Read(ctx); err != nil || string(msg) != "hello" {
		t.Errorf("read %q, %v", msg, err)
	}
}
