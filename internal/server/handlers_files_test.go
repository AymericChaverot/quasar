package server

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/config"
	"quasar/internal/docker"
	"quasar/internal/files"
)

// explorerServer is a dashboard whose two views of the host are separate
// directories, as they are in production: the apps tree mounted at its own
// path, and everything else reachable only under HOST_ROOT.
func explorerServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	base := t.TempDir()
	appsDir := filepath.Join(base, "opt", "quasar", "apps")
	hostRoot := filepath.Join(base, "host")
	mustDir(t, filepath.Join(appsDir, "app1", "data"))
	mustDir(t, filepath.Join(hostRoot, "var", "lib", "docker", "volumes", "pgdata", "_data"))

	s := &Server{cfg: config.Config{AppsDir: appsDir, HostRootPath: hostRoot}}
	return s, appsDir, hostRoot
}

func mustDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An app's data directory is mounted at its own path, so it is read directly.
// A volume is only visible under HOST_ROOT, so its host mountpoint has to be
// rewritten before it can be opened at all.
func TestBrowseRootPicksTheRightViewOfTheHost(t *testing.T) {
	s, appsDir, hostRoot := explorerServer(t)

	appData := filepath.Join(appsDir, "app1", "data")
	root, err := s.browseRoot(appData, false)
	if err != nil {
		t.Fatalf("browseRoot(app data) = %v", err)
	}
	if !sameDir(t, root.Path(), appData) {
		t.Errorf("app data resolved to %q, want the directory itself at %q", root.Path(), appData)
	}

	root, err = s.browseRoot("/var/lib/docker/volumes/pgdata/_data", true)
	if err != nil {
		t.Fatalf("browseRoot(volume) = %v", err)
	}
	want := filepath.Join(hostRoot, "var", "lib", "docker", "volumes", "pgdata", "_data")
	if !sameDir(t, root.Path(), want) {
		t.Errorf("volume resolved to %q, want it under the host mount at %q", root.Path(), want)
	}
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// A host path with nothing behind it is the normal case for a bind mount an
// operator's compose file points at a directory this machine does not have.
func TestBrowseRootRefusesWhatIsNotThere(t *testing.T) {
	s, appsDir, _ := explorerServer(t)

	if _, err := s.browseRoot("", false); err == nil {
		t.Error("browseRoot accepted an empty path")
	}
	if _, err := s.browseRoot("/var/lib/docker/volumes/gone/_data", true); err == nil {
		t.Error("browseRoot accepted a path that does not exist")
	}
	mustFile(t, filepath.Join(appsDir, "app1", "data", "note.txt"), "x")
	if _, err := s.browseRoot(filepath.Join(appsDir, "app1", "data", "note.txt"), false); err == nil {
		t.Error("browseRoot accepted a file as a browsable root")
	}
}

func TestUnderAppsDir(t *testing.T) {
	apps := filepath.Join("opt", "quasar", "apps")
	cases := map[string]bool{
		apps:                                    true,
		filepath.Join(apps, "abc", "data"):      true,
		filepath.Join("opt", "quasar", "appsx"): false,
		filepath.Join("opt", "quasar"):          false,
		filepath.Join("var", "lib", "docker"):   false,
	}
	for p, want := range cases {
		if got := underAppsDir(apps, p); got != want {
			t.Errorf("underAppsDir(%q) = %v, want %v", p, got, want)
		}
	}
}

// testTarget is an explorer pointed at a directory, without the Docker lookups
// the real resolution does.
func testTarget(t *testing.T, dir string) browseTarget {
	t.Helper()
	root, err := files.NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return browseTarget{Kind: "volume", Ref: "pgdata", Title: "pgdata", Root: root}
}

func TestURLsCarryTheMountAndEscape(t *testing.T) {
	plain := browseTarget{Kind: "volume", Ref: "my data"}
	if got := plain.URLFor("logs/app.log"); got != "/files/volume/my%20data?path=logs%2Fapp.log" {
		t.Errorf("URLFor = %q", got)
	}
	if got := plain.URLFor(""); got != "/files/volume/my%20data" {
		t.Errorf("URLFor(root) = %q, want no query at all", got)
	}

	// The mount has to survive every step down the tree, or the second click
	// lands on an app with no mount chosen.
	app := browseTarget{Kind: "app", Ref: "abcd1234", Mount: "/var/lib/mysql"}
	for _, got := range []string{
		app.URLFor("db"),
		app.PartialFor("db"),
		app.PreviewFor("db/ib.log"),
		app.RawFor("db/ib.log"),
	} {
		if !strings.Contains(got, "mount=%2Fvar%2Flib%2Fmysql") {
			t.Errorf("%q lost the mount", got)
		}
	}
	if got := app.PreviewFor("db/ib.log"); !strings.HasPrefix(got, "/partials/files/app/abcd1234?") || !strings.Contains(got, "preview=1") {
		t.Errorf("PreviewFor = %q", got)
	}
	if got := app.RawFor("db/ib.log"); !strings.Contains(got, "raw=1") || strings.Contains(got, "inline=1") {
		t.Errorf("RawFor = %q, want a download", got)
	}
	if got := app.InlineFor("logo.png"); !strings.Contains(got, "inline=1") {
		t.Errorf("InlineFor = %q", got)
	}
	if got := app.MountURL("/data"); got != "/files/app/abcd1234?mount=%2Fdata" {
		t.Errorf("MountURL = %q", got)
	}
}

// The cage, at the HTTP layer this time: a crafted path must not fetch a file
// from outside the volume it names.
func TestServeFileRefusesTraversal(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol"))
	mustFile(t, filepath.Join(base, "vol", "app.log"), "hello")
	mustFile(t, filepath.Join(base, "master.key"), "very secret")
	target := testTarget(t, filepath.Join(base, "vol"))

	s := &Server{}
	for _, path := range []string{"../master.key", "/../master.key", "sub/../../master.key"} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/files/volume/pgdata?raw=1&path="+path, nil)
		s.serveFile(rec, r, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("serveFile(%q) = %d, want 404", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "very secret") {
			t.Fatalf("serveFile(%q) served a file outside the volume", path)
		}
	}
}

// What a file is served as decides whether it can attack the dashboard: served
// inline from this origin, an HTML file or an SVG runs in the admin's session.
func TestServeFileHeaders(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol"))
	mustFile(t, filepath.Join(base, "vol", "app.log"), "hello")
	mustFile(t, filepath.Join(base, "vol", "index.html"), "<script>alert(1)</script>")
	mustFile(t, filepath.Join(base, "vol", "logo.svg"), "<svg xmlns='http://www.w3.org/2000/svg'><script/></svg>")
	mustFile(t, filepath.Join(base, "vol", "shot.png"), "\x89PNG\r\n")
	target := testTarget(t, filepath.Join(base, "vol"))
	s := &Server{}

	cases := []struct {
		path        string
		inline      bool
		wantType    string
		disposition string
	}{
		{"app.log", false, "application/octet-stream", "attachment"},
		// Asking for it inline changes nothing: only the image allowlist is
		// ever served that way.
		{"index.html", true, "application/octet-stream", "attachment"},
		{"logo.svg", true, "application/octet-stream", "attachment"},
		{"shot.png", true, "image/png", "inline"},
		{"shot.png", false, "application/octet-stream", "attachment"},
	}
	for _, c := range cases {
		url := "/files/volume/pgdata?raw=1&path=" + c.path
		if c.inline {
			url += "&inline=1"
		}
		rec := httptest.NewRecorder()
		s.serveFile(rec, httptest.NewRequest(http.MethodGet, url, nil), target)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", c.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != c.wantType {
			t.Errorf("%s (inline=%v): Content-Type %q, want %q", c.path, c.inline, got, c.wantType)
		}
		if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, c.disposition) {
			t.Errorf("%s (inline=%v): Content-Disposition %q, want %s", c.path, c.inline, got, c.disposition)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: served without nosniff", c.path)
		}
	}
}

