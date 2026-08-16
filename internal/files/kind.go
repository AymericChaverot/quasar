package files

import (
	"path"
	"strings"
)

// Kind names the sort of thing a filename suggests, so a listing can show an
// icon that distinguishes one row from the next.
//
// It is a guess from the name, and deliberately nothing more: opening every
// file in a directory to find out what it really is would turn drawing a
// listing into reading the whole tree. A wrong guess costs a wrong icon, which
// is why the preview decides what a file actually is by reading it (see Read)
// and never consults this.
//
// The kinds are the ones an operator is scanning a data directory for: what
// configures the app, what its data lives in, what it logs to, and what is
// simply a payload.
func Kind(name string) string {
	base := strings.ToLower(path.Base(name))

	// Names that carry their meaning without an extension. A dotfile is
	// configuration far more often than not, which is the useful default for
	// anything here that is not otherwise recognised.
	switch base {
	case "dockerfile", "makefile", "caddyfile", "procfile", "vagrantfile", "justfile":
		return "config"
	case "readme", "license", "licence", "changelog", "authors", "notice":
		return "text"
	}

	ext := strings.TrimPrefix(path.Ext(base), ".")
	if ext == "" {
		if strings.HasPrefix(base, ".") {
			return "config"
		}
		return "file"
	}
	if k, ok := kinds[ext]; ok {
		return k
	}
	// ".env.production", "settings.yml.bak" and friends: the extension that
	// says what the file is sits one back from the one that says what was done
	// to it.
	if inner := path.Ext(strings.TrimSuffix(base, "."+ext)); inner != "" {
		if k, ok := kinds[strings.TrimPrefix(inner, ".")]; ok {
			return k
		}
	}
	if strings.HasPrefix(base, ".") {
		return "config"
	}
	return "file"
}

// kinds maps an extension to the icon its file gets. Grouped by what the reader
// is looking for rather than by format family — .json and .yml sit under
// configuration because that is what they are in these trees, whatever else
// they are elsewhere.
var kinds = map[string]string{
	// Configuration: the files an operator came here to read.
	"conf": "config", "cfg": "config", "ini": "config", "toml": "config",
	"yml": "config", "yaml": "config", "env": "config", "properties": "config",
	"json": "config", "plist": "config", "rc": "config", "options": "config",

	// Code and markup.
	"go": "code", "js": "code", "mjs": "code", "cjs": "code", "ts": "code",
	"tsx": "code", "jsx": "code", "py": "code", "rb": "code", "php": "code",
	"sh": "code", "bash": "code", "zsh": "code", "lua": "code", "pl": "code",
	"c": "code", "h": "code", "cpp": "code", "hpp": "code", "rs": "code",
	"java": "code", "kt": "code", "swift": "code", "sql": "code",
	"html": "code", "htm": "code", "css": "code", "scss": "code", "xml": "code",

	// Prose, tables and logs.
	"md": "text", "markdown": "text", "txt": "text", "rst": "text",
	"log": "text", "csv": "text", "tsv": "text", "pdf": "text",

	// Images. Not the same list as the one the preview will render inline —
	// that one is a security decision and closed on purpose; this is only an
	// icon, so an SVG belongs here.
	"png": "image", "jpg": "image", "jpeg": "image", "gif": "image",
	"webp": "image", "svg": "image", "bmp": "image", "ico": "image",
	"avif": "image", "tif": "image", "tiff": "image",

	"zip": "archive", "tar": "archive", "gz": "archive", "tgz": "archive",
	"bz2": "archive", "xz": "archive", "zst": "archive", "7z": "archive",
	"rar": "archive",

	"mp3": "media", "m4a": "media", "wav": "media", "flac": "media",
	"ogg": "media", "opus": "media", "mp4": "media", "mkv": "media",
	"mov": "media", "avi": "media", "webm": "media",

	// Keys and certificates, worth spotting at a glance in someone else's data
	// directory.
	"pem": "key", "key": "key", "crt": "key", "cert": "key", "cer": "key",
	"pub": "key", "p12": "key", "pfx": "key", "asc": "key", "gpg": "key",

	"db": "database", "sqlite": "database", "sqlite3": "database",
	"mdb": "database", "frm": "database", "ibd": "database", "myd": "database",
}
