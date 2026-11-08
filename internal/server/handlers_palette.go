package server

import (
	"encoding/json"
	"net/http"

	"quasar/internal/db"
)

// The command palette.
//
// Ctrl+K, or /, on any page: a box to type a few letters of what you are
// after — an application, a page, a section of the settings, something to do
// — and go there. palette.js draws it and does the matching; this is what it
// matches against, asked afresh each time it opens so the states are now's.
// It holds only what the reader may reach: a viewer is given no admin page and
// no action, which would only answer them with a 403.

// PaletteIndex is everything the palette can find.
type PaletteIndex struct {
	Admin   bool           `json:"admin"`
	Apps    []PaletteApp   `json:"apps"`
	Pages   []PalettePage  `json:"pages"`
	Actions []PaletteDo    `json:"actions"`
	Themes  []PaletteTheme `json:"themes"`
}

// PaletteApp is an application, with what the palette offers to do with it.
type PaletteApp struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Host  string `json:"host,omitempty"`
	State string `json:"state"`
	// Git, Compose say which words the redeploy and update questions use.
	DeployType string `json:"deployType"`
	// Address is where a stack that publishes a port of its own is reached
	// — a game server, a database — for the palette to copy; Proto is the
	// protocol it speaks.
	Address string `json:"address,omitempty"`
	Proto   string `json:"proto,omitempty"`
}

// PalettePage is a page, or a section of one.
type PalettePage struct {
	Title string `json:"title"`
	// Within is the page a section belongs to, shown beside it.
	Within string `json:"within,omitempty"`
	Href   string `json:"href"`
	// Words are other words it is found by.
	Words string `json:"words,omitempty"`
}

// PaletteDo is something to do that is not about one application: a form the
// palette sends, asking first when Confirm says so.
type PaletteDo struct {
	Title   string `json:"title"`
	Within  string `json:"within,omitempty"`
	Action  string `json:"action"`
	Confirm string `json:"confirm,omitempty"`
	Words   string `json:"words,omitempty"`
}

// PaletteTheme is a theme to switch to.
type PaletteTheme struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// palettePages is every page and section, in the order they are offered when
// nothing narrows them; admin marks those a viewer cannot open.
var palettePages = []struct {
	PalettePage
	admin bool
}{
	{PalettePage{Title: "Dashboard", Href: "/", Words: "home applications server health"}, false},
	{PalettePage{Title: "New application", Href: "/apps/new", Words: "create deploy add catalogue"}, true},
	{PalettePage{Title: "Notifications", Href: "/notifications", Words: "alerts bell history"}, false},
	{PalettePage{Title: "Logs", Href: "/logs", Words: "search"}, false},
	{PalettePage{Title: "Audit", Href: "/audit", Words: "trail who did what"}, false},
	{PalettePage{Title: "System", Href: "/system"}, false},
	{PalettePage{Title: "Software update", Within: "System", Href: "/system#update", Words: "upgrade version release"}, false},
	{PalettePage{Title: "Hardware", Within: "System", Href: "/system#hardware", Words: "cpu memory disk"}, false},
	{PalettePage{Title: "TLS certificates", Within: "System", Href: "/system#certificates", Words: "https ssl letsencrypt"}, false},
	{PalettePage{Title: "Docker storage", Within: "System", Href: "/system#storage", Words: "disk cleanup images prune"}, false},
	{PalettePage{Title: "Volumes", Within: "System", Href: "/system#volumes", Words: "data files"}, true},
	{PalettePage{Title: "Backups", Within: "System", Href: "/system#backups", Words: "restore offsite s3"}, true},
	{PalettePage{Title: "Settings", Href: "/settings"}, false},
	{PalettePage{Title: "Appearance", Within: "Settings", Href: "/settings#appearance", Words: "theme dark light"}, false},
	{PalettePage{Title: "Account", Within: "Settings", Href: "/settings#account", Words: "password"}, false},
	{PalettePage{Title: "Two-factor authentication", Within: "Settings", Href: "/settings#two-factor", Words: "2fa totp otp"}, false},
	{PalettePage{Title: "Users", Within: "Settings", Href: "/settings#users", Words: "accounts roles viewer admin"}, true},
	{PalettePage{Title: "Personal tokens", Within: "Settings", Href: "/settings#personal-tokens", Words: "api keys cli qsr login"}, false},
	{PalettePage{Title: "Service tokens", Within: "Settings", Href: "/settings#tokens", Words: "api keys ci pipeline"}, true},
	{PalettePage{Title: "Catalogues", Within: "Settings", Href: "/settings/catalogs", Words: "templates"}, true},
	{PalettePage{Title: "Status", Within: "Settings", Href: "/settings/status", Words: "public page uptime incidents api"}, true},
	{PalettePage{Title: "Stations", Within: "Settings", Href: "/settings/stations", Words: "plugins extensions"}, true},
	{PalettePage{Title: "Git credentials", Within: "Settings", Href: "/settings/git", Words: "github gitlab ssh deploy key"}, true},
	{PalettePage{Title: "Image registries", Within: "Settings", Href: "/settings#registries", Words: "docker ghcr login"}, true},
	{PalettePage{Title: "Integrations", Within: "Settings", Href: "/settings#integrations", Words: "notifications webhook ntfy email smtp alerts thresholds"}, true},
	{PalettePage{Title: "Sign-in protection", Within: "Settings", Href: "/settings#sign-in", Words: "brute force lockout"}, true},
	{PalettePage{Title: "Logs & audit retention", Within: "Settings", Href: "/settings#retention", Words: "purge keep"}, true},
	{PalettePage{Title: "Sessions", Within: "Settings", Href: "/settings#sessions", Words: "sign out everywhere"}, true},
}

