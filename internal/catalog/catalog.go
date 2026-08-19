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
// # Parameters
//
// An entry may declare Params — a version, a flavour, a host port — whose
// values are substituted for {{NAME}} before the form is prefilled. This is
// what lets one entry stand for a fleet rather than for one installation: a
// single Minecraft entry covers vanilla and modded, every version, however many
// servers the operator runs. See params.go.
//
// # Layout
//
// This file holds the types and the helpers; every built-in entry lives in its
// own file named after its ID, and Templates below lists them in presentation
// order. Adding one means writing <id>.go and adding a line to that list — a
// test fails if the two ever disagree, because Go compiles an unreferenced
// entry file happily and it would simply never appear.
//
// An operator's own catalogue is the same shape read from YAML rather than
// compiled (yaml.go), and Catalog.Merge lays it over the built-in one.
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
	"slices"
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

// Template is one catalogue entry: an app a catalogue offers, with the
// parameters its deploy form asks for.
//
// The yaml tags are the format an operator's own catalogue is written in, so
// there is one entry shape rather than two that drift: what the files beside
// this express in Go, a YAML document expresses in snake_case.
type Template struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Category    string `yaml:"category"`

	// Source names the operator catalogue this came from, empty for the ones
	// Quasar ships. It is what the card says under a custom entry, and the
	// only thing distinguishing an override from the built-in it replaced.
	// Merge stamps it; a document declaring one is ignored.
	Source string `yaml:"-"`

	// Params are the choices offered before the form is prefilled — a version,
	// a flavour, a host port. Their values are substituted for {{NAME}} in the
	// fields below; see params.go.
	Params []Param `yaml:"params,omitempty"`

	// AppName is the name to propose for the application, {{NAME}} and all,
	// defaulting to Name. It is separate from Name because they are read in
	// different places: Name titles the card in a catalogue of entries and has
	// to be plain, while this titles one deployment among several from the same
	// entry, where "Minecraft 1.20.1 PAPER" is the whole point of the field.
	AppName string `yaml:"app_name,omitempty"`

	// Subdomain is the address to propose, {{NAME}} and all, defaulting to the
	// ID. An entry meant to be deployed more than once parameterises this:
	// every Minecraft server proposing "minecraft" collides on the first one.
	Subdomain string `yaml:"subdomain,omitempty"`

	// DeployType is "image" or "compose"; empty reads as "image".
	DeployType string `yaml:"deploy_type,omitempty"`

	// Image deploys.
	ImageRef  string `yaml:"image_ref,omitempty"`
	DataMount string `yaml:"data_mount,omitempty"` // container path bound to apps/<id>/data, empty = no volume

	// Compose deploys. ComposeService names the service the domain routes to,
	// left empty where the file has only one plausible candidate and Quasar
	// can work it out itself.
	Compose        string `yaml:"compose,omitempty"`
	ComposeService string `yaml:"compose_service,omitempty"`

	// Port the domain routes to, for either deploy type.
	Port int `yaml:"port"`

	// Env is .env content. {{RANDOM}} becomes a fresh secret per selection.
	Env string `yaml:"env,omitempty"`

	// Raw marks a server that speaks its own protocol rather than HTTP, so it
	// is reached at the host's address and port instead of at the subdomain.
	// The smoke test reads this too: an HTTP probe would never pass on one.
	Raw bool `yaml:"raw,omitempty"`

	// NeedsSetup names what the operator has to supply before the app will
	// start at all — credentials for something outside this server, which
	// Quasar cannot invent. These are the only entries that do not come up on
	// their own, so they say so on the card rather than failing silently after
	// the deploy, and the smoke test skips them for the same reason.
	NeedsSetup string `yaml:"needs_setup,omitempty"`

	// Note is the one thing worth reading before the form is submitted: a URL
	// that has to be filled in, or which port a Raw server listens on.
	Note string `yaml:"note,omitempty"`
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

// Catalog is a set of entries and the order their categories are presented in.
// Quasar ships one, an operator may add their own, and the page shows them
// merged — which is why this is a value rather than the package-level lists it
// grew out of.
type Catalog struct {
	// Name is what the operator called this catalogue, empty for the built-in
	// one. It is stamped on every entry as it is merged.
	Name       string
	Categories []string
	Templates  []Template
}

// Builtin is the catalogue Quasar ships, compiled from the files beside this.
func Builtin() Catalog {
	return Catalog{Categories: Categories, Templates: Templates}
}