func TestServeFileRefusesADirectory(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol", "sub"))
	s := &Server{}
	rec := httptest.NewRecorder()
	s.serveFile(rec, httptest.NewRequest(http.MethodGet, "/files/volume/pgdata?raw=1&path=sub", nil),
		testTarget(t, filepath.Join(base, "vol")))
	if rec.Code != http.StatusNotFound {
		t.Errorf("serveFile(dir) = %d, want 404", rec.Code)
	}
}

// A root that could not be opened must not serve anything, rather than falling
// back to the zero Root and reading from wherever that lands.
func TestServeFileRefusesAnUnopenedRoot(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.serveFile(rec, httptest.NewRequest(http.MethodGet, "/files/volume/x?raw=1&path=a", nil),
		browseTarget{Kind: "volume", Ref: "x"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("serveFile with no root = %d, want 404", rec.Code)
	}
}

func TestListingErrorsReadAsProse(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{files.ErrOutsideRoot, "outside"},
		{files.ErrNotDir, "not a directory"},
		{os.ErrNotExist, "no longer exists"},
		{os.ErrPermission, "not allowed"},
		{io.ErrUnexpectedEOF, "could not be read"},
	} {
		got := listingError(c.err)
		if !strings.Contains(strings.ToLower(got), c.want) {
			t.Errorf("listingError(%v) = %q, want it to mention %q", c.err, got, c.want)
		}
		if strings.Contains(got, "ErrOutsideRoot") {
			t.Errorf("listingError(%v) leaked a Go error name", c.err)
		}
	}
}

