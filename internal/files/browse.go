// Package files browses a directory tree on behalf of the dashboard, confined
// to a root it is not allowed to leave, and edits it where it may.
//
// The trees it opens are application data: databases, uploads, and the
// configuration whatever image the operator deployed wrote for itself. The
// dashboard reaches them two different ways, and which one applies decides
// whether they can be changed. An app's data directory is bind-mounted into
// this process read-write, so it is editable. A named volume is normally only
// visible through the host filesystem mounted at /host/root read-only, so it is
// not — until an operator mounts Docker's volume tree in as well.
//
// Which of those a given root is, this package works out for itself rather than
// being told: see canWrite. A guess would put an edit button on a file whose
// filesystem will refuse the write, which is worse than no button.
//
// The cage is the point of the package. The dashboard container can see the
// whole host filesystem, so a browser that took a path from a URL and joined it
// to a root would hand out /etc/shadow and the platform's own master key to
// anyone who could type "../". Resolve is the only way in, and every other
// function goes through it — the writes included, which resolve the parent
// directory and refuse a name that is anything but a name.
package files

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrOutsideRoot is returned for a path that resolves out of the root, whether
// it climbed out with ".." or was walked out through a symlink.
var ErrOutsideRoot = errors.New("path leaves the browsable root")

// ErrNotDir is returned when a root, or a path being listed, is not a
// directory.
var ErrNotDir = errors.New("not a directory")

// Root is a directory the browser may read, and nothing above it.
//
// The zero value is unusable: NewRoot resolves the symlinks in the root itself
// once, so that the prefix check in Resolve compares two paths that have both
// been through the kernel and can be compared as strings at all.
type Root struct {
	path     string
	writable bool
}

// NewRoot prepares dir for browsing. It fails if dir does not exist or is not a
// directory — a volume whose mountpoint has been removed under the daemon, or a
// bind mount to a path that never existed on this host.
func NewRoot(dir string) (Root, error) {
	if dir == "" {
		return Root{}, ErrNotDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Root{}, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Root{}, err
	}
	info, err := os.Stat(real)
	if err != nil {
		return Root{}, err
	}
	if !info.IsDir() {
		return Root{}, ErrNotDir
	}
	return Root{path: real, writable: canWrite(real)}, nil
}

// Path is the resolved directory this root browses, for display.
func (r Root) Path() string { return r.path }

// Valid reports whether the root was built by NewRoot.
func (r Root) Valid() bool { return r.path != "" }

// Writable reports whether this process can change what is in the root, as the
// kernel answers it rather than as the caller hopes. Everything that writes
// checks it, and the interface asks it before offering an edit.
func (r Root) Writable() bool { return r.writable }

// ReadOnly returns a copy of the root that refuses every write, whatever the
// filesystem would have allowed. It is how a caller declines a capability it
// has — nothing takes it away again.
func (r Root) ReadOnly() Root {
	r.writable = false
	return r
}

// Resolve turns a slash-separated path from a URL into an absolute path inside
// the root, or fails.
//
// Two separate things are being defended against, and only doing both is safe.
// Cleaning under a leading slash collapses "../../etc" to "/etc" before the
// join, so no amount of dot-dot in the URL can climb above the root. That alone
// would still be defeated by a symlink: the entries in these trees are written
// by application containers, which are free to drop in a link pointing at /, and
// the kernel follows it whatever the string looked like. So the answer is
// resolved through its links and only then checked against the root.
func (r Root) Resolve(rel string) (string, error) {
	if !r.Valid() {
		return "", ErrNotDir
	}
	clean := path.Clean("/" + strings.TrimPrefix(rel, "/"))
	full := filepath.Join(r.path, filepath.FromSlash(clean))
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	if !within(r.path, real) {
		return "", ErrOutsideRoot
	}
	return real, nil
}

// within reports whether p is root or sits underneath it. The separator matters:
// without it "/data" would be judged to contain "/database".
func within(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// Clean normalises a path from a URL for display and for links, without
// touching the filesystem. It is what the breadcrumb is built from, so a
// listing and the trail above it cannot disagree about where the reader is.
func Clean(rel string) string {
	return strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(rel, "/")), "/")
}

// Entry is one line of a directory listing.
type Entry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
	// Link marks a symlink. It is listed rather than hidden, because a data
	// directory full of links the browser silently dropped would look empty for
	// no visible reason.
	Link bool
	// Broken marks a link that goes nowhere this browser will follow: a missing
	// target, or one outside the root. Both are dead ends here, and the reason
	// they are dead is worth showing rather than failing on the click.
	Broken bool
}

