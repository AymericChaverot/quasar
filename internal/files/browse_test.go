package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree builds a root with a couple of files in it, plus a sibling directory
// outside the root for the escape tests to aim at.
func tree(t *testing.T) (Root, string) {
	t.Helper()
	base := t.TempDir()
	inside := filepath.Join(base, "data")
	outside := filepath.Join(base, "secret")
	for _, d := range []string{inside, outside, filepath.Join(inside, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(inside, "config.yml"), "port: 8080\n")
	write(t, filepath.Join(inside, "sub", "note.txt"), "hello")
	write(t, filepath.Join(outside, "master.key"), "very secret")

	root, err := NewRoot(inside)
	if err != nil {
		t.Fatal(err)
	}
	return root, outside
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// symlink creates a link, skipping the test when the platform will not allow it
// unprivileged — which is the normal case on Windows, where these tests are
// also run.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestResolveStaysInsideRoot(t *testing.T) {
	root, _ := tree(t)

	for _, rel := range []string{"", "/", ".", "sub", "/sub/", "sub/note.txt", "./sub/../sub"} {
		if _, err := root.Resolve(rel); err != nil {
			t.Errorf("Resolve(%q) = %v, want it to resolve", rel, err)
		}
	}
}

// The dot-dot half of the cage. None of these may reach the sibling directory,
// whatever shape the climb takes.
//
// They fail rather than being refused, and the difference is worth being clear
// about: cleaning under a leading slash leaves ".." nothing to climb, so what
// is looked up is /secret/master.key *under the root*, which does not exist.
// The escape is gone before the filesystem is touched.
func TestResolveRefusesTraversal(t *testing.T) {
	root, outside := tree(t)

	for _, rel := range []string{
		"../secret",
		"../secret/master.key",
		"/../secret/master.key",
		"sub/../../secret/master.key",
		"../../../../../../etc/passwd",
	} {
		got, err := root.Resolve(rel)
		if err == nil {
			t.Errorf("Resolve(%q) = %q, want a refusal", rel, got)
		}
		if err == nil && strings.HasPrefix(got, outside) {
			t.Fatalf("Resolve(%q) reached outside the root", rel)
		}
	}
}

// The same climb, where the name it lands on does exist inside the root. It
// must resolve to the one inside and never to the sibling that shares its name.
func TestResolveTraversalLandsInsideTheRoot(t *testing.T) {
	root, outside := tree(t)
	if err := os.Mkdir(filepath.Join(root.Path(), "secret"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root.Path(), "secret", "master.key"), "the app's own")

	got, err := root.Resolve("../secret/master.key")
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if !within(root.Path(), got) {
		t.Fatalf("Resolve = %q, which is outside %q", got, root.Path())
	}
	if strings.HasPrefix(got, outside) {
		t.Fatalf("Resolve reached the sibling at %q", got)
	}
	body, err := os.ReadFile(got)
	if err != nil || string(body) != "the app's own" {
		t.Errorf("read %q = %q, %v", got, body, err)
	}
}

// The symlink half. Cleaning the string cannot see this one: the path never
// mentions the target, and only the kernel knows where the link goes.
func TestResolveRefusesSymlinkEscape(t *testing.T) {
	root, outside := tree(t)
	symlink(t, outside, filepath.Join(root.Path(), "escape"))

	if _, err := root.Resolve("escape"); !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("Resolve(escape) error = %v, want ErrOutsideRoot", err)
	}
	if _, err := root.Resolve("escape/master.key"); err == nil {
		t.Error("Resolve reached a file through a link out of the root")
	}
}

// A link that stays inside is followed: application data is full of them, and
// refusing every link would hide real directories.
func TestResolveFollowsSymlinkInsideRoot(t *testing.T) {
	root, _ := tree(t)
	symlink(t, filepath.Join(root.Path(), "sub"), filepath.Join(root.Path(), "alias"))

	got, err := root.Resolve("alias/note.txt")
	if err != nil {
		t.Fatalf("Resolve(alias/note.txt) = %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join("sub", "note.txt")) {
		t.Errorf("Resolve followed the link to %q", got)
	}
}

// A root that is itself reached through a link still browses: /var/lib/docker
// is a symlink on more than one distribution, and the prefix check compares
// resolved paths on both sides for exactly this reason.
func TestNewRootThroughSymlink(t *testing.T) {
	root, _ := tree(t)
	base := filepath.Dir(root.Path())
	link := filepath.Join(base, "link-to-data")
	symlink(t, root.Path(), link)

	linked, err := NewRoot(link)
	if err != nil {
		t.Fatalf("NewRoot(link) = %v", err)
	}
	if _, err := linked.Resolve("sub/note.txt"); err != nil {
		t.Errorf("Resolve through a linked root = %v", err)
	}
	if _, err := linked.Resolve("../secret/master.key"); err == nil {
		t.Error("a linked root let a traversal out")
	}
}

// "/data" must not be judged to contain "/database".
func TestRootIsNotAPrefixOfItsSibling(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "database"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(filepath.Join(base, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if within(root.Path(), filepath.Join(base, "database")) {
		t.Error("a sibling whose name extends the root was judged inside it")
	}
}

func TestNewRootRejectsFileAndMissing(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "file"), "x")

	if _, err := NewRoot(filepath.Join(base, "file")); !errors.Is(err, ErrNotDir) {
		t.Errorf("NewRoot(file) = %v, want ErrNotDir", err)
	}
	if _, err := NewRoot(filepath.Join(base, "gone")); err == nil {
		t.Error("NewRoot accepted a path that does not exist")
	}
	if _, err := NewRoot(""); !errors.Is(err, ErrNotDir) {
		t.Error("NewRoot accepted an empty path")
	}
	// The zero value is what a failed lookup leaves behind; it must not browse.
	var zero Root
	if _, err := zero.Resolve("anything"); err == nil {
		t.Error("the zero Root resolved a path")
	}
}

func TestListOrdersDirectoriesFirst(t *testing.T) {
	root, _ := tree(t)
	write(t, filepath.Join(root.Path(), "Apple.txt"), "a")
	if err := os.Mkdir(filepath.Join(root.Path(), "zebra"), 0o755); err != nil {
		t.Fatal(err)
	}

	entries, err := root.List("")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := []string{"sub", "zebra", "Apple.txt", "config.yml"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("List order = %v, want %v", names, want)
	}
}

func TestListMarksBrokenAndEscapingLinks(t *testing.T) {
	root, outside := tree(t)
	symlink(t, outside, filepath.Join(root.Path(), "escape"))
	symlink(t, filepath.Join(root.Path(), "nothing-here"), filepath.Join(root.Path(), "dangling"))

	entries, err := root.List("")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]Entry{}
	for _, e := range entries {
		found[e.Name] = e
	}
	for _, name := range []string{"escape", "dangling"} {
		e, ok := found[name]
		if !ok {
			t.Fatalf("%s missing from the listing", name)
		}
		if !e.Link || !e.Broken {
			t.Errorf("%s: Link=%v Broken=%v, want both true", name, e.Link, e.Broken)
		}
	}
}

