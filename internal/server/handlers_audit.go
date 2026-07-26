package server

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"quasar/internal/auth"
	"quasar/internal/db"
)

// isAdmin reports whether the request comes from an admin, for partials that
// build their own data map instead of going through render.
func (s *Server) isAdmin(r *http.Request) bool {
	_, _, role, _ := s.currentUser(r)
	return role == auth.RoleAdmin
}

// audit records an action against the logged-in user. Called after the action
// succeeds, so the trail says what happened rather than what was attempted —
// failed attempts that matter (a rejected login) are recorded explicitly.
func (s *Server) audit(r *http.Request, action, target, detail string) {
	_, username, _, _ := s.currentUser(r)
	if username == "" {
		username = db.ActorSystem
	}
	db.RecordAudit(s.db, db.AuditEntry{
		Actor:  username,
		Action: action,
		Target: target,
		Detail: detail,
		IP:     clientIP(r),
	})
}

// auditAs records an action for an actor with no session, such as a deploy
// triggered by a webhook.
func (s *Server) auditAs(r *http.Request, actor, action, target, detail string) {
	db.RecordAudit(s.db, db.AuditEntry{
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
		IP:     clientIP(r),
	})
}

// clientIP reports the address the request came from.
//
// Quasar always sits behind its own Traefik, so RemoteAddr is the proxy and
// X-Forwarded-For is the real client. Only the last entry is trusted: earlier
// ones are whatever the client chose to send, and recording those in an audit
// trail would let anyone forge the origin of their own actions.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 2000 {
		limit = n
	}
	entries, err := db.ListAudit(s.db, query, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "audit", map[string]any{
		"Title":   "Audit",
		"Entries": entries,
		"Query":   query,
	})
}
