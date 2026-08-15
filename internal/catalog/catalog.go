// Package catalog holds the one-click application templates. Selecting one
// prefills the "new application" form; {{RANDOM}} placeholders in env vars are
// replaced with a fresh secret at that moment.
//
// An entry is either a single image or a whole compose stack. Most of the
// self-hosted software people actually want is several containers — an app, a
// database and a cache — so the single-image shape the catalogue started with
// could only ever have described the minority of it.
//
// Compose entries interpolate ${VAR} from the app's .env, which Quasar writes
// beside the compose file and passes with --env-file, so a stack's secrets are
// generated once here and referenced from both places. Relative paths resolve
// against apps/<id>/, and apps/<id>/data is created before the first deploy —
// stacks bind their state under ./data so the backup archive picks it up,
// which a named volume would not.
//
// # Layout
//
// This file holds the type and the helpers; every entry lives in its own file
// named after its ID, and Templates below lists them in presentation order.
// Adding one means writing <id>.go and adding a line to that list — a test
// fails if the two ever disagree, because Go compiles an unreferenced entry
// file happily and it would simply never appear.
//
// # Checking it
//
// An entry is data, and data with a typo in it fails on somebody's server
// rather than in review. Two opt-in tests check the catalogue against the
// world: TestEveryImageStillExists asks each registry whether the image is
// still there, and TestDeployEveryTemplate actually deploys every entry and
// waits for it to answer. See their comments for how to run them.
package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Categories in the order the catalogue presents them: the reasons people
// install a server at all, roughly most common first.
var Categories = []string{
	"Media",
	"Files & sync",
	"Downloads",
	"Notes & docs",
	"Tasks & projects",
	"Dashboards & monitoring",
	"Security",
	"Development",
	"Automation",
	"Reading & RSS",
	"Analytics",
	"Websites",
	"Utilities",
	"Databases",
	"Game servers",
}

type Template struct {
	ID          string
	Name        string
	Description string
	Category    string

	// DeployType is "image" or "compose"; empty reads as "image".
	DeployType string

	// Image deploys.
	ImageRef  string
	DataMount string // container path bound to apps/<id>/data, empty = no volume

	// Compose deploys. ComposeService names the service the domain routes to,
	// left empty where the file has only one plausible candidate and Quasar
	// can work it out itself.
	Compose        string
	ComposeService string

	// Port the domain routes to, for either deploy type.
	Port int

	// Env is .env content. {{RANDOM}} becomes a fresh secret per selection.
	Env string

	// Raw marks a server that speaks its own protocol rather than HTTP, so it
	// is reached at the host's address and port instead of at the subdomain.
	// The smoke test reads this too: an HTTP probe would never pass on one.
	Raw bool

	// NeedsSetup names what the operator has to supply before the app will
	// start at all — credentials for something outside this server, which
	// Quasar cannot invent. These are the only entries that do not come up on
	// their own, so they say so on the card rather than failing silently after
	// the deploy, and the smoke test skips them for the same reason.
	NeedsSetup string

	// Note is the one thing worth reading before the form is submitted: a URL
	// that has to be filled in, or which port a Raw server listens on.
	Note string
}

// Caveat is the note as the page shows it: what the entry needs before it will
// run, then the standing explanation for a server that does not speak HTTP,
// then whatever else the entry has to say.
func (t Template) Caveat() string {
	var parts []string
	if t.NeedsSetup != "" {
		parts = append(parts, "Does not start on its own: "+t.NeedsSetup)
	}
	if t.Raw {
		parts = append(parts, noteRawPort)
	}
	if t.Note != "" {
		parts = append(parts, t.Note)
	}
	return strings.Join(parts, " ")
}

// Type is the deploy type to prefill, defaulting to a single image.
func (t Template) Type() string {
	if t.DeployType == "" {
		return "image"
	}
	return t.DeployType
}

// noteRawPort is carried by every server that speaks its own protocol rather
// than HTTP. Traefik holds :80 and :443 and routes by Host header, which a
// game client never sends, so these are reached at the host's own address and
// the subdomain the form insists on stays unused.
const noteRawPort = "Not an HTTP app: reach it at your server's IP on the port below, not at the subdomain. " +
	"The subdomain is still required by the form but will not serve anything."

