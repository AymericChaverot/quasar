package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"quasar/internal/auth"
	"quasar/internal/event"
)

// redirectSettings sends the browser back to Settings with how it went, so a
// failed user action reads like any other notice instead of a bare HTTP status.
func (s *Server) redirectSettings(w http.ResponseWriter, r *http.Request, n Notice) {
	s.redirectWith(w, r, "/settings", n)
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	role := r.FormValue("role")
	if err := auth.CreateUser(s.db, username, r.FormValue("password"), role); err != nil {
		s.redirectSettings(w, r, errNotice("User not created", "", err))
		return
	}
	s.audit(r, "user.create", username, role)
	event.Info("users", fmt.Sprintf("%q created", username), role, "by "+s.actor(r))
	s.redirectSettings(w, r, okNotice("User created", username+", as "+role+"."))
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
		s.redirectSettings(w, r, warnNotice("Role not changed", "You cannot remove your own admin access — ask another admin to do it."))
		return
	}
	if err := auth.SetRole(s.db, id, role); err != nil {
		s.redirectSettings(w, r, errNotice("Role not changed", "", err))
		return
	}
	s.audit(r, "user.role", target, "set to "+role)
	event.Info("users", fmt.Sprintf("%q is now %s", target, role), "by "+s.actor(r))
	s.redirectSettings(w, r, okNotice("Role changed", target+" is now "+role+"."))
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	id, target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if err := auth.ResetPassword(s.db, id, r.FormValue("password")); err != nil {
		s.redirectSettings(w, r, errNotice("Password not reset", "", err))
		return
	}
	s.audit(r, "user.password-reset", target, "")
	event.Info("users", fmt.Sprintf("%q had their password reset", target), "by "+s.actor(r))
	s.redirectSettings(w, r, okNotice("Password reset", target))
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id, target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if selfID, _, _, _ := s.currentUser(r); selfID == id {
		s.redirectSettings(w, r, warnNotice("User not deleted", "You cannot delete your own account."))
		return
	}
	if err := auth.DeleteUser(s.db, id); err != nil {
		s.redirectSettings(w, r, errNotice("User not deleted", "", err))
		return
	}
	s.audit(r, "user.delete", target, "")
	event.Info("users", fmt.Sprintf("%q deleted", target), "by "+s.actor(r))
	s.redirectSettings(w, r, okNotice("User deleted", target))
}

// handleTokenCreate issues an API token and shows the secret once. It is not
// stored, so there is no way to show it again.
func (s *Server) handleTokenCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	role := r.FormValue("role")
	secret, err := auth.CreateToken(s.db, name, role)
	if err != nil {
		s.redirectSettings(w, r, errNotice("Token not created", "", err))
		return
	}
	s.audit(r, "token.create", name, role)
	event.Info("api token", fmt.Sprintf("%q created", name), role, "by "+s.actor(r))

	// Drawn rather than redirected to: the secret is on this page and nowhere
	// else, ever. The toast is drawn with it.
	s.flash(r, okNotice("Token created", name+" — copy it now: it is not stored and cannot be shown again."))
	data := s.settingsData(r)
	data["NewToken"] = secret
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
		s.redirectSettings(w, r, errNotice("Token not revoked", "", err))
		return
	}
	s.audit(r, "token.delete", name, "")
	event.Info("api token", fmt.Sprintf("%q revoked", name), "by "+s.actor(r))
	s.redirectSettings(w, r, okNotice("Token revoked", name))
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
		s.redirectSettings(w, r, errNotice("Users unavailable", "", err))
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
