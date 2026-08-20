package server

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"quasar/internal/auth"
	"quasar/internal/db"
	"quasar/internal/monitor"
	"quasar/internal/notify"
)

// Theme describes a selectable UI theme, rendered on the settings page.
type Theme struct {
	ID          string
	Name        string
	Description string
}

// themes[0] is the default every unset or unrecognised cookie falls back to.
var themes = []Theme{
	{"nebula", "Nebula", "Dark developer dashboard (default)"},
	{"marathon", "Marathon", "Bone and hazard orange, after Bungie's Marathon"},
	{"nord", "Nord", "Cool arctic blues, low contrast"},
	{"synthwave", "Synthwave", "Neon magenta on deep violet"},
	{"terminal", "Terminal", "Brutalist green-on-black CRT"},
	{"paper", "Paper", "Minimal light"},
	{"solarized", "Solarized", "Warm light, Solarized palette"},
}

const themeCookie = "quasar_theme"

func validTheme(id string) bool {
	for _, t := range themes {
		if t.ID == id {
			return true
		}
	}
	return false
}

// themeFrom resolves the active theme from the request cookie.
func themeFrom(r *http.Request) string {
	if c, _ := r.Cookie(themeCookie); c != nil && validTheme(c.Value) {
		return c.Value
	}
	return themes[0].ID
}