// Templates is the catalogue in the order it is presented, one entry per
// file beside this one. Keeping the order here rather than letting it fall
// out of the file names means the list reads as a table of contents, and
// moving an entry is a one-line change.
var Templates = []Template{
	// --- Media -------------------------------------------------------------
	jellyfin,
	immich,
	navidrome,
	audiobookshelf,
	calibreWeb,
	// --- Files & sync ------------------------------------------------------
	nextcloud,
	syncthing,
	filebrowser,
	// --- Downloads ---------------------------------------------------------
	qbittorrent,
	sonarr,
	radarr,
	prowlarr,
	sabnzbd,
	jellyseerr,
	// --- Notes & docs ------------------------------------------------------
	memos,
	trilium,
	outline,
	docmost,
	wikijs,
	// --- Tasks & projects --------------------------------------------------
	vikunja,
	planka,
	// --- Dashboards & monitoring -------------------------------------------
	uptimeKuma,
	homepage,
	dashy,
	grafana,
	beszel,
	// --- Security ----------------------------------------------------------
	vaultwarden,
	authentik,
	authelia,
	// --- Development -------------------------------------------------------
	gitea,
	forgejo,
	registry,
	woodpecker,
	// --- Automation --------------------------------------------------------
	n8n,
	nodeRed,
	homeAssistant,
	// --- Reading & RSS -----------------------------------------------------
	freshrss,
	miniflux,
	wallabag,
	karakeep,
	// --- Analytics ---------------------------------------------------------
	umami,
	plausible,
	matomo,
	// --- Websites ----------------------------------------------------------
	ghost,
	wordpress,
	directus,
	// --- Utilities ---------------------------------------------------------
	paperlessNgx,
	stirlingPdf,
	itTools,
	actual,
	mealie,
	// --- Databases ---------------------------------------------------------
	postgres,
	mysql,
	mariadb,
	redis,
	mongo,
	// --- Game servers ------------------------------------------------------
	//
	// These publish their own host ports, which Quasar's compose adaptation
	// leaves alone as long as they are not 80 or 443. Change the host side of
	// a ports entry if two servers want the same number.
	minecraft,
	valheim,
	palworld,
	satisfactory,
	terraria,
	factorio,
}

// Group is one category with the templates filed under it.
type Group struct {
	Category  string
	Templates []Template
}

// Grouped returns the templates by category, in Categories order. A category
// with no entries is left out, and a template filed under an unknown category
// would vanish — the test in this package guards against that.
func Grouped() []Group {
	var out []Group
	for _, c := range Categories {
		var in []Template
		for _, t := range Templates {
			if t.Category == c {
				in = append(in, t)
			}
		}
		if len(in) > 0 {
			out = append(out, Group{Category: c, Templates: in})
		}
	}
	return out
}

// Get returns the template with the given ID, or nil.
func Get(id string) *Template {
	for i := range Templates {
		if Templates[i].ID == id {
			return &Templates[i]
		}
	}
	return nil
}

// RenderEnv resolves an entry's placeholders for the app about to be created:
// {{RANDOM}} becomes a fresh secret, {{HOST}} the app's public hostname and
// {{URL}} its https address.
//
// Filling the address in matters more than it looks. A dozen of these refuse to
// work until they are told the URL they are served on — Outline will not start,
// Paperless rejects its own login form as a CSRF failure, Ghost builds every
// link from it — and Quasar already knows the answer, so asking the operator to
// paste it back in only creates a way to get it wrong. An empty host leaves the
// placeholders in place rather than writing a URL with a hole in it.
func (t *Template) RenderEnv(host string) string {
	env := t.Env
	for strings.Contains(env, "{{RANDOM}}") {
		buf := make([]byte, 16)
		rand.Read(buf)
		env = strings.Replace(env, "{{RANDOM}}", hex.EncodeToString(buf), 1)
	}
	if host != "" {
		env = strings.ReplaceAll(env, "{{HOST}}", host)
		env = strings.ReplaceAll(env, "{{URL}}", "https://"+host)
	}
	return env
}