func TestListingWalksTheTree(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol", "conf.d"))
	mustFile(t, filepath.Join(base, "vol", "conf.d", "site.conf"), "listen 80;")
	target := testTarget(t, filepath.Join(base, "vol"))
	s := &Server{}

	data, err := s.listing(target, "conf.d")
	if err != nil {
		t.Fatal(err)
	}
	if data["Parent"] != "" || data["AtRoot"] != false {
		t.Errorf("one level down: Parent=%v AtRoot=%v", data["Parent"], data["AtRoot"])
	}
	entries := data["Entries"].([]files.Entry)
	if len(entries) != 1 || entries[0].Name != "site.conf" {
		t.Fatalf("entries = %+v", entries)
	}
	if crumbs := data["Crumbs"].([]files.Crumb); len(crumbs) != 1 || crumbs[0].Name != "conf.d" {
		t.Errorf("crumbs = %+v", crumbs)
	}

	// A climb out of the volume does not fail, it lands back at the root: the
	// path is cleaned before it is joined, so ".." has nothing left to climb.
	up, err := s.listing(target, "../../..")
	if err != nil {
		t.Fatalf("listing(..) = %v", err)
	}
	if up["AtRoot"] != true || up["Path"] != "" {
		t.Errorf("climbing out landed at %v, want the root", up["Path"])
	}
}