// List reads one directory. Entries are ordered directories first and then by
// name, folded for case, which is the order a reader scans for a name in.
func (r Root) List(rel string) ([]Entry, error) {
	dir, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrNotDir
	}
	raw, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(raw))
	for _, d := range raw {
		out = append(out, r.entry(rel, d))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// entry describes one directory entry, following a symlink far enough to know
// whether clicking it would land in a directory or on a file.
func (r Root) entry(rel string, d fs.DirEntry) Entry {
	e := Entry{Name: d.Name(), IsDir: d.IsDir()}
	if info, err := d.Info(); err == nil {
		e.Size = info.Size()
		e.ModTime = info.ModTime()
	}
	if d.Type()&fs.ModeSymlink == 0 {
		return e
	}
	e.Link = true
	// Resolve rather than os.Stat: a link pointing out of the root is followable
	// by the kernel but not by this browser, and it is the same dead end as a
	// link pointing at nothing.
	target, err := r.Resolve(path.Join(rel, d.Name()))
	if err != nil {
		e.Broken = true
		return e
	}
	info, err := os.Stat(target)
	if err != nil {
		e.Broken = true
		return e
	}
	e.IsDir = info.IsDir()
	e.Size = info.Size()
	e.ModTime = info.ModTime()
	return e
}

// Stat describes a single path inside the root.
func (r Root) Stat(rel string) (fs.FileInfo, error) {
	full, err := r.Resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.Stat(full)
}

// Open opens a file inside the root for reading. A directory is refused: the
// caller wants bytes, and os.Open would hand back a handle that only fails
// later.
func (r Root) Open(rel string) (*os.File, fs.FileInfo, error) {
	full, err := r.Resolve(rel)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if info.IsDir() {
		f.Close()
		return nil, nil, ErrNotDir
	}
	return f, info, nil
}

// PreviewMax is how much of a file the preview will read. Large enough for a
// configuration file or a stack trace to arrive whole, small enough that
// clicking a 40 GB database file costs a page rather than the server.
const PreviewMax = 256 << 10

// sniffLen is how much of a file is examined to decide whether it is text.
const sniffLen = 8 << 10

// Preview is what the reader is shown for one file.
type Preview struct {
	// Kind is "text", "image", "binary" or "empty". The template renders one of
	// four things from it and never has to guess from the extension itself.
	Kind      string
	Text      string
	Truncated bool
	Size      int64
	ModTime   time.Time
}

// Read returns a preview of one file: its text if it is text, its kind
// otherwise. It never returns bytes for a binary — those go through the
// download route, which sets the headers that make them safe to hand out.
func (r Root) Read(rel string) (Preview, error) {
	f, info, err := r.Open(rel)
	if err != nil {
		return Preview{}, err
	}
	defer f.Close()

	p := Preview{Size: info.Size(), ModTime: info.ModTime()}
	if info.Size() == 0 {
		p.Kind = "empty"
		return p, nil
	}
	if IsImage(rel) {
		p.Kind = "image"
		return p, nil
	}
	buf := make([]byte, PreviewMax)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return Preview{}, err
	}
	buf = buf[:n]
	if !isText(buf) {
		p.Kind = "binary"
		return p, nil
	}
	p.Kind = "text"
	p.Text = string(buf)
	p.Truncated = info.Size() > int64(n)
	return p, nil
}

// isText decides whether a chunk of a file can be shown as text.
//
// A NUL byte settles it: no text format this browser will meet contains one,
// and every binary format meets one early. Past that the test is deliberately
// loose about encoding — a log file in latin-1 is still a log file, and
// refusing to show it because a single accented byte is not valid UTF-8 would
// hide exactly the files an operator opens this for. Only a chunk that is
// mostly unreadable is called binary.
func isText(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	head := b
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	bad := 0
	for i := 0; i < len(head); i++ {
		c := head[i]
		if c == 0 {
			return false
		}
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			bad++
		}
	}
	// Counted separately from the control bytes: a UTF-8 file cut mid-rune by
	// the sniff window would otherwise be judged on its own truncation.
	if !utf8.Valid(head) {
		bad += invalidRunes(head)
	}
	return bad*20 <= len(head)
}

// invalidRunes counts the bytes in b that decode to nothing.
func invalidRunes(b []byte) int {
	n := 0
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			n++
		}
		b = b[size:]
	}
	return n
}

// inlineImages are the types a browser renders from an <img> with no way for
// the file to run code in the dashboard's origin.
//
// SVG is deliberately absent. It is an image everywhere else in this interface,
// but an SVG served from the dashboard's own host and opened in a tab is a
// document that can carry script, and these files are written by application
// containers. It downloads instead.
var inlineImages = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".avif": "image/avif",
}

// IsImage reports whether a name is one of the image types safe to show inline.
func IsImage(name string) bool {
	_, ok := inlineImages[strings.ToLower(path.Ext(name))]
	return ok
}

// ImageType is the content type to serve an inline image as, or "" for anything
// that must not be served inline.
func ImageType(name string) string {
	return inlineImages[strings.ToLower(path.Ext(name))]
}

// Crumb is one step of the trail back to the root.
type Crumb struct {
	Name string
	Path string // the rel path this step points at, "" for the root itself
}

// Crumbs breaks a path into the trail shown above a listing. The root itself is
// not included: the caller labels it with the volume or mount it is showing,
// which is a better name for it than "/".
func Crumbs(rel string) []Crumb {
	clean := Clean(rel)
	if clean == "" {
		return nil
	}
	parts := strings.Split(clean, "/")
	out := make([]Crumb, 0, len(parts))
	for i, p := range parts {
		out = append(out, Crumb{Name: p, Path: strings.Join(parts[:i+1], "/")})
	}
	return out
}

// Parent is the directory containing rel, "" when rel is at the root.
func Parent(rel string) string {
	clean := Clean(rel)
	if clean == "" {
		return ""
	}
	i := strings.LastIndex(clean, "/")
	if i < 0 {
		return ""
	}
	return clean[:i]
}