// Merge lays other catalogues over this one, in the order given.
//
// An entry whose ID is already present replaces it where it stood. That is the
// useful reading: an operator who writes their own "minecraft" means to use
// theirs wherever the built-in one appeared, not to be shown two cards with the
// same name and left to remember which is which.
//
// Categories keep this catalogue's order, with any the others introduce
// appended as they are declared — including one that only an entry mentions.
// Grouped drops an entry filed under a category nobody listed, and a hand-written
// catalogue disappearing because of a forgotten line is not a failure worth
// having.
func (c Catalog) Merge(others ...Catalog) Catalog {
	out := Catalog{
		Categories: slices.Clone(c.Categories),
		Templates:  slices.Clone(c.Templates),
	}
	for _, o := range others {
		for _, t := range o.Templates {
			t.Source = o.Name
			if i := slices.IndexFunc(out.Templates, func(x Template) bool { return x.ID == t.ID }); i >= 0 {
				out.Templates[i] = t
			} else {
				out.Templates = append(out.Templates, t)
			}
		}
		for _, cat := range append(slices.Clone(o.Categories), categoriesUsed(o.Templates)...) {
			if !slices.Contains(out.Categories, cat) {
				out.Categories = append(out.Categories, cat)
			}
		}
	}
	return out
}

// categoriesUsed lists the categories the entries file themselves under, in the
// order they first appear.
func categoriesUsed(ts []Template) []string {
	var out []string
	for _, t := range ts {
		if t.Category != "" && !slices.Contains(out, t.Category) {
			out = append(out, t.Category)
		}
	}
	return out
}

// Group is one category with the templates filed under it.
type Group struct {
	Category  string
	Templates []Template
}

// Grouped returns the templates by category, in the catalogue's category order.
// A category with no entries is left out, and a template filed under an unknown
// category would vanish — the test in this package guards against that for the
// built-in entries, and Merge does for an operator's.
func (c Catalog) Grouped() []Group {
	var out []Group
	for _, cat := range c.Categories {
		var in []Template
		for _, t := range c.Templates {
			if t.Category == cat {
				in = append(in, t)
			}
		}
		if len(in) > 0 {
			out = append(out, Group{Category: cat, Templates: in})
		}
	}
	return out
}

// Get returns the template with the given ID, or nil.
func (c Catalog) Get(id string) *Template {
	for i := range c.Templates {
		if c.Templates[i].ID == id {
			return &c.Templates[i]
		}
	}
	return nil
}

// Filled is an entry with its parameters and placeholders resolved: exactly
// what the new-application form is prefilled with.
type Filled struct {
	Name           string
	Subdomain      string
	DeployType     string
	ImageRef       string
	Compose        string
	ComposeService string
	Port           int
	DataMount      string
	Env            string
}

// SubdomainFor is the address to propose for an app created from this entry,
// with the parameters filled in and reduced to a legal DNS label.
func (t *Template) SubdomainFor(v Values) string {
	s := t.Subdomain
	if s == "" {
		s = t.ID
	}
	if out := slug(substitute(s, v)); out != "" {
		return out
	}
	return t.ID
}

// Fill resolves the entry for the app about to be created: parameter values
// substituted everywhere the entry writes {{NAME}}, then the placeholders
// Quasar answers itself.
//
// The address is passed in rather than worked out here. SubdomainFor proposes
// one, but only the caller can see whether an app already holds it and what to
// use instead — and the env cannot be rendered until that is settled, since
// {{URL}} has to name the address the app will really be served on.
func (t *Template) Fill(picked Values, subdomain, host string) Filled {
	v := t.Resolve(picked)
	name := t.AppName
	if name == "" {
		name = t.Name
	}
	return Filled{
		Name:           substitute(name, v),
		Subdomain:      subdomain,
		DeployType:     t.Type(),
		ImageRef:       substitute(t.ImageRef, v),
		Compose:        substitute(t.Compose, v),
		ComposeService: t.ComposeService,
		Port:           t.Port,
		DataMount:      t.DataMount,
		Env:            t.RenderEnv(host, v),
	}
}

// RenderEnv resolves an entry's env for the app about to be created: the
// parameter values picked for it, then {{RANDOM}} as a fresh secret, {{HOST}}
// as the app's public hostname and {{URL}} as its https address.
//
// Filling the address in matters more than it looks. A dozen of these refuse to
// work until they are told the URL they are served on — Outline will not start,
// Paperless rejects its own login form as a CSRF failure, Ghost builds every
// link from it — and Quasar already knows the answer, so asking the operator to
// paste it back in only creates a way to get it wrong. An empty host leaves the
// placeholders in place rather than writing a URL with a hole in it.
//
// {{RANDOM}} is resolved here and nowhere else, deliberately. Each occurrence
// draws a fresh secret, so the same placeholder written in both the env and the
// compose file would produce two different values for what is meant to be one
// secret; a stack references what is generated here as ${VAR} instead.
func (t *Template) RenderEnv(host string, v Values) string {
	env := substitute(t.Env, v)
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
