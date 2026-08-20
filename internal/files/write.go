package files

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MaxUpload bounds one uploaded file. The real limit is the server's disk, which
// nothing here can give back once it is full — an app that cannot write its
// database because someone uploaded a film is a worse outcome than a refused
// upload.
const MaxUpload = 256 << 20

// MaxEdit bounds what the editor may write back. It is PreviewMax on purpose:
// the editor is filled from a preview, and a preview of a larger file is the
// first 256 KB of it. Saving that back would write those 256 KB over the whole
// file and silently drop the rest, so a file too large to preview whole is a
// file too large to edit at all.
const MaxEdit = PreviewMax

var (
	// ErrReadOnly is returned when the filesystem behind a root will not take
	// writes — the normal case for a named volume seen through /host/root.
	ErrReadOnly = errors.New("this storage is read-only")

	// ErrBadName is returned for a name that is not a name: empty, a dot, or
	// something carrying a path separator.
	ErrBadName = errors.New("that name cannot be used")

	// ErrNotRegular is returned when a write would land on something that is
	// not a plain file — a directory, a device, or a symlink.
	ErrNotRegular = errors.New("that is not a regular file")

	// ErrIsDir is returned when an operation meant for a file is given a
	// directory.
	ErrIsDir = errors.New("that is a directory")

	// ErrTooLarge is returned when the content exceeds the caller's limit.
	ErrTooLarge = errors.New("that file is too large")
)

// SafeName reduces the filename an upload arrived with to something that names
// one entry in one directory, or reports that it does not.
//
// Browsers send the basename, but the field is client-controlled and a
// multipart part is free to claim it is called "../../etc/cron.d/backdoor".
// Only the last component survives, and it still has to be a plausible name
// afterwards.
func SafeName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if !validName(name) {
		return "", false
	}
	return name, true
}

func validName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return false
	}
	return true
}

// resolveNew resolves a path whose last component need not exist yet.
//
// Resolve cannot do this: it runs the whole path through EvalSymlinks, which
// fails on anything that is not there. So the parent is resolved — with the
// full cage check, since that is where an escape would be attempted — and the
// name is joined onto the answer without ever being followed. That the name
// cannot itself contain a separator is what keeps the join from reintroducing a
// path.
func (r Root) resolveNew(rel string) (string, error) {
	if !r.Valid() {
		return "", ErrNotDir
	}
	clean := Clean(rel)
	if clean == "" {
		return "", ErrBadName
	}
	dir, base := path.Split(clean)
	if !validName(base) {
		return "", ErrBadName
	}
	parent, err := r.Resolve(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(parent)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", ErrNotDir
	}
	return filepath.Join(parent, base), nil
}

// Save writes src to rel, creating the file or replacing what is there, and
// returns how many bytes landed.
//
// The write goes to a temporary file in the same directory and is renamed into
// place. Three things follow from that, all of them wanted. A config file is
// never observed half-written by the application reading it, which for a
// process that reloads on change is the difference between a new setting and a
// crash. A write that fails partway leaves the old file untouched. And the
// rename replaces a symlink rather than writing through it — these trees are
// written by application containers, and one of them pointing a name at
// /etc/shadow must not turn an upload into a write there. The lstat above
// refuses that case outright; the rename is what makes the refusal safe to have
// raced.
func (r Root) Save(rel string, src io.Reader, max int64) (int64, error) {
	if !r.writable {
		return 0, ErrReadOnly
	}
	target, err := r.resolveNew(rel)
	if err != nil {
		return 0, err
	}

	// What is already there decides whether this is allowed, and what
	// permissions the replacement should keep. A file the operator chmodded
	// 600 must not come back 644 because it was edited.
	mode := fs.FileMode(0o644)
	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			return 0, ErrIsDir
		}
		if !info.Mode().IsRegular() {
			return 0, ErrNotRegular
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}

	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".quasar-tmp-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	// Removed on every path out but the successful rename, which has already
	// taken the file away by then and makes this a no-op.
	defer os.Remove(tmpName)

	// One byte past the limit is read on purpose: it is the only way to tell a
	// file that exactly fills the cap from one that overruns it.
	n, err := io.Copy(tmp, io.LimitReader(src, max+1))
	if err != nil {
		_ = tmp.Close() // the deferred remove is what matters here
		return 0, err
	}
	if n > max {
		_ = tmp.Close() // the deferred remove is what matters here
		return 0, ErrTooLarge
	}
	if err := tmp.Chmod(mode); err != nil && !errors.Is(err, os.ErrInvalid) {
		// Windows has no chmod worth the name; a development machine failing
		// here should not fail the write.
		_ = err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpName, target); err != nil {
		return 0, err
	}
	return n, nil
}

// Remove deletes one file.
//
// Directories are refused rather than removed. os.Remove would take an empty
// one happily, and a control that deletes a folder when it happens to be empty
// and refuses when it is not is a control nobody can predict — recursive
// removal is a different decision, and not one this offers.
//
// The name is resolved without following it, so deleting a symlink deletes the
// link and never what it points at.
func (r Root) Remove(rel string) error {
	if !r.writable {
		return ErrReadOnly
	}
	target, err := r.resolveNew(rel)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return ErrIsDir
	}
	return os.Remove(target)
}

// Mkdir creates one directory, and only one: the parent has to exist already.
//
// It goes through resolveNew for the same reason every other write does — the
// parent is resolved through its symlinks and checked against the root, and the
// name that is joined onto the answer cannot itself be a path. A directory that
// is already there is not an error, because the caller wanted it to exist and
// it does.
func (r Root) Mkdir(rel string) error {
	if !r.writable {
		return ErrReadOnly
	}
	target, err := r.resolveNew(rel)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.IsDir() {
			return nil
		}
		return ErrNotDir
	}
	return os.Mkdir(target, 0o755)
}

// Editable reports whether a file can be opened in the editor: writable
// storage, text, and small enough that what is shown is the whole of it.
func (r Root) Editable(p Preview) bool {
	return r.writable && p.Kind == "text" && !p.Truncated
}
