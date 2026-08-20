package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"quasar/internal/files"
)

// The files capabilities, over the application's own folder.
//
// Dispatched from Do in station_broker.go.

// maxStationFileBytes is what a script may read or write in one call. A
// station edits configuration and drops in mods; a file approaching this is
// not something an action should be moving through a JavaScript string.
const maxStationFileBytes = 4 << 20

type fileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// files reads and writes under apps/<id>/, restricted to the globs the
// document declared.
//
// The confinement is not this function's work: files.Root resolves every path
// through its symlinks before checking it against the root, and its escape
// tests already exist. What is added here is the second, narrower cage — the
// station's own globs — because an application's whole folder is more than any
// station needs and far more than an operator meant to grant.
func (c *stationCall) files(capability string, raw json.RawMessage) (json.RawMessage, error) {
	var a fileArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if !c.doc.Permissions.AllowsPath(a.Path) {
		return nil, denied(fmt.Sprintf("%s(%q)", capability, a.Path), "files")
	}
	root, err := c.root()
	if err != nil {
		return nil, err
	}

	switch capability {
	case "files.list":
		entries, err := root.List(a.Path)
		if err != nil {
			return nil, readableFileError(err, a.Path)
		}
		out := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			out = append(out, map[string]any{
				"name": e.Name, "size": e.Size, "dir": e.IsDir,
				"mtime": e.ModTime.UTC().Format("2006-01-02T15:04:05Z"),
			})
		}
		return json.Marshal(out)

	case "files.read", "files.readBytes":
		f, info, err := root.Open(a.Path)
		if err != nil {
			return nil, readableFileError(err, a.Path)
		}
		defer f.Close()
		if info.Size() > maxStationFileBytes {
			return nil, fmt.Errorf("%s is %d KB, over the %d KB a script may read",
				a.Path, info.Size()/1024, maxStationFileBytes/1024)
		}
		buf := make([]byte, info.Size())
		if _, err := readFull(f, buf); err != nil {
			return nil, err
		}
		if capability == "files.readBytes" {
			// As numbers, which is what a Uint8Array is on the other side.
			// Bytes that are not text have no business being turned into a
			// string on the way past.
			out := make([]int, len(buf))
			for i, b := range buf {
				out[i] = int(b)
			}
			return json.Marshal(out)
		}
		return json.Marshal(string(buf))

	case "files.write":
		// Over the storage explorer's own write: temporary file then rename,
		// so a configuration file is never read half-written, and permissions
		// are preserved — a secret at 600 does not come back at 644 because a
		// station touched it.
		n, err := root.Save(a.Path, strings.NewReader(a.Content), maxStationFileBytes)
		if err != nil {
			return nil, readableFileError(err, a.Path)
		}
		c.audit("station.files.write", fmt.Sprintf("%s (%d bytes)", a.Path, n))
		return json.RawMessage("null"), nil

	case "files.delete":
		if err := root.Remove(a.Path); err != nil {
			return nil, readableFileError(err, a.Path)
		}
		c.audit("station.files.delete", a.Path)
		return json.RawMessage("null"), nil
	}

	if err := root.Mkdir(a.Path); err != nil {
		return nil, readableFileError(err, a.Path)
	}
	return json.RawMessage("null"), nil
}

// root is the application's own folder, and nothing above it.
func (c *stationCall) root() (files.Root, error) {
	root, err := files.NewRoot(filepath.Join(c.srv.cfg.AppsDir, c.app.ID))
	if err != nil {
		return files.Root{}, fmt.Errorf("this application has no folder to read: %w", err)
	}
	return root, nil
}

// readableFileError keeps the cage's own refusals recognisable and says what
// the script was reaching for.
func readableFileError(err error, path string) error {
	switch {
	case errors.Is(err, files.ErrOutsideRoot):
		return fmt.Errorf("%s leaves this application's folder", path)
	case errors.Is(err, files.ErrReadOnly):
		return fmt.Errorf("%s: this application's storage is not writable from here", path)
	}
	return fmt.Errorf("%s: %w", path, err)
}

// readFull fills buf, and is here rather than as io.ReadFull so that a file
// that shrank between the stat and the read is not an error.
func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) || n == 0 {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}
