package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"quasar/internal/auth"
	"quasar/internal/db"
)

// The status API: what the operator chose to publish about the applications
// they chose, and nothing else. Off until they turn it on; then either for
// anybody holding an API token, or for anybody at all. The status page, when
// deployed, reads it with a key of its own, so it works whatever the mode.

// Status modes, in the order the settings offer them.
var statusModes = []string{"off", "token", "public"}

// StatusConfig is the status settings as one value.
type StatusConfig struct {
	Mode string
	CORS bool

	ShowState, ShowUptime, ShowIncidents, ShowServer bool

	Title, Description, Link string

	Page      bool
	Subdomain string
	PageKey   string
}

// defaultStatusSubdomain is where the page goes unless somebody says otherwise.
const defaultStatusSubdomain = "status"

func loadStatusConfig(database *sql.DB) StatusConfig {
	get := func(key, fallback string) string {
		if v := db.GetSetting(database, key); v != "" {
			return v
		}
		return fallback
	}
	c := StatusConfig{
		Mode:          get(db.SettingStatusMode, "off"),
		CORS:          get(db.SettingStatusCORS, "false") == "true",
		ShowState:     get(db.SettingStatusShowState, "true") == "true",
		ShowUptime:    get(db.SettingStatusShowUptime, "true") == "true",
		ShowIncidents: get(db.SettingStatusShowIncidents, "true") == "true",
		ShowServer:    get(db.SettingStatusShowServer, "false") == "true",
		Title:         get(db.SettingStatusTitle, "Status"),
		Description:   db.GetSetting(database, db.SettingStatusDescription),
		Link:          db.GetSetting(database, db.SettingStatusLink),
		Page:          get(db.SettingStatusPage, "false") == "true",
		Subdomain:     get(db.SettingStatusSubdomain, defaultStatusSubdomain),
		PageKey:       db.GetSetting(database, db.SettingStatusPageKey),
	}
	if !slices.Contains(statusModes, c.Mode) {
		c.Mode = "off"
	}
	return c
}

// statusIncidentWindow is how far back the incidents go.
const statusIncidentWindow = 14 * 24 * time.Hour

// statusCacheFor is how long one answer is served again. The API is public,
// and an answer reads ninety days of every published application: a page left
// open in a few hundred browsers should not cost that a few hundred times.
const statusCacheFor = 15 * time.Second

// StatusDoc is the API's answer. A section the operator did not publish is
// absent rather than empty: an empty list says there is nothing, which is
// not what turning it off means.
type StatusDoc struct {
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Link        string            `json:"link,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
	Status      string            `json:"status,omitempty"`
	Services    []StatusService   `json:"services"`
	Incidents   *[]StatusIncident `json:"incidents,omitempty"`
	Server      *StatusServer     `json:"server,omitempty"`
}

// StatusService is one published application.
type StatusService struct {
	Name   string        `json:"name"`
	Status string        `json:"status,omitempty"`
	Uptime *StatusUptime `json:"uptime,omitempty"`
	Days   []StatusDay   `json:"days,omitempty"`
}

// StatusUptime is the share of checks that answered, in percent; null over a
// stretch with no check.
type StatusUptime struct {
	Today *float64 `json:"today"`
	Week  *float64 `json:"7d"`
	Month *float64 `json:"30d"`
	Days  *float64 `json:"90d"`
}

// StatusDay is one day of checks, oldest first; Up is null on a day with none.
type StatusDay struct {
	Date string   `json:"date"`
	Up   *float64 `json:"up"`
}

// StatusIncident is a stretch a published application did not answer.
type StatusIncident struct {
	Service   string     `json:"service"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	Seconds   int64      `json:"duration_seconds"`
	Restarted bool       `json:"restarted"`
}

// StatusServer is the server's use, in percent.
type StatusServer struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	Disk   float64 `json:"disk"`
}

// Service and overall states.
const (
	stateOperational   = "operational"
	stateDown          = "down"
	statePartialOutage = "partial_outage"
	stateMajorOutage   = "major_outage"
)

type statusCache struct {
	mu  sync.Mutex
	at  time.Time
	doc StatusDoc
}

func (c *statusCache) forget() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

func (s *Server) handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	cfg := loadStatusConfig(s.db)
	if !statusAllowed(s.db, cfg, r) {
		if cfg.Mode == "off" {
			writeAPIError(w, http.StatusNotFound, "the status API is turned off")
			return
		}
		writeAPIError(w, http.StatusUnauthorized, "this status needs an API token: Authorization: Bearer <token>")
		return
	}
	if cfg.CORS {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	}
	if cfg.Mode == "public" {
		w.Header().Set("Cache-Control", "public, max-age=15")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}

	s.status.mu.Lock()
	if time.Since(s.status.at) > statusCacheFor {
		s.status.doc = s.buildStatus(r.Context(), cfg, time.Now())
		s.status.at = time.Now()
	}
	doc := s.status.doc
	s.status.mu.Unlock()
	writeJSON(w, http.StatusOK, doc)
}

