package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveCreatesAndReplaces(t *testing.T) {
	root, _ := tree(t)

	if _, err := root.Save("new.txt", strings.NewReader("hello"), MaxEdit); err != nil {
		t.Fatalf("Save(new) = %v", err)
	}
	if body := read(t, filepath.Join(root.Path(), "new.txt")); body != "hello" {
		t.Errorf("new file = %q", body)
	}

	if _, err := root.Save("config.yml", strings.NewReader("port: 9090\n"), MaxEdit); err != nil {
		t.Fatalf("Save(existing) = %v", err)
	}
	if body := read(t, filepath.Join(root.Path(), "config.yml")); body != "port: 9090\n" {
		t.Errorf("replaced file = %q", body)
	}

	// Into a subdirectory, which is the shape an upload takes when the reader
	// has walked somewhere first.
	if _, err := root.Save("sub/deep.txt", strings.NewReader("x"), MaxEdit); err != nil {
		t.Errorf("Save(sub/deep.txt) = %v", err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The cage, on the writing side. None of these may create a file outside the
// root, and none may name something that is not a name.
func TestSaveRefusesEscapesAndBadNames(t *testing.T) {
	root, outside := tree(t)

	for _, rel := range []string{
		"../escaped.txt",
		"/../escaped.txt",
		"sub/../../escaped.txt",
		"../../../../tmp/escaped.txt",
	} {
		// These clean back to a name inside the root rather than failing, which
		// is the point: what must never happen is a file appearing outside.
		root.Save(rel, strings.NewReader("x"), MaxEdit)
		if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); err == nil {
			t.Fatalf("Save(%q) wrote outside the root", rel)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(outside), "escaped.txt")); err == nil {
			t.Fatalf("Save(%q) wrote above the root", rel)
		}
	}

	for _, rel := range []string{"", "/", ".", ".."} {
		if _, err := root.Save(rel, strings.NewReader("x"), MaxEdit); !errors.Is(err, ErrBadName) {
			t.Errorf("Save(%q) = %v, want ErrBadName", rel, err)
		}
	}
	// "sub/" is a name that happens to be a directory, not a malformed one:
	// it is refused for what it is rather than for how it was spelled.
	if _, err := root.Save("sub/", strings.NewReader("x"), MaxEdit); !errors.Is(err, ErrIsDir) {
		t.Errorf(`Save("sub/") = %v, want ErrIsDir`, err)
	}

	// A directory that does not exist is not created on the way.
	if _, err := root.Save("nope/file.txt", strings.NewReader("x"), MaxEdit); err == nil {
		t.Error("Save created a file under a directory that does not exist")
	}
}

// A name is one component. Anything that arrived carrying a path is reduced to
// its last part, and the parts that could climb are refused outright.
func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"notes.txt":                    "notes.txt",
		"../../etc/cron.d/backdoor":    "backdoor",
		`..\..\windows\system32\a.dll`: "a.dll",
		"/absolute/path/photo.png":     "photo.png",
		"  spaced.txt  ":               "spaced.txt",
	}
	for in, want := range cases {
		got, ok := SafeName(in)
		if !ok || got != want {
			t.Errorf("SafeName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "   ", ".", "..", "../", "/"} {
		if got, ok := SafeName(in); ok {
			t.Errorf("SafeName(%q) = %q, want a refusal", in, got)
		}
	}
}

func TestSaveRefusesDirectoryAndSymlink(t *testing.T) {
	root, outside := tree(t)

	if _, err := root.Save("sub", strings.NewReader("x"), MaxEdit); !errors.Is(err, ErrIsDir) {
		t.Errorf("Save(dir) = %v, want ErrIsDir", err)
	}

	// A link an application container dropped in, pointing at something that
	// must not be written through.
	symlink(t, filepath.Join(outside, "master.key"), filepath.Join(root.Path(), "innocent.conf"))
	if _, err := root.Save("innocent.conf", strings.NewReader("owned"), MaxEdit); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Save(symlink) = %v, want ErrNotRegular", err)
	}
	if body := read(t, filepath.Join(outside, "master.key")); body != "very secret" {
		t.Fatalf("the write went through the link: target is now %q", body)
	}
}

func TestSaveEnforcesTheLimit(t *testing.T) {
	root, _ := tree(t)

	// Exactly the cap is allowed; one byte more is not.
	if _, err := root.Save("at-cap", strings.NewReader(strings.Repeat("a", 100)), 100); err != nil {
		t.Errorf("Save at the cap = %v", err)
	}
	if _, err := root.Save("over-cap", strings.NewReader(strings.Repeat("a", 101)), 100); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Save over the cap = %v, want ErrTooLarge", err)
	}
	// A refused write leaves nothing behind, temporary file included.
	if _, err := os.Stat(filepath.Join(root.Path(), "over-cap")); err == nil {
		t.Error("the refused write created the file anyway")
	}
	entries, _ := os.ReadDir(root.Path())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".quasar-tmp-") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// Replacing a file keeps the permissions it had: a secret chmodded 600 must not