func (s *Server) handleThemeSet(w http.ResponseWriter, r *http.Request) {
	theme := r.FormValue("theme")
	if !validTheme(theme) {
		theme = themes[0].ID
	}
	http.SetCookie(w, &http.Cookie{
		Name:     themeCookie,
		Value:    theme,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// currentUser resolves the logged-in user from the session cookie. requireAuth
// already validated the session, so failures here are exceptional.
//
// An unresolved session yields an empty role, which requireAdmin treats as not
// an admin — failing closed if this ever returns early.
func (s *Server) currentUser(r *http.Request) (id int64, username, role, token string) {
	cookie, _ := r.Cookie(auth.SessionCookie)
	if cookie == nil {
		return 0, "", "", ""
	}
	id, username, role, err := auth.UserForSession(s.db, cookie.Value)
	if err != nil {
		return 0, "", "", cookie.Value
	}
	return id, username, role, cookie.Value
}

func (s *Server) settingsData(r *http.Request) map[string]any {
	userID, username, role, _ := s.currentUser(r)
	registries, _ := db.ListRegistries(s.db)
	users, _ := auth.ListUsers(s.db)
	tokens, _ := auth.ListTokens(s.db)
	gitCreds, _ := db.ListGitCredentials(s.db)
	return map[string]any{
		"Title":        "Settings",
		"Username":     username,
		"Role":         role,
		"IsAdmin":      role == auth.RoleAdmin,
		"UserID":       userID,
		"Users":        users,
		"Tokens":       tokens,
		"Themes":       themes,
		"Domain":       s.cfg.Domain,
		"AppsDir":      s.cfg.AppsDir,
		"DBPath":       s.cfg.DBPath,
		"Registries":   registries,
		"GitCredCount": len(gitCreds),
		// What the new-application page will actually offer, which is the
		// built-in entries plus whatever the operator's catalogues add or
		// replace — the merged count, not the sum.
		"CatalogEntryCount": len(s.catalog().Templates),
		"CatalogCount":      len(s.customCatalogs()),
		"StationCount":      len(s.stationViews()),
		"NotifyURL":         db.GetSetting(s.db, db.SettingNotifyURL),
		"NtfyURL":           db.GetSetting(s.db, db.SettingNtfyURL),
		"SMTPHost":          db.GetSetting(s.db, db.SettingSMTPHost),
		"SMTPPort":          db.GetSetting(s.db, db.SettingSMTPPort),
		"SMTPUser":          db.GetSetting(s.db, db.SettingSMTPUser),
		"SMTPFrom":          db.GetSetting(s.db, db.SettingSMTPFrom),
		"SMTPTo":            db.GetSetting(s.db, db.SettingSMTPTo),
		"SMTPPassSet":       db.GetSetting(s.db, db.SettingSMTPPassword) != "",
		"TOTPEnabled":       auth.TOTPEnabled(s.db, userID),
		"AlertDisk":         s.alertThreshold(db.SettingAlertDisk, monitor.AlertDefaultDisk),
		"AlertMem":          s.alertThreshold(db.SettingAlertMem, monitor.AlertDefaultMem),
		"AlertCPU":          s.alertThreshold(db.SettingAlertCPU, monitor.AlertDefaultCPU),
	}
}

// alertThreshold shows the value the monitor will actually use, so the form
// reflects the effective default rather than an empty box.
func (s *Server) alertThreshold(key string, fallback int) int {
	if v, err := strconv.Atoi(db.GetSetting(s.db, key)); err == nil {
		return v
	}
	return fallback
}

// handle2FASetupBegin generates a secret and shows the QR code to scan.
func (s *Server) handle2FASetupBegin(w http.ResponseWriter, r *http.Request) {
	userID, username, _, _ := s.currentUser(r)
	secret, qr, err := auth.BeginTOTPSetup(s.db, userID, "Quasar ("+s.cfg.Domain+")", username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := s.settingsData(r)
	// template.URL marks the data: URI as safe — html/template would
	// otherwise replace it with #ZgotmplZ.
	data["TOTPSetup"] = map[string]any{"Secret": secret, "QR": template.URL(qr)}
	s.render(w, r, "settings", data)
}

func (s *Server) handle2FAEnable(w http.ResponseWriter, r *http.Request) {
	userID, _, _, _ := s.currentUser(r)
	if err := auth.EnableTOTP(s.db, userID, r.FormValue("code")); err != nil {
		data := s.settingsData(r)
		data["Error"] = err.Error() + " — scan the QR code again if needed."
		s.render(w, r, "settings", data)
		return
	}
	s.audit(r, "2fa.enable", "", "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) handle2FADisable(w http.ResponseWriter, r *http.Request) {
	userID, _, _, _ := s.currentUser(r)
	if err := auth.DisableTOTP(s.db, userID, r.FormValue("password")); err != nil {
		data := s.settingsData(r)
		data["Error"] = err.Error()
		s.render(w, r, "settings", data)
		return
	}
	s.audit(r, "2fa.disable", "", "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// handleRegistryAdd stores (or replaces) credentials for an image registry.
func (s *Server) handleRegistryAdd(w http.ResponseWriter, r *http.Request) {
	server := strings.TrimSpace(r.FormValue("server"))
	username := strings.TrimSpace(r.FormValue("username"))
	secret := r.FormValue("secret")
	if server == "" || username == "" || secret == "" {
		data := s.settingsData(r)
		data["Error"] = "Registry server, username and token are all required."
		s.render(w, r, "settings", data)
		return
	}
	if err := db.InsertRegistry(s.db, &db.Registry{Server: server, Username: username, Secret: secret}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "registry.add", server, username)
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) handleRegistryDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := db.DeleteRegistry(s.db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "registry.delete", strconv.FormatInt(id, 10), "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// handleIntegrationsSave stores the notification and alerting configuration.
// Git credentials moved to their own page once one token per forge became the
// point; see handlers_git.go.
func (s *Server) handleIntegrationsSave(w http.ResponseWriter, r *http.Request) {
	// The form is saved key by key, but it is one action to the operator: if
	// any key failed, saying "saved" would be a lie, and the first failure is
	// the one worth showing — the rest are the same locked database twice.
	var failed error
	save := func(key, value string) {
		if err := db.SetSetting(s.db, key, value); err != nil && failed == nil {
			failed = err
		}
	}

	save(db.SettingNotifyURL, strings.TrimSpace(r.FormValue("notify_url")))

	// Thresholds are stored as typed, so an unparseable or out-of-range value
	// is dropped rather than silently disabling the alert (0 means "off").
	for field, key := range map[string]string{
		"alert_disk": db.SettingAlertDisk,
		"alert_mem":  db.SettingAlertMem,
		"alert_cpu":  db.SettingAlertCPU,
	} {
		raw := strings.TrimSpace(r.FormValue(field))
		if raw == "" {
			continue
		}
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 && v <= 100 {
			save(key, strconv.Itoa(v))
		}
	}
	save(db.SettingNtfyURL, strings.TrimSpace(r.FormValue("ntfy_url")))
	save(db.SettingSMTPHost, strings.TrimSpace(r.FormValue("smtp_host")))
	save(db.SettingSMTPFrom, strings.TrimSpace(r.FormValue("smtp_from")))
	save(db.SettingSMTPTo, strings.TrimSpace(r.FormValue("smtp_to")))
	save(db.SettingSMTPUser, strings.TrimSpace(r.FormValue("smtp_user")))
	if port := strings.TrimSpace(r.FormValue("smtp_port")); port != "" {
		if v, err := strconv.Atoi(port); err == nil && v > 0 && v <= 65535 {
			save(db.SettingSMTPPort, strconv.Itoa(v))
		}
	}
	// Blank means "keep the stored password", so saving the rest of the form
	// does not silently wipe a credential the field never displays.
	if pw := r.FormValue("smtp_password"); pw != "" {
		save(db.SettingSMTPPassword, pw)
	}
	if r.FormValue("clear_smtp_password") == "on" {
		save(db.SettingSMTPPassword, "")
	}
	if failed != nil {
		http.Error(w, failed.Error(), http.StatusInternalServerError)
		return
	}

	// No values in the detail: this form carries a git access token and an SMTP
	// password.
	s.audit(r, "settings.integrations", "", "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// platform, so this is the only way to know delivery works.
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "notify.test", "", "")
	if err := notify.Test(s.db); err != nil {
		s.settingsError(w, r, "Test notification failed — "+err.Error())
		return
	}
	data := s.settingsData(r)
	data["Saved"] = "Test notification sent to every configured channel."
	s.render(w, r, "settings", data)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	data := s.settingsData(r)
	if r.URL.Query().Get("saved") == "1" {
		data["Saved"] = "Settings saved."
	}
	s.render(w, r, "settings", data)
}

func (s *Server) handlePasswordChange(w http.ResponseWriter, r *http.Request) {
	userID, _, _, token := s.currentUser(r)

	newPassword := r.FormValue("new_password")
	if newPassword != r.FormValue("confirm_password") {
		data := s.settingsData(r)
		data["Error"] = "New passwords do not match."
		s.render(w, r, "settings", data)
		return
	}
	if err := auth.ChangePassword(s.db, userID, token, r.FormValue("current_password"), newPassword); err != nil {
		data := s.settingsData(r)
		data["Error"] = err.Error()
		s.render(w, r, "settings", data)
		return
	}
	s.audit(r, "password.change", "", "")
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

func (s *Server) handleSessionsClear(w http.ResponseWriter, r *http.Request) {
	if err := auth.ClearAllSessions(s.db); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "sessions.clear", "", "every session invalidated")
	auth.ClearCookie(w, s.cfg.CookieSecure)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
