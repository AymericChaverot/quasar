// Package statuspage is the public status page: a small server that reads the
// dashboard's status API and shows it.
//
// It runs in a container of its own, from the dashboard's image, holding no
// database, no Docker socket and no secret but the key it reads the API with.
// Whatever it serves, it could only have learned from the API, which already
// publishes nothing the operator did not choose.
package statuspage

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

//go:embed page.html page.css
var files embed.FS

// Doc is the status API's answer, as far as the page reads it.
type Doc struct {
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Link        string      `json:"link"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Status      string      `json:"status"`
	Services    []Service   `json:"services"`
	Incidents   *[]Incident `json:"incidents"`
	Server      *Server     `json:"server"`
}

// Service is one published application.
type Service struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Uptime *Uptime `json:"uptime"`
	Days   []Day   `json:"days"`
}

// Uptime is the share of checks that answered over each stretch.
type Uptime struct {
	Today *float64 `json:"today"`
	Week  *float64 `json:"7d"`
	Month *float64 `json:"30d"`
	Days  *float64 `json:"90d"`
}

// Day is one day of checks.
type Day struct {
	Date string   `json:"date"`
	Up   *float64 `json:"up"`
}

// Incident is a stretch a service did not answer.
type Incident struct {
	Service   string     `json:"service"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Seconds   int64      `json:"duration_seconds"`
	Restarted bool       `json:"restarted"`
}

// Server is the server's use, in percent.
type Server struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Disk   float64 `json:"disk"`
}

// Config is where the page reads the status from, and with what.
type Config struct {
	API string // the status API's address
	Key string // the page's key, sent as a bearer token
}

// refreshEvery is how often the page reloads itself, and how long an answer
// from the API is reused: the API caches for fifteen seconds, so asking it
// more often than that would only get the same answer.
const refreshEvery = 30 * time.Second

// Handler serves the page.
type Handler struct {
	cfg    Config
	client *http.Client
	page   *template.Template
	css    []byte
	// cssURL carries a digest of the sheet, so a release that changes it is
	// fetched again rather than served from an hour of cache.
	cssURL string

	mu  sync.Mutex
	at  time.Time
	doc *Doc
	err error
}

// New prepares a handler.
func New(cfg Config) *Handler {
	css, _ := files.ReadFile("page.css")
	sum := sha256.Sum256(css)
	return &Handler{
		cssURL: "/page.css?v=" + hex.EncodeToString(sum[:6]),
		cfg:    cfg,
		client: &http.Client{Timeout: 5 * time.Second},
		page:   template.Must(template.New("page.html").ParseFS(files, "page.html")),
		css:    css,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	// Nothing on the page comes from anywhere but here, and nothing on it
	// runs: no script, no frame, no form.
	header.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")

	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(http.StatusNoContent)
	case "/page.css":
		header.Set("Content-Type", "text/css; charset=utf-8")
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = w.Write(h.css)
	case "/":
		h.servePage(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) servePage(w http.ResponseWriter, r *http.Request) {
	doc, err := h.status(r.Context())
	view := buildView(doc, err, time.Now())
	view.CSS = h.cssURL
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if doc == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	if err := h.page.Execute(w, view); err != nil {
		log.Printf("status page: rendering: %v", err)
	}
}

// status is the API's answer, reused for a while. A failure is reused too:
// an API that is down should not be asked again by every visitor at once.
func (h *Handler) status(ctx context.Context) (*Doc, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.at) < refreshEvery && (h.doc != nil || h.err != nil) {
		return h.doc, h.err
	}
	doc, err := h.fetch(ctx)
	if err != nil {
		log.Printf("status page: reading the status: %v", err)
	}
	h.doc, h.err, h.at = doc, err, time.Now()
	return doc, err
}

func (h *Handler) fetch(ctx context.Context) (*Doc, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.cfg.API, nil)
	if err != nil {
		return nil, err
	}
	if h.cfg.Key != "" {
		req.Header.Set("Authorization", "Bearer "+h.cfg.Key)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the status API answered %s", resp.Status)
	}
	var doc Doc
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// ListenAndServe runs the page until the context ends.
func ListenAndServe(ctx context.Context, addr string, cfg Config) error {
	if cfg.API == "" {
		return errors.New("no status API to read: set QUASAR_STATUS_API")
	}
	srv := &http.Server{Addr: addr, Handler: New(cfg), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
