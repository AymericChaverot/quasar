package server

import (
	"net/http"
	"strconv"
	"strings"

	"quasar/internal/auth"
)

// settingsError re-renders the settings page with a message, so a failed user
// action reads like a form error instead of a bare HTTP status.
func (s *Server) settingsError(w http.ResponseWriter, r *http.Request, msg string) {
	data := s.settingsData(r)
	data["Error"] = msg
	s.render(w, r, "settings", data)
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	role := r.FormValue("role")
	if err := auth.CreateUser(s.db, username, r.FormValue("password"), role); err != nil {
		s.settingsError(w, r, err.Error())
		return
	}
	s.audit(r, "user.create", username, role)
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) handleUserRole(w http.ResponseWriter, r *http.Request) {
	id, target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	role := r.FormValue("role")
	// Demoting yourself would take away the ability to undo it. The last-admin
	// check in auth.SetRole does not catch this on its own: with two admins,
	// either one could still strand themselves.
	if selfID, _, _, _ := s.currentUser(r); selfID == id && role != auth.RoleAdmin {
		s.settingsError(w, r, "You cannot remove your own admin access — ask another admin to do it.")
		return
	}
	if err := auth.SetRole(s.db, id, role); err != nil {
		s.settingsError(w, r, err.Error())
		return
	}
	s.audit(r, "user.role", target, "set to "+role)
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	id, target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if err := auth.ResetPassword(s.db, id, r.FormValue("password")); err != nil {
		s.settingsError(w, r, err.Error())
		return
	}
	s.audit(r, "user.password-reset", target, "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id, target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if selfID, _, _, _ := s.currentUser(r); selfID == id {
		s.settingsError(w, r, "You cannot delete your own account.")
		return
	}
	if err := auth.DeleteUser(s.db, id); err != nil {
		s.settingsError(w, r, err.Error())
		return
	}
	s.audit(r, "user.delete", target, "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// handleTokenCreate issues an API token and shows the secret once. It is not
// stored, so there is no way to show it again.
func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	role := r.FormValue("role")
	secret, err := auth.CreateToken(s.db, name, role)
	if err != nil {
		s.settingsError(w, r, err.Error())
		return
	}
	s.audit(r, "token.create", name, role)

	data := s.settingsData(r)
	data["NewToken"] = secret
	data["Saved"] = "Token created — copy it now, it is not stored and cannot be shown again."
	s.render(w, r, "settings", data)
}

func (s *Server) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad token id", http.StatusBadRequest)
		return
	}
	name, err := auth.DeleteToken(s.db, id)
	if err != nil {
		s.settingsError(w, r, err.Error())
		return
	}
	s.audit(r, "token.delete", name, "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// targetUser resolves the {id} of a user-management route, returning the id and
// the username for the audit trail (looked up before the account is changed).
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad user id", http.StatusBadRequest)
		return 0, "", false
	}
	users, err := auth.ListUsers(s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return 0, "", false
	}
	for _, u := range users {
		if u.ID == id {
			return id, u.Username, true
		}
	}
	http.Error(w, "user not found", http.StatusNotFound)
	return 0, "", false
}
