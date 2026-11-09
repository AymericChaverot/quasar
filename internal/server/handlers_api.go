package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/event"
	"quasar/internal/version"
	"quasar/internal/vps"
)

// The JSON API exists so a pipeline can drive Quasar. Deploy webhooks only ever
// covered "redeploy this one app"; this covers listing, inspecting and acting.
//
// Nothing here returns an app's environment variables or compose file. A token
// lives in CI configuration, which is a weaker place than the encrypted column
// those values sit in, so the API deliberately cannot read them out.

// apiTokenKey is the context key carrying who the authenticated token acts
// as, for the handlers and the audit trail.
type apiTokenKey struct{}

func withAPIToken(r *http.Request, id *auth.Identity) context.Context {
	return context.WithValue(r.Context(), apiTokenKey{}, id)
}

// apiIdentity is who the request's token acts as. requireToken always sets
// it; nil only reaches a handler registered outside it.
func apiIdentity(r *http.Request) *auth.Identity {
	id, _ := r.Context().Value(apiTokenKey{}).(*auth.Identity)
	return id
}

// apiActor names a token in the audit log. A personal token's actions are its
// owner's, done through it ("alice via laptop"); a service token's are the
// token's own ("token:github-actions").
func apiActor(id *auth.Identity) string {
	switch {
	case id == nil:
		return "token"
	case id.Personal():
		return id.Username + " via " + id.Token
	default:
		return "token:" + id.Token
	}
}

// auditAPI records an action against the token that performed it, so an API
// deploy is attributable to a specific credential rather than to "webhook" or
// nothing at all.
func (s *Server) auditAPI(r *http.Request, action, target, detail string) {
	s.auditAs(r, apiActor(apiIdentity(r)), action, target, detail)
}

// round1 keeps API numbers readable instead of shipping float noise.
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// vpsCollect is a variable so tests can stand in for host metrics.
var vpsCollect = vps.Collect

// requireToken authenticates a Bearer token and, when adminOnly, requires that
// it acts as an admin: a service token issued with the admin role, or a
// personal token whose owner is an admin now.
func (s *Server) requireToken(adminOnly bool, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if secret == "" {
			writeAPIError(w, http.StatusUnauthorized, "missing Authorization: Bearer <token> header")
			return
		}
		// A wrong token counts like a wrong password, against the same
		// address and under the same lock: guessing one is hopeless, but an
		// address that keeps trying is worth refusing all the same. A right
		// token forgets nothing, though — holding one is no proof that the
		// password failures from the same place were honest.
		ip := clientIP(r)
		if wait, blocked := s.loginGuard.blocked(ip); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeAPIError(w, http.StatusTooManyRequests,
				"too many failed attempts from your address; try again in "+humanWait(wait))
			return
		}
		id, err := auth.AuthenticateToken(s.db, secret)
		if err != nil {
			if locked, p := s.loginGuard.failed(ip); locked {
				detail := fmt.Sprintf("after %d failed attempts", p.Attempts)
				event.Warning("api", ip+" refused for "+humanWait(p.Lock), detail)
				s.auditAs(r, db.ActorSystem, "login.lockout", ip, detail+", for "+humanWait(p.Lock))
			}
		}
		if errors.Is(err, auth.ErrTokenExpired) {
			writeAPIError(w, http.StatusUnauthorized, "token expired; create a new one in Settings")
			return
		}
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		if adminOnly && id.Role != auth.RoleAdmin {
			writeAPIError(w, http.StatusForbidden, "this token is read-only")
			return
		}
		next(w, r.WithContext(withAPIToken(r, id)))
	})
}

func writeAPIError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line has already gone out, so there is no way left to report
	// a half-written body to the caller — only to whoever reads the log.
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: writing the response body: %v", err)
	}
}

// apiApp is the shape an app takes over the API: identity, configuration that
// is not secret, and live state.
type apiApp struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Subdomain  string `json:"subdomain"`
	Host       string `json:"host"`
	DeployType string `json:"deploy_type"`
	ImageRef   string `json:"image_ref,omitempty"`
	GitURL     string `json:"git_url,omitempty"`
	GitBranch  string `json:"git_branch,omitempty"`
	Port       int    `json:"port"`
	State      string `json:"state"`
	Uptime     string `json:"uptime,omitempty"`
	// Deploy is how the latest deploy since the dashboard started went,
	// which is what a client that started one polls for the outcome.
	Deploy apiDeploy `json:"deploy"`
}

// apiDeploy is where an application's latest deploy stands.
type apiDeploy struct {
	Running bool `json:"running"`
	// Error is why the latest deploy failed, or null when it did not (or
	// when there has been none since the dashboard started).
	Error *string `json:"error"`
}

func apiDeployOf(d *docker.DeployState) apiDeploy {
	if d == nil {
		return apiDeploy{}
	}
	out := apiDeploy{Running: d.Running}
	if !d.Running && d.Err != "" {
		msg := docker.RedactURL(d.Err)
		out.Error = &msg
	}
	return out
}

func (s *Server) apiAppFrom(r *http.Request, a *db.App) apiApp { return apiAppOf(s.appView(r, a)) }