// Every branch of the explorer's markup, since none of it is reachable from the
// other template tests.
func TestExplorerTemplates(t *testing.T) {
	s := testServer(t)
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol", "conf.d"))
	mustFile(t, filepath.Join(base, "vol", "app.log"), "started\n")
	target := testTarget(t, filepath.Join(base, "vol"))
	target.Subtitle = "Docker volume"
	target.HostPath = "/var/lib/docker/volumes/pgdata/_data"
	target.BackURL, target.BackLabel = "/system", "System"

	listing, err := s.listing(target, "")
	if err != nil {
		t.Fatal(err)
	}

	appTarget := browseTarget{
		Kind: "app", Ref: "abcd1234", Title: "Blog", Subtitle: "Storage",
		BackURL: "/apps/abcd1234", BackLabel: "Blog",
		Mounts: []docker.Mount{
			{Type: "volume", Name: "qs-abcd1234_db", Destination: "/var/lib/mysql", RW: true, Service: "db"},
			{Type: "bind", Source: "/opt/quasar/apps/abcd1234/data", Destination: "/data", RW: true},
		},
	}

	empty := browseTarget{Kind: "app", Ref: "x", Title: "Blog", BackLabel: "Blog", BackURL: "/apps/x"}

	cases := []struct {
		what string
		data map[string]any
	}{
		{"a listing", map[string]any{"Title": "pgdata", "Target": target, "Listing": listing, "Path": ""}},
		{"a listing that failed", map[string]any{"Title": "pgdata", "Target": target, "Error": "That path no longer exists."}},
		{"a deep link to a file", map[string]any{"Title": "pgdata", "Target": target, "Listing": listing, "Open": "app.log"}},
		{"a root that will not open", map[string]any{"Title": "pgdata", "Target": target, "Reason": `This volume is on the "nfs" driver.`}},
		{"the mount picker", map[string]any{"Title": "Blog", "Target": appTarget, "Picker": true}},
		{"an app with nothing mounted", map[string]any{"Title": "Blog", "Target": empty, "Picker": true}},
	}
	for _, c := range cases {
		if err := s.pages["files"].ExecuteTemplate(io.Discard, "layout", c.data); err != nil {
			t.Errorf("files page with %s: %v", c.what, err)
		}
	}

	// A file of each kind the preview has a branch for, plus the two lists that
	// lead into the explorer from the app page and from System.
	sub, err := s.listing(target, "conf.d")
	if err != nil {
		t.Fatal(err)
	}
	text, err := target.Root.Read("app.log")
	if err != nil {
		t.Fatal(err)
	}
	big := text
	big.Truncated, big.Size = true, 40<<20

	// The same listing on storage that will not take writes, which is what a
	// named volume looks like on an install that has not mounted Docker's
	// volume tree in: no upload control, no edit, no delete.
	roTarget := target
	roTarget.Root = target.Root.ReadOnly()
	roListing, err := s.listing(roTarget, "")
	if err != nil {
		t.Fatal(err)
	}
	if target.Writable() == roTarget.Writable() {
		t.Fatal("the writable and read-only targets are the same, so this proves nothing")
	}

	partials := []struct {
		name string
		data any
	}{
		{"file_list", listing},
		{"file_list", roListing},
		// What a write reports when it comes back.
		{"file_list", withFlash(listing, flashOK("Uploaded site.conf."))},
		{"file_list", withFlash(listing, flashErr("1 uploaded, 1 refused (big.iso)."))},
		// A folder below the root, which is the only case with a trail and an
		// "up one level" row in it.
		{"file_list", sub},
		{"file_list", map[string]any{"Target": target, "Path": "gone", "Error": "That path no longer exists."}},
		{"file_preview", map[string]any{"Target": target, "Path": "app.log", "Name": "app.log", "Preview": text}},
		{"file_preview", map[string]any{"Target": target, "Path": "app.log", "Name": "app.log", "Preview": big}},
		// Editable, being edited, and the same panel reporting a refused save.
		{"file_preview", map[string]any{"Target": target, "Path": "app.log", "Name": "app.log",
			"Preview": text, "Editable": true}},
		{"file_preview", map[string]any{"Target": target, "Path": "app.log", "Name": "app.log",
			"Preview": text, "Editable": true, "Editing": true}},
		{"file_preview", map[string]any{"Target": target, "Path": "app.log", "Name": "app.log",
			"Preview": text, "Editable": true, "Problem": "This storage is read-only."}},
		// On read-only storage the same file offers only the download.
		{"file_preview", map[string]any{"Target": roTarget, "Path": "app.log", "Name": "app.log",
			"Preview": text}},
		{"file_preview", map[string]any{"Target": target, "Path": "db.sqlite", "Name": "db.sqlite",
			"Preview": files.Preview{Kind: "binary", Size: 4 << 20}}},
		{"file_preview", map[string]any{"Target": target, "Path": "logo.png", "Name": "logo.png",
			"Preview": files.Preview{Kind: "image", Size: 8000}}},
		{"file_preview", map[string]any{"Target": target, "Path": "empty", "Name": "empty",
			"Preview": files.Preview{Kind: "empty"}}},
		{"file_preview", map[string]any{"Target": target, "Path": "gone", "Name": "gone",
			"Error": "That path no longer exists."}},
		{"app_storage", map[string]any{"Mounts": []map[string]any{
			{"Mount": appTarget.Mounts[0], "URL": "/files/app/abcd1234?mount=%2Fvar%2Flib%2Fmysql", "Readable": true},
			{"Mount": appTarget.Mounts[1], "URL": "/files/app/abcd1234?mount=%2Fdata", "Readable": false},
		}}},
		{"app_storage", map[string]any{"Mounts": nil}},
		{"system_volumes", map[string]any{
			"Total": int64(3_400_000_000),
			"Volumes": []map[string]any{
				// One belonging to an app, one held by something outside
				// Quasar, one orphaned, and one whose data is not on this
				// machine at all.
				{"Volume": docker.Volume{Name: "qs-abcd1234_db", Driver: "local", Mountpoint: "/var/lib/docker/volumes/qs-abcd1234_db/_data",
					Bytes: 3_000_000_000, RefCount: 1, AppID: "abcd1234"}, "AppName": "Blog", "URL": "/files/volume/qs-abcd1234_db"},
				{"Volume": docker.Volume{Name: "portainer_data", Driver: "local", Mountpoint: "/x", Bytes: 300_000_000, RefCount: 1}, "URL": "/files/volume/portainer_data"},
				{"Volume": docker.Volume{Name: "orphan-pgdata", Driver: "local", Mountpoint: "/y", Bytes: 100_000_000}, "URL": "/files/volume/orphan-pgdata"},
				{"Volume": docker.Volume{Name: "nfs-share", Driver: "nfs", Bytes: 0}, "URL": "/files/volume/nfs-share"},
			}}},
		{"system_volumes", map[string]any{"Volumes": nil}},
		{"system_volumes", map[string]any{"Error": true}},
	}
	host := s.pages["dashboard"]
	for _, p := range partials {
		if err := host.ExecuteTemplate(io.Discard, p.name, p.data); err != nil {
			t.Errorf("execute partial %s: %v", p.name, err)
		}
	}
}