func TestListRefusesAFile(t *testing.T) {
	root, _ := tree(t)
	if _, err := root.List("config.yml"); !errors.Is(err, ErrNotDir) {
		t.Errorf("List(file) = %v, want ErrNotDir", err)
	}
}

func TestReadClassifiesContent(t *testing.T) {
	root, _ := tree(t)
	write(t, filepath.Join(root.Path(), "empty.log"), "")
	write(t, filepath.Join(root.Path(), "binary.db"), "SQLite\x00format 3\x00\x00\x00")
	write(t, filepath.Join(root.Path(), "accents.log"), "caf\xe9 na\xefve — latin-1 logs are still logs\n")
	if err := os.WriteFile(filepath.Join(root.Path(), "icon.png"), []byte("\x89PNG\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"config.yml":  "text",
		"empty.log":   "empty",
		"binary.db":   "binary",
		"accents.log": "text",
		"icon.png":    "image",
	}
	for name, want := range cases {
		p, err := root.Read(name)
		if err != nil {
			t.Fatalf("Read(%s) = %v", name, err)
		}
		if p.Kind != want {
			t.Errorf("Read(%s).Kind = %q, want %q", name, p.Kind, want)
		}
	}
}

func TestReadTruncatesLargeText(t *testing.T) {
	root, _ := tree(t)
	big := strings.Repeat("log line\n", PreviewMax/9+500)
	write(t, filepath.Join(root.Path(), "big.log"), big)

	p, err := root.Read("big.log")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "text" || !p.Truncated {
		t.Fatalf("Kind=%q Truncated=%v, want a truncated text preview", p.Kind, p.Truncated)
	}
	if len(p.Text) != PreviewMax {
		t.Errorf("preview is %d bytes, want the %d-byte cap", len(p.Text), PreviewMax)
	}
	if p.Size != int64(len(big)) {
		t.Errorf("Size = %d, want the whole file's %d", p.Size, len(big))
	}
}

// A binary is never handed back as text, even when the caller asks for it: the
// download route sets the headers that make bytes safe to serve.
func TestReadRefusesToTraverse(t *testing.T) {
	root, _ := tree(t)
	if _, err := root.Read("../secret/master.key"); err == nil {
		t.Error("Read reached outside the root")
	}
	if _, _, err := root.Open("../secret/master.key"); err == nil {
		t.Error("Open reached outside the root")
	}
	if _, err := root.Stat("../secret/master.key"); err == nil {
		t.Error("Stat reached outside the root")
	}
}

func TestOpenRefusesADirectory(t *testing.T) {
	root, _ := tree(t)
	if _, _, err := root.Open("sub"); !errors.Is(err, ErrNotDir) {
		t.Errorf("Open(dir) = %v, want ErrNotDir", err)
	}
}

func TestImageInlineAllowlist(t *testing.T) {
	for _, name := range []string{"a.png", "A.PNG", "photo.jpeg", "anim.gif"} {
		if !IsImage(name) || ImageType(name) == "" {
			t.Errorf("%s should be inline-able", name)
		}
	}
	// SVG is an image everywhere else and deliberately not here: served from
	// this origin it is a document that can carry script.
	for _, name := range []string{"logo.svg", "dump.sql", "data.tar.gz", "noext"} {
		if IsImage(name) || ImageType(name) != "" {
			t.Errorf("%s should not be served inline", name)
		}
	}
}

func TestCrumbsAndParent(t *testing.T) {
	crumbs := Crumbs("/var/log/../log/nginx/")
	if len(crumbs) != 3 {
		t.Fatalf("Crumbs = %+v, want three steps", crumbs)
	}
	if crumbs[2].Name != "nginx" || crumbs[2].Path != "var/log/nginx" {
		t.Errorf("last crumb = %+v", crumbs[2])
	}
	if crumbs[0].Path != "var" {
		t.Errorf("first crumb = %+v", crumbs[0])
	}
	if got := Crumbs(""); got != nil {
		t.Errorf("Crumbs(root) = %v, want none", got)
	}

	cases := map[string]string{
		"":                    "",
		"sub":                 "",
		"sub/deep":            "sub",
		"/sub/deep/deeper":    "sub/deep",
		"sub/../other/thing":  "other",
		"////sub////deep////": "sub",
	}
	for in, want := range cases {
		if got := Parent(in); got != want {
			t.Errorf("Parent(%q) = %q, want %q", in, got, want)
		}
	}
}
