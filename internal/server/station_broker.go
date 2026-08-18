package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"quasar/internal/db"
	"quasar/internal/files"
	"quasar/internal/station"
)

// The privileged half of a station call.
//
// A station's script runs in a process holding no socket, no disk, no network
// and no database handle, so everything it wants done arrives here as a
// request. This is where the permission is checked, where the work is done
// with privileges the worker does not have, and where the audit entry is
// written — all three on the same side of the process boundary, which is the
// property the whole design was built for. A check that only existed as "we
// did not inject that binding" would be one goja bug away from nothing.
//
// Every capability follows the same three steps: read the arguments, refuse
// unless the document declared this exact thing, do it. The refusals say what
// was missing, because the operator reading them is usually the author.

// stationCall is one call's context: which application, which station, and who
// asked.
type stationCall struct {
	srv *Server
	app *db.App
	doc station.Station
	r   *http.Request
}

// Do performs one capability on the worker's behalf.
func (c *stationCall) Do(ctx context.Context, capability string, args json.RawMessage) (json.RawMessage, error) {
	switch capability {
	case "store.get", "store.set", "store.delete", "store.keys":
		return c.store(capability, args)
	case "files.list", "files.read", "files.readBytes", "files.write", "files.delete", "files.mkdir":
		return c.files(capability, args)
	case "env.get", "env.set":
		return c.env(capability, args)
	}
	return nil, fmt.Errorf("this build of Quasar does not offer %s", capability)
}

// denied is a refusal in the words the author needs: what was reached for, and
// which line of the document would have allowed it.
func denied(what, permission string) error {
	return fmt.Errorf("%s: this station's %s permission does not cover it", what, permission)
}

// ---------------------------------------------------------------- store ----

// storeArgs are the arguments every store call takes some of.
type storeArgs struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// store is the one capability that needs no permission: it is scoped to this
// application and this station, and there is nothing in it a station could
// reach that is not its own.
func (c *stationCall) store(capability string, raw json.RawMessage) (json.RawMessage, error) {
	var a storeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if capability != "store.keys" && a.Key == "" {
		return nil, errors.New("quasar.store needs a key")
	}

	switch capability {
	case "store.get":
		value, ok := db.StationStoreGet(c.srv.db, c.app.ID, c.doc.ID, a.Key)
		if !ok {
			return json.RawMessage("null"), nil
		}
		return json.RawMessage(value), nil

	case "store.set":
		value := string(a.Value)
		if value == "" {
			value = "null"
		}
		if err := db.StationStoreSet(c.srv.db, c.app.ID, c.doc.ID, a.Key, value); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil

	case "store.delete":
		if err := db.StationStoreDelete(c.srv.db, c.app.ID, c.doc.ID, a.Key); err != nil {
			return nil, err
		}
		return json.RawMessage("null"), nil
	}

	keys, err := db.StationStoreKeys(c.srv.db, c.app.ID, c.doc.ID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(keys)
}

// ---------------------------------------------------------------- files ----

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

// ------------------------------------------------------------------ env ----

type envArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// env reads and writes named keys of the application's .env, per key and per
// direction. A station has no business reading a database password it did not
// generate, which is why this is a list of keys and not a permission to read
// the file.
func (c *stationCall) env(capability string, raw json.RawMessage) (json.RawMessage, error) {
	var a envArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}

	if capability == "env.get" {
		if !c.doc.Permissions.AllowsEnvRead(a.Key) {
			return nil, denied(fmt.Sprintf("reading %s", a.Key), "env")
		}
		value, ok := envValue(c.app.EnvContent, a.Key)
		if !ok {
			return json.RawMessage("null"), nil
		}
		return json.Marshal(value)
	}

	if !c.doc.Permissions.AllowsEnvWrite(a.Key) {
		return nil, denied(fmt.Sprintf("writing %s", a.Key), "env")
	}
	if strings.ContainsAny(a.Value, "\n\r") {
		return nil, fmt.Errorf("%s: a value cannot carry a line break", a.Key)
	}

	updated := setEnvValue(c.app.EnvContent, a.Key, a.Value)
	if err := db.UpdateAppEnv(c.srv.db, c.srv.keyring, c.app.ID, updated); err != nil {
		return nil, err
	}
	c.app.EnvContent = updated
	c.audit("station.env.write", a.Key)
	// Said rather than done: the environment is read when a stack is brought
	// up, so a value changed here is not a value in effect. A station that
	// means it to take effect asks for a restart, which is a permission of its
	// own and an entry of its own in this log.
	return json.RawMessage("null"), nil
}

// envValue reads one key out of .env content.
func envValue(content, key string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == key {
			return trimEnvQuotes(v), true
		}
	}
	return "", false
}

// setEnvValue replaces one key in place, or appends it, keeping every other
// line — comments included — exactly as it was. The .env is a file a person
// edits, and a station that reformatted it on every write would be one nobody
// could keep an ordering or a comment in.
func setEnvValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == key {
			lines[i] = key + "=" + value
			return strings.Join(lines, "\n")
		}
	}
	out := strings.TrimRight(content, "\n")
	if out != "" {
		out += "\n"
	}
	return out + key + "=" + value + "\n"
}

func trimEnvQuotes(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// ---------------------------------------------------------------- audit ----

// audit records one privileged thing a station did, so that "what did this
// station do to my server" has an answer that is not "look at the logs and
// guess".
func (c *stationCall) audit(action, detail string) {
	target := c.doc.ID + " on " + c.app.Name
	if c.r != nil {
		c.srv.audit(c.r, action, target, detail)
		return
	}
	// A hook or a scheduled action is nobody's click. Attributing it to
	// whoever happened to be signed in would be worse than saying which
	// station did it.
	db.RecordAudit(c.srv.db, db.AuditEntry{
		Actor:  "station " + c.doc.ID,
		Action: action,
		Target: target,
		Detail: detail,
	})
}
