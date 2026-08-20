package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"quasar/internal/db"
)

// The env capabilities, over named keys of the application's .env.
//
// Dispatched from Do in station_broker.go.

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