// uploadBody builds a multipart body of the shape a browser file input sends,
// so the parser under test meets what it will meet in production.
func uploadBody(t *testing.T, parts map[string]string) *multipart.Reader {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for name, content := range parts {
		part, err := w.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return multipart.NewReader(&buf, w.Boundary())
}

func mustRoot(t *testing.T, dir string) files.Root {
	t.Helper()
	root, err := files.NewRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestStoreUploadsWritesEveryPart(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol", "conf.d"))
	root := mustRoot(t, filepath.Join(base, "vol"))

	body := uploadBody(t, map[string]string{"site.conf": "listen 80;", "notes.txt": "hello"})
	saved, failed := storeUploads(root, "conf.d", body)
	if len(saved) != 2 || len(failed) != 0 {
		t.Fatalf("saved=%v failed=%v", saved, failed)
	}
	for name, want := range map[string]string{"site.conf": "listen 80;", "notes.txt": "hello"} {
		got, err := os.ReadFile(filepath.Join(base, "vol", "conf.d", name))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v", name, got, err)
		}
	}
}

// A part's filename is client-controlled. One claiming to be called
// "../../escaped.conf" must not put a file there, and must not stop the rest of
// the batch from landing either.
func TestStoreUploadsRefusesEscapingNames(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol", "sub"))
	root := mustRoot(t, filepath.Join(base, "vol", "sub"))

	body := uploadBody(t, map[string]string{
		"../../escaped.conf": "owned",
		"good.txt":           "fine",
	})
	saved, _ := storeUploads(root, "", body)

	for _, stray := range []string{
		filepath.Join(base, "escaped.conf"),
		filepath.Join(base, "vol", "escaped.conf"),
		filepath.Join(filepath.Dir(base), "escaped.conf"),
	} {
		if _, err := os.Stat(stray); err == nil {
			t.Errorf("a file escaped to %s", stray)
		}
	}
	found := false
	for _, name := range saved {
		if name == "good.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("the rest of the batch was dropped: saved=%v", saved)
	}
}

func TestStoreUploadsReportsWhatFailed(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol", "sub"))
	root := mustRoot(t, filepath.Join(base, "vol"))

	// A name already taken by a directory cannot become a file.
	body := uploadBody(t, map[string]string{"sub": "x", "ok.txt": "y"})
	saved, failed := storeUploads(root, "", body)
	if len(saved) != 1 || saved[0] != "ok.txt" {
		t.Errorf("saved = %v, want just ok.txt", saved)
	}
	if len(failed) != 1 || failed[0] != "sub" {
		t.Errorf("failed = %v, want just sub", failed)
	}
}

// Uploading into read-only storage fails on every part rather than partly
// succeeding.
func TestStoreUploadsIntoReadOnlyStorage(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol"))
	root := mustRoot(t, filepath.Join(base, "vol")).ReadOnly()

	body := uploadBody(t, map[string]string{"a.txt": "x", "b.txt": "y"})
	saved, failed := storeUploads(root, "", body)
	if len(saved) != 0 || len(failed) != 2 {
		t.Errorf("saved=%v failed=%v, want nothing saved", saved, failed)
	}
}

func TestWriteEditNormalisesLineEndings(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol"))
	mustFile(t, filepath.Join(base, "vol", "app.conf"), "old\n")
	root := mustRoot(t, filepath.Join(base, "vol"))

	// What a browser submits from a textarea, whatever was typed into it.
	if err := writeEdit(root, "app.conf", "one\r\ntwo\r\n"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(base, "vol", "app.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one\ntwo\n" {
		t.Errorf("saved %q, want the CRLFs gone", got)
	}
}

// The guard that stops the editor destroying a file it only ever showed part
// of. Without it, saving a 40 MB log back would leave a 256 KB one.
func TestWriteEditRefusesAFileTooLargeToHaveBeenShownWhole(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol"))
	big := strings.Repeat("log line\n", files.PreviewMax/9+500)
	mustFile(t, filepath.Join(base, "vol", "big.log"), big)
	root := mustRoot(t, filepath.Join(base, "vol"))

	err := writeEdit(root, "big.log", "oops")
	if !errors.Is(err, errEditTooBig) {
		t.Fatalf("writeEdit = %v, want errEditTooBig", err)
	}
	after, readErr := os.ReadFile(filepath.Join(base, "vol", "big.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(after) != len(big) {
		t.Errorf("the file is now %d bytes, was %d — the refusal did not hold", len(after), len(big))
	}
	if msg := writeError(err); !strings.Contains(msg, "nothing was written") {
		t.Errorf("writeError = %q, want it to say nothing was written", msg)
	}
}

func TestWriteEditCreatesAndRefusesReadOnly(t *testing.T) {
	base := t.TempDir()
	mustDir(t, filepath.Join(base, "vol"))
	root := mustRoot(t, filepath.Join(base, "vol"))

	if err := writeEdit(root, "new.conf", "port = 1\n"); err != nil {
		t.Errorf("writeEdit(new) = %v", err)
	}
	if err := writeEdit(root.ReadOnly(), "new.conf", "port = 2\n"); !errors.Is(err, files.ErrReadOnly) {
		t.Errorf("writeEdit on read-only = %v, want ErrReadOnly", err)
	}
	if got, _ := os.ReadFile(filepath.Join(base, "vol", "new.conf")); string(got) != "port = 1\n" {
		t.Errorf("the read-only write went through: %q", got)
	}
}

func TestWriteErrorsReadAsProse(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{files.ErrReadOnly, "read-only"},
		{files.ErrTooLarge, "larger"},
		{files.ErrIsDir, "directory"},
		{files.ErrNotRegular, "plain file"},
		{files.ErrBadName, "cannot be used"},
		{os.ErrNotExist, "no longer exists"},
		{os.ErrPermission, "not allowed"},
		{io.ErrUnexpectedEOF, "could not be written"},
	} {
		got := writeError(c.err)
		if !strings.Contains(strings.ToLower(got), c.want) {
			t.Errorf("writeError(%v) = %q, want it to mention %q", c.err, got, c.want)
		}
		if strings.Contains(got, "Err") {
			t.Errorf("writeError(%v) leaked a Go error name: %q", c.err, got)
		}
	}
}

func TestAuditTargetNamesWhatChanged(t *testing.T) {
	app := browseTarget{Kind: "app", Ref: "abcd1234", Title: "Blog"}
	if got := app.auditTarget(); got != "Blog" {
		t.Errorf("app auditTarget = %q, want the name of the app", got)
	}
	vol := browseTarget{Kind: "volume", Ref: "pgdata", Title: "pgdata"}
	if got := vol.auditTarget(); got != "volume pgdata" {
		t.Errorf("volume auditTarget = %q", got)
	}
}

func TestWriteActionURLs(t *testing.T) {
	app := browseTarget{Kind: "app", Ref: "abcd1234", Mount: "/var/lib/mysql"}
	if got := app.UploadURL("conf.d"); got != "/files/app/abcd1234/upload?mount=%2Fvar%2Flib%2Fmysql&path=conf.d" {
		t.Errorf("UploadURL = %q", got)
	}
	if got := app.UploadURL(""); got != "/files/app/abcd1234/upload?mount=%2Fvar%2Flib%2Fmysql" {
		t.Errorf("UploadURL(root) = %q", got)
	}
	if got := app.SaveURL(); !strings.HasPrefix(got, "/files/app/abcd1234/save?") {
		t.Errorf("SaveURL = %q", got)
	}

	// A volume needs no mount, and gets no stray query for one.
	vol := browseTarget{Kind: "volume", Ref: "pgdata"}
	if got := vol.DeleteURL(); got != "/files/volume/pgdata/delete" {
		t.Errorf("volume DeleteURL = %q", got)
	}
	if got := vol.EditFor("app.conf"); !strings.Contains(got, "edit=1") || !strings.Contains(got, "preview=1") {
		t.Errorf("EditFor = %q", got)
	}
}

// withFlash copies a listing with a note attached, so one built listing can be
// rendered in each of the states a write leaves it in.
func withFlash(listing map[string]any, flash map[string]string) map[string]any {
	out := make(map[string]any, len(listing)+1)
	for k, v := range listing {
		out[k] = v
	}
	out["Flash"] = flash
	return out
}