// apiAppOf is an application as the API reports it.
func apiAppOf(v AppView) apiApp {
	a := v.App
	out := apiApp{
		ID:         a.ID,
		Name:       a.Name,
		Subdomain:  a.Subdomain,
		Host:       v.Host(),
		DeployType: a.DeployType,
		ImageRef:   a.ImageRef,
		// An operator may have pasted credentials into the clone URL, and a
		// viewer token is no place for them.
		GitURL: docker.RedactURL(a.GitURL),
		Port:   a.Port,
		State:  v.Status.State,
		Uptime: v.Status.Uptime,
		Deploy: apiDeployOf(v.Deploy),
	}
	// Every application stores a branch, "main" by default, but only a git
	// one is deployed from it.
	if a.DeployType == "git" {
		out.GitBranch = a.GitBranch
	}
	return out
}

func (s *Server) handleAPIApps(w http.ResponseWriter, r *http.Request) {
	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		apiInternalError(w, r, err)
		return
	}
	out := make([]apiApp, 0, len(apps))
	for _, v := range s.appViews(r, apps) {
		out = append(out, apiAppOf(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

func (s *Server) handleAPIApp(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	writeJSON(w, http.StatusOK, s.apiAppFrom(r, a))
}

// handleAPIDeploy triggers a deploy and returns immediately: a deploy outlives
// any sensible request timeout, so the caller polls the app for its state.
//
// It fetches first, like the webhook: what calls this is a pipeline that has
// just pushed something and expects to see it live.
func (s *Server) handleAPIDeploy(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	if d := s.dock.Deploying(a.ID); d != nil && d.Running {
		writeAPIError(w, http.StatusConflict, "a deploy is already in progress")
		return
	}
	s.dock.UpdateAsync(a, "api")
	s.auditAPI(r, "app.deploy", a.Name, "triggered by API token")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deploying", "app": a.ID})
}

// handleAPILifecycle starts, stops or restarts an application's containers,
// as the buttons on its page do. Unlike a deploy it is quick, so it answers
// once it is done.
//
// A failure says why, as the dashboard does to the admin who pressed the
// button: only an admin token gets this far, and "internal error" would leave
// it guessing whether the app is down.
func (s *Server) handleAPILifecycle(action string) http.HandlerFunc {
	done := map[string]string{"start": "started", "stop": "stopped", "restart": "restarted"}[action]
	return func(w http.ResponseWriter, r *http.Request) {
		a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "application not found")
			return
		}
		if d := s.dock.Deploying(a.ID); d != nil && d.Running {
			writeAPIError(w, http.StatusConflict, "a deploy is in progress")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		switch action {
		case "start":
			err = s.dock.Start(ctx, a)
		case "stop":
			err = s.dock.Stop(ctx, a)
		default:
			err = s.dock.Restart(ctx, a)
		}
		if err != nil {
			log.Printf("api: %s %s: %v", action, a.Name, err)
			writeAPIError(w, http.StatusInternalServerError,
				a.Name+" did not "+action+": "+docker.RedactURL(err.Error()))
			return
		}
		s.auditAPI(r, "app."+action, a.Name, "triggered by API token")
		writeJSON(w, http.StatusOK, map[string]string{"status": done, "app": a.ID})
	}
}

// apiMe is who a token acts as, and what it is talking to.
type apiMe struct {
	Kind          string     `json:"kind"` // "personal" or "service"
	Token         string     `json:"token"`
	User          *string    `json:"user"`
	Role          string     `json:"role"`
	ExpiresAt     *time.Time `json:"expires_at"`
	ServerVersion string     `json:"server_version"`
	// UploadMaxBytes is the largest folder upload accepted, compressed, so a
	// client can refuse one before sending it.
	UploadMaxBytes int64 `json:"upload_max_bytes"`
}

// handleAPIMe answers who the token is. It is what a client signs in with:
// one call that proves the token works, says what it may do, and tells the
// client which server version it is speaking to, so that it can say plainly
// when the server is too old for a command rather than fail on a missing
// route.
func (s *Server) handleAPIMe(w http.ResponseWriter, r *http.Request) {
	id := apiIdentity(r)
	me := apiMe{Kind: "service", Token: id.Token, Role: id.Role, ServerVersion: version.Version,
		UploadMaxBytes: s.uploadLimit()}
	if id.Personal() {
		me.Kind = "personal"
		me.User = &id.Username
	}
	if id.ExpiresAt.Valid {
		t := id.ExpiresAt.Time.UTC()
		me.ExpiresAt = &t
	}
	writeJSON(w, http.StatusOK, me)
}

// handleAPISystem reports host usage, for an external monitor that would
// otherwise have to scrape the dashboard's HTML.
func (s *Server) handleAPISystem(w http.ResponseWriter, r *http.Request) {
	stats, err := vpsCollect(s.cfg.HostRootPath)
	if err != nil {
		apiInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cpu_percent":   round1(stats.CPUPercent),
		"mem_percent":   round1(stats.MemPercent),
		"mem_used_gb":   round1(stats.MemUsedGB),
		"mem_total_gb":  round1(stats.MemTotalGB),
		"disk_percent":  round1(stats.DiskPercent),
		"disk_used_gb":  round1(stats.DiskUsedGB),
		"disk_total_gb": round1(stats.DiskTotalGB),
	})
}