// come back world-readable because someone edited it.
func TestSaveKeepsTheExistingMode(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("no meaningful file modes")
	}
	root, _ := tree(t)
	secret := filepath.Join(root.Path(), "secret.env")
	write(t, secret, "TOKEN=old\n")
	if err := os.Chmod(secret, 0o600); err != nil {
		t.Skipf("chmod unavailable: %v", err)
	}
	before, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode().Perm() != 0o600 {
		t.Skip("filesystem does not carry modes")
	}

	if _, err := root.Save("secret.env", strings.NewReader("TOKEN=new\n"), MaxEdit); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != 0o600 {
		t.Errorf("mode after the edit = %o, want 600", after.Mode().Perm())
	}
}

func TestRemove(t *testing.T) {
	root, outside := tree(t)

	if err := root.Remove("config.yml"); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root.Path(), "config.yml")); err == nil {
		t.Error("the file is still there")
	}

	if err := root.Remove("sub"); !errors.Is(err, ErrIsDir) {
		t.Errorf("Remove(dir) = %v, want ErrIsDir", err)
	}
	if err := root.Remove("gone.txt"); err == nil {
		t.Error("Remove accepted a file that does not exist")
	}
	for _, rel := range []string{"", "..", "/"} {
		if err := root.Remove(rel); !errors.Is(err, ErrBadName) {
			t.Errorf("Remove(%q) = %v, want ErrBadName", rel, err)
		}
	}

	// Deleting a link deletes the link, never what it points at.
	symlink(t, filepath.Join(outside, "master.key"), filepath.Join(root.Path(), "link"))
	if err := root.Remove("link"); err != nil {
		t.Fatalf("Remove(symlink) = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "master.key")); err != nil {
		t.Error("removing the link removed its target")
	}
}

// A root the filesystem will not take writes to refuses every one of them, and
// says so as itself rather than as an errno from somewhere in the middle.
func TestReadOnlyRootRefusesEveryWrite(t *testing.T) {
	root, _ := tree(t)
	ro := root.ReadOnly()

	if ro.Writable() {
		t.Fatal("ReadOnly() left the root writable")
	}
	if _, err := ro.Save("x.txt", strings.NewReader("x"), MaxEdit); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Save = %v, want ErrReadOnly", err)
	}
	if err := ro.Remove("config.yml"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Remove = %v, want ErrReadOnly", err)
	}
	if _, err := os.Stat(filepath.Join(root.Path(), "config.yml")); err != nil {
		t.Error("the refused Remove deleted the file anyway")
	}
	// Reading still works: read-only is a restriction, not a closed door.
	if _, err := ro.List(""); err != nil {
		t.Errorf("List on a read-only root = %v", err)
	}
}

// A temporary directory is writable, and NewRoot should have found that out on
// its own rather than needing to be told.
func TestNewRootDetectsWritability(t *testing.T) {
	root, _ := tree(t)
	if !root.Writable() {
		t.Error("a temporary directory came back read-only")
	}
}

// The editor is filled from a preview, and a preview of a large file is its
// first 256 KB. Saving that back would drop the rest, so such a file must never
// be offered as editable.
func TestEditableRefusesWhatWouldBeTruncated(t *testing.T) {
	root, _ := tree(t)
	big := strings.Repeat("log line\n", PreviewMax/9+500)
	write(t, filepath.Join(root.Path(), "big.log"), big)

	whole, err := root.Read("config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !root.Editable(whole) {
		t.Error("a small text file should be editable")
	}

	cut, err := root.Read("big.log")
	if err != nil {
		t.Fatal(err)
	}
	if !cut.Truncated {
		t.Fatal("the large file was not truncated, so this proves nothing")
	}
	if root.Editable(cut) {
		t.Error("a truncated preview must not be editable — saving it would drop the rest of the file")
	}

	// Nor is anything that is not text.
	for _, p := range []Preview{{Kind: "binary"}, {Kind: "image"}, {Kind: "empty"}} {
		if root.Editable(p) {
			t.Errorf("%s should not be editable", p.Kind)
		}
	}
	// Nor anything at all on read-only storage.
	if root.ReadOnly().Editable(whole) {
		t.Error("read-only storage offered an edit")
	}
}
