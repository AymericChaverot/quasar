package station

import "testing"

func TestAllowsPath(t *testing.T) {
	p := Permissions{Files: Files{Paths: []string{
		"data/mods/**",
		"data/server.properties",
		"config/*.toml",
	}}}

	for _, c := range []struct {
		path string
		want bool
		why  string
	}{
		{"data/mods/sodium.jar", true, "the obvious case"},
		{"data/mods/nested/deep.jar", true, "** crosses separators"},
		{"data/mods", true, "** matches nothing at all, which is the folder itself"},
		{"data/server.properties", true, "an exact path"},
		{"config/app.toml", true, "* inside one segment"},

		{"data/ops.json", false, "a sibling the document did not name"},
		{"config/nested/app.toml", false, "* does not cross a separator"},
		{"data/server.properties.bak", false, "an exact path is exact"},
		{"", false, "nothing is not a path"},

		// The two ways out, both closed before anything touches a disk.
		{"../../etc/shadow", false, "climbing out"},
		{"data/mods/../../../etc/shadow", false, "climbing out from inside a match"},
		{"/etc/shadow", false, "an absolute path is read as relative to the app"},
		{`data\mods\..\..\..\etc\shadow`, false, "and the same with the other separator"},
	} {
		if got := p.AllowsPath(c.path); got != c.want {
			t.Errorf("AllowsPath(%q) = %v, want %v — %s", c.path, got, c.want, c.why)
		}
	}
}

// A station with no files permission reaches no file. The empty case is the
// one worth pinning: it is what every station starts as.
func TestNoFilesPermissionAllowsNothing(t *testing.T) {
	var p Permissions
	for _, path := range []string{"data", "data/x", "", "/"} {
		if p.AllowsPath(path) {
			t.Errorf("AllowsPath(%q) with no permission declared", path)
		}
	}
}

// Reading and writing are separate grants, and a key that can be written can
// be read — a station that may change a value it cannot see would be writing
// blind.
func TestEnvIsPerKeyAndPerDirection(t *testing.T) {
	p := Permissions{Env: Env{Read: []string{"TYPE"}, Write: []string{"MINECRAFT_VERSION"}}}

	for _, c := range []struct {
		key         string
		read, write bool
	}{
		{"TYPE", true, false},
		{"MINECRAFT_VERSION", true, true},
		{"POSTGRES_PASSWORD", false, false},
		{"type", false, false},
	} {
		if got := p.AllowsEnvRead(c.key); got != c.read {
			t.Errorf("AllowsEnvRead(%q) = %v, want %v", c.key, got, c.read)
		}
		if got := p.AllowsEnvWrite(c.key); got != c.write {
			t.Errorf("AllowsEnvWrite(%q) = %v, want %v", c.key, got, c.write)
		}
	}
}

// net.external is a list of hosts and not a pattern, which is what makes it
// worth showing to an operator in the first place.
func TestAllowsHostIsExact(t *testing.T) {
	p := Permissions{NetExternal: NetExternal{Allow: []string{"api.modrinth.com"}}}

	for _, c := range []struct {
		host string
		want bool
	}{
		{"api.modrinth.com", true},
		{"API.Modrinth.com", true},
		{"cdn.modrinth.com", false},
		{"evil.com/api.modrinth.com", false},
		{"api.modrinth.com.evil.com", false},
		{"modrinth.com", false},
	} {
		if got := p.AllowsHost(c.host); got != c.want {
			t.Errorf("AllowsHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// A service and a port, not a service or a port: reaching a map viewer on 8123
// is not reaching the same container's admin port.
func TestAllowsInternalNeedsBoth(t *testing.T) {
	p := Permissions{NetInternal: NetInternal{Services: []string{"minecraft"}, Ports: []int{8123}}}

	for _, c := range []struct {
		service string
		port    int
		want    bool
	}{
		{"minecraft", 8123, true},
		{"minecraft", 25575, false},
		{"db", 8123, false},
	} {
		if got := p.AllowsInternal(c.service, c.port); got != c.want {
			t.Errorf("AllowsInternal(%q, %d) = %v, want %v", c.service, c.port, got, c.want)
		}
	}
}
