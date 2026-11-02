package server

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"quasar/internal/db"
)

// The status settings: who may read the API, what it publishes, and which
// applications under which names. The page that shows it is further down
// the same settings page.

// statusAppRow is one application in the list of those that may be published.
type statusAppRow struct {
	ID, Name, HealthPath string
	Published            bool
	PublicName           string
}

// Lengths a status's words are held to: a title is a line, a description a
// sentence, a name a word or two.
const (
	statusTitleMax       = 80
	statusDescriptionMax = 200
	statusNameMax        = 60
)

func (s *Server) handleStatusSettings(w http.ResponseWriter, r *http.Request) {
	cfg := loadStatusConfig(s.db)
	published, _ := db.StatusApps(s.db)
	apps, _ := db.ListApps(s.db, s.keyring)
	rows := make([]statusAppRow, 0, len(apps))
	for _, a := range apps {
		name, ok := published[a.ID]
		rows = append(rows, statusAppRow{ID: a.ID, Name: a.Name, HealthPath: a.HealthPath, Published: ok, PublicName: name})
	}
	s.render(w, r, "status_settings", map[string]any{
		"Title":    "Status",
		"Status":   cfg,
		"Modes":    statusModes,
		"Apps":     rows,
		"Endpoint": "https://admin." + s.cfg.Domain + "/api/v1/status",
		"Domain":   s.cfg.Domain,
	})
}

func (s *Server) handleStatusSettingsSave(w http.ResponseWriter, r *http.Request) {
	back := func(n Notice) { s.redirectWith(w, r, "/settings/status", n) }
	if err := r.ParseForm(); err != nil {
		back(warnNotice("Status not saved", "The form could not be read."))
		return
	}
	mode := r.FormValue("mode")
	if !slices.Contains(statusModes, mode) {
		back(warnNotice("Status not saved", "Pick who may read the status: nobody, token holders or anybody."))
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = "Status"
	}
	description := strings.TrimSpace(r.FormValue("description"))
	link := strings.TrimSpace(r.FormValue("link"))
	if problem := statusTextProblem(title, description, link); problem != "" {
		back(warnNotice("Status not saved", problem))
		return
	}

	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		back(errNotice("Status not saved", "", err))
		return
	}
	chosen := r.Form["publish"]
	var published []db.StatusApp
	for _, a := range apps {
		if !slices.Contains(chosen, a.ID) {
			continue
		}
		name := strings.TrimSpace(r.FormValue("name_" + a.ID))
		if utf8.RuneCountInString(name) > statusNameMax {
			back(warnNotice("Status not saved", fmt.Sprintf("A public name is at most %d characters.", statusNameMax)))
			return
		}
		published = append(published, db.StatusApp{AppID: a.ID, Name: name})
	}

	settings := map[string]string{
		db.SettingStatusMode:          mode,
		db.SettingStatusCORS:          checkbox(r, "cors"),
		db.SettingStatusShowState:     checkbox(r, "show_state"),
		db.SettingStatusShowUptime:    checkbox(r, "show_uptime"),
		db.SettingStatusShowIncidents: checkbox(r, "show_incidents"),
		db.SettingStatusShowServer:    checkbox(r, "show_server"),
		db.SettingStatusTitle:         title,
		db.SettingStatusDescription:   description,
		db.SettingStatusLink:          link,
	}
	for k, v := range settings {
		if err := db.SetSetting(s.db, k, v); err != nil {
			back(errNotice("Status not saved", "", err))
			return
		}
	}
	if err := db.SetStatusApps(s.db, published); err != nil {
		back(errNotice("Status not saved", "", err))
		return
	}
	s.status.forget()

	said := statusModeWords(mode) + ", " + count(int64(len(published)), "application", "applications") + " published"
	s.audit(r, "settings.status", "", said)
	back(okNotice("Status saved", strings.ToUpper(said[:1])+said[1:]+"."))
}

func checkbox(r *http.Request, name string) string {
	if r.FormValue(name) != "" {
		return "true"
	}
	return "false"
}

// statusTextProblem is what is wrong with the words a status presents itself
// with, or nothing.
func statusTextProblem(title, description, link string) string {
	switch {
	case utf8.RuneCountInString(title) > statusTitleMax:
		return fmt.Sprintf("The title is at most %d characters.", statusTitleMax)
	case utf8.RuneCountInString(description) > statusDescriptionMax:
		return fmt.Sprintf("The description is at most %d characters.", statusDescriptionMax)
	}
	if link != "" {
		u, err := url.Parse(link)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return "The link has to be a web address, starting with https://."
		}
	}
	return ""
}

// statusModeWords says a mode the way the audit trail and the notices do.
func statusModeWords(mode string) string {
	switch mode {
	case "token":
		return "readable with an API token"
	case "public":
		return "public"
	}
	return "API off"
}

// statusSummary is the settings page's one line about the status.
func (s *Server) statusSummary() string {
	cfg := loadStatusConfig(s.db)
	published, _ := db.StatusApps(s.db)
	n := len(published)
	line := statusModeWords(cfg.Mode)
	if cfg.Page {
		line += " · page on " + cfg.Subdomain + "." + s.cfg.Domain
	}
	return line + " · " + count(int64(n), "application", "applications") + " published"
}
