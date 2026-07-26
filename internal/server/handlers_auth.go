package server

import (
	"net/http"

	"quasar/internal/auth"
	"quasar/internal/db"
)

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "login", map[string]any{"Title": "Sign in", "HideNav": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	token, needs2FA, err := auth.Login(s.db, username, r.FormValue("password"))
	if err != nil {
		// Rejected attempts are the whole point of auditing logins: a run of
		// them against one account is the only warning of a password attack.
		// The submitted name is recorded as the target, never as the actor.
		s.auditAs(r, db.ActorSystem, "login.failed", username, "")
		s.render(w, r, "login", map[string]any{
			"Title": "Sign in", "HideNav": true,
			"Error": "Invalid username or password.",
		})
		return
	}
	auth.SetCookie(w, token, s.cfg.CookieSecure)
	if needs2FA {
		http.Redirect(w, r, "/2fa", http.StatusSeeOther)
		return
	}
	s.auditAs(r, username, "login", "", "")
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
	if err := auth.Confirm2FA(s.db, cookie.Value, r.FormValue("code")); err != nil {
		_, username, _, _ := s.currentUser(r)
		s.auditAs(r, db.ActorSystem, "2fa.failed", username, "")
		s.render(w, r, "twofa", map[string]any{
			"Title": "Two-factor authentication", "HideNav": true,
			"Error": "Invalid code, try again.",
		})
		return
	}
	s.audit(r, "login", "", "second factor confirmed")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, _ := r.Cookie(auth.SessionCookie); cookie != nil {
		s.audit(r, "logout", "", "")
		auth.Logout(s.db, cookie.Value)
	}
	auth.ClearCookie(w, s.cfg.CookieSecure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