// handleStatusPreflight answers a browser asking whether another site's page
// may send a token: only when the operator let other sites read the status.
func (s *Server) handleStatusPreflight(w http.ResponseWriter, r *http.Request) {
	if loadStatusConfig(s.db).CORS {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
	}
	w.WriteHeader(http.StatusNoContent)
}

// statusAllowed is whether this request may read the status: the page's own
// key always may, and otherwise the mode decides.
func statusAllowed(database *sql.DB, cfg StatusConfig, r *http.Request) bool {
	secret := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if cfg.PageKey != "" && secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(cfg.PageKey)) == 1 {
		return true
	}
	switch cfg.Mode {
	case "public":
		return true
	case "token":
		_, _, err := auth.AuthenticateToken(database, secret)
		return secret != "" && err == nil
	}
	return false
}

// buildStatus assembles the answer from what is published.
func (s *Server) buildStatus(ctx context.Context, cfg StatusConfig, now time.Time) StatusDoc {
	doc := StatusDoc{Title: cfg.Title, Description: cfg.Description, Link: cfg.Link,
		UpdatedAt: now.UTC().Truncate(time.Second), Services: []StatusService{}}
	published, _ := db.StatusApps(s.db)
	apps, _ := db.ListApps(s.db, s.keyring)

	var open map[string]bool
	incidents, _ := db.Incidents(s.db, now.Add(-statusIncidentWindow))
	for _, in := range incidents {
		if in.Ended == nil {
			if open == nil {
				open = map[string]bool{}
			}
			open[in.AppID] = true
		}
	}

	names := map[string]string{}
	down := 0
	for _, a := range apps {
		name, ok := published[a.ID]
		if !ok {
			continue
		}
		if name == "" {
			name = a.Name
		}
		names[a.ID] = name
		svc := StatusService{Name: name}

		if cfg.ShowState {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			running := statusRunning(s, cctx, a)
			cancel()
			svc.Status = stateOperational
			if !running || open[a.ID] {
				svc.Status = stateDown
				down++
			}
		}
		// An application without a health check has a state, from its
		// container, but no history: nobody asked it anything.
		if cfg.ShowUptime && a.HealthPath != "" {
			if days, err := db.UptimeDays(s.db, a.ID, uptimeDays, now); err == nil {
				svc.Uptime, svc.Days = statusUptime(days)
			}
		}
		doc.Services = append(doc.Services, svc)
	}

	if cfg.ShowState {
		switch {
		case down == 0:
			doc.Status = stateOperational
		case down == len(doc.Services):
			doc.Status = stateMajorOutage
		default:
			doc.Status = statePartialOutage
		}
	}
	if cfg.ShowIncidents {
		list := []StatusIncident{}
		for _, in := range incidents {
			name, ok := names[in.AppID]
			if !ok {
				continue
			}
			end := now
			if in.Ended != nil {
				end = *in.Ended
			}
			list = append(list, StatusIncident{Service: name, StartedAt: in.Started, EndedAt: in.Ended,
				Seconds: int64(end.Sub(in.Started).Seconds()), Restarted: in.Restarted})
		}
		doc.Incidents = &list
	}
	if cfg.ShowServer {
		if st, err := vpsCollect(s.cfg.HostRootPath); err == nil {
			doc.Server = &StatusServer{CPU: round1(st.CPUPercent), Memory: round1(st.MemPercent), Disk: round1(st.DiskPercent)}
		}
	}
	return doc
}

// statusUptime words a run of days for the API: the share over each stretch,
// and every day's.
func statusUptime(days []db.UptimeDay) (*StatusUptime, []StatusDay) {
	share := func(n int) *float64 {
		var ok, total int
		for _, d := range days[len(days)-n:] {
			ok, total = ok+d.OK, total+d.Total
		}
		if total == 0 {
			return nil
		}
		v := cut2(float64(ok) / float64(total) * 100)
		return &v
	}
	up := &StatusUptime{Today: share(1), Week: share(7), Month: share(30), Days: share(len(days))}
	out := make([]StatusDay, len(days))
	for i, d := range days {
		out[i].Date = d.Day.Format(time.DateOnly)
		if p := d.Percent(); p >= 0 {
			v := cut2(p)
			out[i].Up = &v
		}
	}
	return up, out
}

// statusRunning is whether an application's container is up; a variable so
// the tests can answer without a Docker daemon.
var statusRunning = func(s *Server, ctx context.Context, a *db.App) bool {
	return s.dock.Status(ctx, a).State == "running"
}

// cut2 keeps two decimals, cut rather than rounded like the dashboard's own:
// 99.996% is not 100%.
func cut2(v float64) float64 { return math.Floor(v*100) / 100 }