// paletteActions are the things to do that are about the whole server.
var paletteActions = []PaletteDo{
	{Title: "Run a backup now", Within: "System", Action: "/system/backup", Words: "save archive"},
	{Title: "Check for a Quasar update", Within: "System", Action: "/system/update/check", Words: "upgrade version release"},
}

// paletteChoices is what the palette offers besides the applications: the
// pages and actions the reader's role reaches, and the themes.
func paletteChoices(admin, stations bool, theme string) ([]PalettePage, []PaletteDo, []PaletteTheme) {
	var pages []PalettePage
	for _, p := range palettePages {
		if p.admin && !admin {
			continue
		}
		pages = append(pages, p.PalettePage)
	}
	// The stations' own page is in the header only once there is one.
	if stations {
		pages = append(pages, PalettePage{Title: "Stations", Href: "/stations", Words: "plugins panels"})
	}
	actions := []PaletteDo{}
	if admin {
		actions = append(actions, paletteActions...)
	}
	var ts []PaletteTheme
	for _, t := range themes {
		ts = append(ts, PaletteTheme{ID: t.ID, Name: t.Name, Current: t.ID == theme})
	}
	return pages, actions, ts
}

// handlePalette is what the palette matches against.
func (s *Server) handlePalette(w http.ResponseWriter, r *http.Request) {
	admin := s.isAdmin(r)
	idx := PaletteIndex{Admin: admin, Apps: []PaletteApp{}, Actions: []PaletteDo{}}

	apps, err := db.ListApps(s.db, s.keyring)
	if err != nil {
		http.Error(w, "the applications could not be read", http.StatusInternalServerError)
		return
	}
	for _, v := range s.appViews(r, apps) {
		app := PaletteApp{ID: v.ID, Name: v.Name, Host: v.Host(), State: v.Status.State, DeployType: v.DeployType}
		if ports := v.PublicPorts(); len(ports) > 0 {
			app.Address, app.Proto = ports[0].Dial, ports[0].Proto
		}
		idx.Apps = append(idx.Apps, app)
	}

	idx.Pages, idx.Actions, idx.Themes = paletteChoices(admin, db.CountEnabledStations(s.db) > 0, themeFrom(r))

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(idx)
}
