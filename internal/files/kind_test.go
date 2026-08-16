package files

import "testing"

func TestKind(t *testing.T) {
	cases := map[string]string{
		// The case that started this: a config file should look like one.
		"postgresql.conf":  "config",
		"nginx.CONF":       "config",
		"config.yml":       "config",
		"settings.json":    "config",
		"php.ini":          "config",
		"Dockerfile":       "config",
		".env":             "config",
		".gitignore":       "config",
		"unknown.whatever": "file",

		"entrypoint.sh": "code",
		"index.html":    "code",
		"schema.sql":    "code",

		"README.md":      "text",
		"LICENSE":        "text",
		"postgresql.log": "text",

		"avatar.png": "image",
		"logo.svg":   "image",

		"backup.tar.gz": "archive",
		"dump.zip":      "archive",

		"theme.mp3":   "media",
		"trailer.mkv": "media",

		"fullchain.pem": "key",
		"server.key":    "key",

		"database.sqlite": "database",
		"main.db":         "database",

		"data":     "file",
		"noext":    "file",
		"archive.": "file",
	}
	for name, want := range cases {
		if got := Kind(name); got != want {
			t.Errorf("Kind(%q) = %q, want %q", name, got, want)
		}
	}
}

// A suffix added to a file does not change what the file is: an operator's
// backup of a config is still a config.
func TestKindLooksPastATrailingSuffix(t *testing.T) {
	cases := map[string]string{
		"settings.yml.bak":  "config",
		"nginx.conf.orig":   "config",
		"app.log.1":         "text",
		".env.production":   "config",
		"database.sqlite.0": "database",
	}
	for name, want := range cases {
		if got := Kind(name); got != want {
			t.Errorf("Kind(%q) = %q, want %q", name, got, want)
		}
	}
}

// The icon list and the inline-serving allowlist are different decisions and
// must not be quietly wired to each other: an SVG gets a picture icon and is
// still never served inline.
func TestSVGHasAnImageIconButIsNotServedInline(t *testing.T) {
	if Kind("logo.svg") != "image" {
		t.Error("an SVG should look like an image in the listing")
	}
	if IsImage("logo.svg") || ImageType("logo.svg") != "" {
		t.Error("an SVG must not be servable inline")
	}
}

// Kind names a fixed set, because the template dispatches on it with a template
// per value and anything else falls through to no icon at all.
func TestKindOnlyReturnsKnownNames(t *testing.T) {
	known := map[string]bool{
		"config": true, "code": true, "text": true, "image": true,
		"archive": true, "media": true, "key": true, "database": true, "file": true,
	}
	for ext, kind := range kinds {
		if !known[kind] {
			t.Errorf("extension %q maps to unknown kind %q", ext, kind)
		}
	}
	for _, name := range []string{"x.conf", "x", "Dockerfile", ".env", "x.zz"} {
		if !known[Kind(name)] {
			t.Errorf("Kind(%q) = %q, which no icon covers", name, Kind(name))
		}
	}
}
