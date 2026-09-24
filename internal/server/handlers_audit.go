package server

import (
	"log"
	"net"
	"net/http"
	"net/url"
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

// actor is who the request acts as: the signed-in user, or the system for a
// request with no session behind it.
func (s *Server) actor(r *http.Request) string {
	_, username, _, _ := s.currentUser(r)
	if username == "" {
		return db.ActorSystem
	}
	return username
}

// audit records an action against the logged-in user. Called after the action
// succeeds, so the trail says what happened rather than what was attempted —
// failed attempts that matter (a rejected login) are recorded explicitly.
func (s *Server) audit(r *http.Request, action, target, detail string) {
	if err := db.RecordAudit(s.db, db.AuditEntry{
		Actor:  s.actor(r),
		Action: action,
		Target: target,
		Detail: detail,
		IP:     clientIP(r),
	}); err != nil {
		log.Printf("audit: recording %q: %v", action, err)
	}
}

// auditAs records an action for an actor with no session, such as a deploy
// triggered by a webhook.
func (s *Server) auditAs(r *http.Request, actor, action, target, detail string) {
	if err := db.RecordAudit(s.db, db.AuditEntry{
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
		IP:     clientIP(r),
	}); err != nil {
		log.Printf("audit: recording %q: %v", action, err)
	}
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

// auditPageSize is how many entries the Audit page shows at a time.
const auditPageSize = 50

func (s *Server) handleAuditPage(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	total, err := db.CountAudit(s.db, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A page past the end, typed in or not, is the last one.
	pages := pageCount(total, auditPageSize)
	page := min(pageOf(r), pages)
	entries, err := db.ListAuditPage(s.db, query, auditPageSize, (page-1)*auditPageSize)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "audit", map[string]any{
		"Title":   "Audit",
		"Entries": entries,
		"Query":   query,
		"Page":    page,
		"Pager":   pagerFor("/audit", url.Values{"q": {query}}, page, pages),
	})
}
