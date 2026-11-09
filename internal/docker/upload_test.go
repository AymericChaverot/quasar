package docker

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"quasar/internal/db"
)

type tarEntry struct {
	name string
	body string
	link string // symlink target
	typ  byte
}

func gzTar(t *testing.T, entries ...tarEntry) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: e.typ}
		switch {
		case e.typ == 0 && e.link != "":
			h.Typeflag, h.Linkname = tar.TypeSymlink, e.link
		case e.typ == 0 && strings.HasSuffix(e.name, "/"):
			h.Typeflag, h.Mode = tar.TypeDir, 0o755
		case e.typ == 0:
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestReplaceSourceUnpacksAndReplaces(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	src := c.sourceDir("a1")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "stale.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats, err := c.ReplaceSource("a1", gzTar(t,
		tarEntry{name: "./"},
		tarEntry{name: "Dockerfile", body: "FROM nginx\n"},
		tarEntry{name: "site/"},
		tarEntry{name: "site/index.html", body: "<h1>hi</h1>"},
	), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 2 || stats.Bytes != int64(len("FROM nginx\n")+len("<h1>hi</h1>")) {
		t.Errorf("stats = %+v", stats)
	}
	if got, _ := os.ReadFile(filepath.Join(src, "site", "index.html")); string(got) != "<h1>hi</h1>" {
		t.Errorf("index.html = %q", got)
	}
	if _, err := os.Stat(filepath.Join(src, "stale.txt")); !os.IsNotExist(err) {
		t.Error("the previous source should be gone")
	}
	if _, err := os.Stat(src + ".next"); !os.IsNotExist(err) {
		t.Error("the staging directory should be gone")
	}
	if b := c.GitBuildFor(&db.App{ID: "a1", DeployType: "local"}); b.Mode != db.GitBuildDockerfile {
		t.Errorf("an uploaded Dockerfile should be built as one, got mode %q", b.Mode)
	}
}

func TestReplaceSourceRefusesEscapes(t *testing.T) {
	cases := map[string][]tarEntry{
		"parent path":        {{name: "../evil", body: "x"}},
		"nested parent path": {{name: "a/../../evil", body: "x"}},
		"absolute path":      {{name: "/etc/evil", body: "x"}},
		"drive path":         {{name: "C:/evil", body: "x"}},
		"absolute link":      {{name: "l", link: "/etc"}},
		"link out":           {{name: "a/l", link: "../../etc"}},
		"through a link": {
			{name: "d/", typ: tar.TypeDir},
			{name: "l", link: "d"},
			{name: "l/x", body: "x"},
		},
		"hard link": {{name: "h", link: "x", typ: tar.TypeLink}},
	}
	for name, entries := range cases {
		t.Run(name, func(t *testing.T) {
			if name == "through a link" && runtime.GOOS == "windows" {
				t.Skip("symbolic links need privileges on Windows")
			}
			c := &Client{appsDir: t.TempDir()}
			src := c.sourceDir("a1")
			if err := os.MkdirAll(src, 0o755); err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(src, "keep.txt")
			if err := os.WriteFile(keep, []byte("still here"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ReplaceSource("a1", gzTar(t, entries...), 1<<20); err == nil {
				t.Fatal("the upload should have been refused")
			}
			if got, _ := os.ReadFile(keep); string(got) != "still here" {
				t.Error("a refused upload must leave the previous source alone")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(c.appsDir), "evil")); !os.IsNotExist(err) {
				t.Error("something was written outside the source directory")
			}
		})
	}
}

func TestReplaceSourceCapsTheUnpackedSize(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	big := strings.Repeat("a", 2000) // compresses to almost nothing
	_, err := c.ReplaceSource("a1", gzTar(t, tarEntry{name: "big", body: big}), 100)
	var tooBig *UploadTooLargeError
	if !errors.As(err, &tooBig) || tooBig.Limit != 100 {
		t.Fatalf("err = %v, want an UploadTooLargeError naming the limit", err)
	}
	if _, err := os.Stat(c.sourceDir("a1")); !os.IsNotExist(err) {
		t.Error("nothing should be left behind")
	}
}

func TestReplaceSourceRefusesSomethingElse(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	if _, err := c.ReplaceSource("a1", strings.NewReader("not an archive"), 1<<20); err == nil {
		t.Fatal("plain text should be refused")
	}
}

func TestLocalAppNeedsAnUpload(t *testing.T) {
	c := &Client{appsDir: t.TempDir()}
	_, err := c.deployLocal(t.Context(), &db.App{ID: "a1", Subdomain: "blog", DeployType: "local"}, false)
	if err == nil || !strings.Contains(err.Error(), "qsr deploy blog") {
		t.Fatalf("err = %v, want one pointing at qsr deploy", err)
	}
}
