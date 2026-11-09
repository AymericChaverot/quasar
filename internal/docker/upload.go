package docker

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// An uploaded folder arrives as a gzipped tar from qsr, which has already left
// out what .gitignore, .dockerignore and .git would have excluded. Whatever is
// in it is untrusted: anyone holding an admin token can send one, and an
// archive is the classic way to write outside the directory it is meant for.
// So every entry is checked before anything is written, and the tree is built
// beside the current source and only swapped in once it is complete.

// UploadTooLargeError is an upload that went past the configured limit.
type UploadTooLargeError struct{ Limit int64 }

func (e *UploadTooLargeError) Error() string {
	return fmt.Sprintf("the upload is larger than the %s this server accepts", humanBytes(e.Limit))
}

// expandFactor bounds what an upload may grow to once decompressed, relative
// to the limit on what is sent: source code compresses well, but not a
// thousand to one, which is what a decompression bomb does.
const expandFactor = 10

// UploadStats describes an upload that was unpacked.
type UploadStats struct {
	Files int
	Bytes int64 // decompressed
}

// ReplaceSource unpacks a gzipped tar into the app's source directory,
// replacing what was there. body must already be limited to limit bytes (the
// handler does that, so it can answer 413 itself); the unpacked size is held to
// a multiple of it here.
//
// On any error the previous source is left exactly as it was.
func (c *Client) ReplaceSource(appID string, body io.Reader, limit int64) (UploadStats, error) {
	src := c.sourceDir(appID)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		return UploadStats{}, err
	}
	next := src + ".next"
	_ = os.RemoveAll(next) // a leftover from an upload that died halfway
	stats, err := unpackSource(next, body, limit*expandFactor)
	if err != nil {
		_ = os.RemoveAll(next)
		var tooBig *UploadTooLargeError
		if errors.As(err, &tooBig) {
			tooBig.Limit = limit
		}
		return stats, err
	}
	if err := os.RemoveAll(src); err != nil {
		_ = os.RemoveAll(next)
		return stats, fmt.Errorf("removing the previous source: %w", err)
	}
	if err := os.Rename(next, src); err != nil {
		return stats, fmt.Errorf("moving the new source into place: %w", err)
	}
	return stats, nil
}

// unpackSource writes the archive into dir, which must not exist yet, refusing
// any entry that would land outside it and stopping once more than maxBytes
// have been written.
func unpackSource(dir string, body io.Reader, maxBytes int64) (UploadStats, error) {
	var stats UploadStats
	gz, err := gzip.NewReader(body)
	if err != nil {
		return stats, fmt.Errorf("the upload is not a gzipped tar: %w", err)
	}
	defer gz.Close()
	if err := os.Mkdir(dir, 0o755); err != nil {
		return stats, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return stats, fmt.Errorf("reading the upload: %w", err)
		}
		rel, err := entryPath(h.Name)
		if err != nil {
			return stats, err
		}
		if rel == "" {
			continue // the archive's own root
		}
		if err := noLinksAbove(dir, rel); err != nil {
			return stats, err
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return stats, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return stats, err
			}
			n, err := writeEntry(target, tr, fileMode(h.Mode), maxBytes-stats.Bytes)
			stats.Bytes += n
			if err != nil {
				return stats, err
			}
			stats.Files++
		case tar.TypeSymlink:
			if err := linkStaysInside(rel, h.Linkname); err != nil {
				return stats, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return stats, err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return stats, err
			}
		default:
			// Hard links, devices and fifos have no business in a build
			// context, and hard links are a way out of the directory.
			return stats, fmt.Errorf("%s: only files, folders and symbolic links can be uploaded", rel)
		}
	}
	return stats, nil
}

// entryPath is an archive entry's path, cleaned and relative to the archive's
// root, or an error when it is absolute or climbs out of it.
func entryPath(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
		return "", fmt.Errorf("%s: absolute paths are not allowed in an upload", name)
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%s: the path leaves the uploaded folder", name)
	}
	return clean, nil
}

// noLinksAbove refuses an entry whose parent directories include a symbolic
// link. A link that points inside the folder is allowed, but writing through
// one would make every later check reason about the wrong directory: a link
// "a" to "b/c" and then an entry "a/x" pointing at "../../.." lands in a place
// none of the paths in the archive mention.
func noLinksAbove(dir, rel string) error {
	cur := dir
	parts := strings.Split(rel, "/")
	for _, p := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, p)
		st, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // created as a plain directory from here down
		}
		if err != nil {
			return err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s: an entry inside a symbolic link is not allowed", rel)
		}
	}
	return nil
}

// linkStaysInside refuses a symbolic link whose target is absolute or
// resolves outside the uploaded folder.
func linkStaysInside(rel, target string) error {
	t := strings.ReplaceAll(target, "\\", "/")
	if t == "" || strings.HasPrefix(t, "/") || (len(t) > 1 && t[1] == ':') {
		return fmt.Errorf("%s: a symbolic link to an absolute path is not allowed", rel)
	}
	resolved := path.Clean(path.Join(path.Dir(rel), t))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%s: the symbolic link points outside the uploaded folder", rel)
	}
	return nil
}

// fileMode keeps an entry's permission bits, owner-readable and writable at
// least, and nothing beyond them: no setuid, no sticky bit.
func fileMode(m int64) os.FileMode {
	return os.FileMode(m)&0o755 | 0o600
}

// writeEntry copies one file out of the archive, writing at most budget bytes,
// and returns how many it wrote.
func writeEntry(target string, r io.Reader, mode os.FileMode, budget int64) (int64, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(r, budget+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > budget {
		return n, &UploadTooLargeError{}
	}
	return n, err
}

// humanBytes writes a size the way the Settings page asks for it.
func humanBytes(n int64) string {
	const mb = 1 << 20
	if n >= mb && n%mb == 0 {
		return fmt.Sprintf("%d MB", n/mb)
	}
	return fmt.Sprintf("%d bytes", n)
}
