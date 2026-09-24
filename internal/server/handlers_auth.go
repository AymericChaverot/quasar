package server

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/event"
)

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login", map[string]any{"Title": "Sign in", "HideNav": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	loginPage := map[string]any{"Title": "Sign in", "HideNav": true}
	// Checked before the password is: a refused address learns nothing about
	// whether its next guess would have been right.
	if s.refuse(w, r, "login", loginPage) {
		return
	}
	username := r.FormValue("username")
	token, needs2FA, err := auth.Login(s.db, username, r.FormValue("password"))
	if err != nil {
		// Rejected attempts are the whole point of auditing logins: a run of
		// them against one account is the only warning of a password attack.
		// The submitted name is recorded as the target, never as the actor.
		s.auditAs(r, db.ActorSystem, "login.failed", username, "")
		s.loginFailures.record(clientIP(r), "sign-in", username)
		if s.countFailure(w, r, "login", loginPage) {
			return
		}
		loginPage["Error"] = "Invalid username or password."
		s.render(w, r, "login", loginPage)
		return
	}
	auth.SetCookie(w, token, s.cfg.CookieSecure)
	if needs2FA {
		http.Redirect(w, r, "/2fa", http.StatusSeeOther)
		return
	}
	s.loginGuard.succeeded(clientIP(r))
	s.auditAs(r, username, "login", "", "")
	event.Info("login", username+" signed in", "from "+clientIP(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handle2FAPage shows the TOTP prompt for a session pending confirmation.
func (s *Server) handle2FAPage(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(auth.SessionCookie)
	if cookie == nil || !auth.PendingSession(s.db, cookie.Value) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.render(w, r, "twofa", map[string]any{"Title": "Two-factor authentication", "HideNav": true})
}

func (s *Server) handle2FAVerify(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(auth.SessionCookie)
	if cookie == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	// A code is six digits: without a limit here, a password alone would be
	// enough to walk through all of them.
	twofaPage := map[string]any{"Title": "Two-factor authentication", "HideNav": true}
	if s.refuse(w, r, "twofa", twofaPage) {
		return
	}
	if err := auth.Confirm2FA(s.db, cookie.Value, r.FormValue("code")); err != nil {
		_, username, _, _ := s.currentUser(r)
		s.auditAs(r, db.ActorSystem, "2fa.failed", username, "")
		s.loginFailures.record(clientIP(r), "2FA code", username)
		if s.countFailure(w, r, "twofa", twofaPage) {
			return
		}
		twofaPage["Error"] = "Invalid code, try again."
		s.render(w, r, "twofa", twofaPage)
		return
	}
	s.loginGuard.succeeded(clientIP(r))
	s.audit(r, "login", "", "second factor confirmed")
	event.Info("login", s.actor(r)+" signed in", "with 2FA", "from "+clientIP(r))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, _ := r.Cookie(auth.SessionCookie); cookie != nil {
		s.audit(r, "logout", "", "")
		// The cookie is cleared either way, but a session row that survives is
		// still a working credential for anyone who kept a copy of the token.
		if err := auth.Logout(s.db, cookie.Value); err != nil {
			log.Printf("logout: invalidating the session: %v", err)
		}
	}
	auth.ClearCookie(w, s.cfg.CookieSecure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// refuse answers a sign-in step from an address that has failed too often,
// and reports whether it did.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, page string, data map[string]any) bool {
	wait, blocked := s.loginGuard.blocked(clientIP(r))
	if !blocked {
		return false
	}
	s.renderLocked(w, r, page, data, wait)
	return true
}

// countFailure counts a failed step against the address it came from, and
// when that is the one that locks it out, says so instead of the usual error.
func (s *Server) countFailure(w http.ResponseWriter, r *http.Request, page string, data map[string]any) bool {
	ip := clientIP(r)
	locked, p := s.loginGuard.failed(ip)
	if !locked {
		return false
	}
	detail := fmt.Sprintf("after %d failed attempts", p.Attempts)
	event.Warning("login", ip+" refused for "+humanWait(p.Lock), detail)
	s.auditAs(r, db.ActorSystem, "login.lockout", ip, detail+", for "+humanWait(p.Lock))
	s.renderLocked(w, r, page, data, p.Lock)
	return true
}

func (s *Server) renderLocked(w http.ResponseWriter, r *http.Request, page string, data map[string]any, wait time.Duration) {
	data["Error"] = "Too many failed attempts from your address. Try again in " + humanWait(wait) + "."
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	w.WriteHeader(http.StatusTooManyRequests)
	s.render(w, r, page, data)
}
