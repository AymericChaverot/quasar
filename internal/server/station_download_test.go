package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"quasar/internal/station"
	"quasar/internal/station/ui"
)

// Handing a file over.
//
// A station's world archive is the case: minutes to make, gigabytes on disk,
// and useless where it is. What matters is that the offer is held to the same
// permission a read would be — a station that may not read a file may not give
// it away either, and the two checks must not be able to drift apart.

// offering is a station granted the paths given, and an application with one
// archive in it.
func offering(t *testing.T, paths ...string) (*stationCall, string) {
	t.Helper()
	c, dir := brokerFor(t, station.Permissions{Files: station.Files{Paths: paths}}, "")
	if err := os.MkdirAll(filepath.Join(dir, "data", "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "backups", "world.tgz"), []byte("archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func TestAFileIsOfferedOnlyWhereTheStationCouldReadIt(t *testing.T) {
	c, dir := offering(t, "data/backups/**")

	ok := c.srv.allowDownload(c.app, c.doc, ui.Result{Toast: "done", Download: "data/backups/world.tgz"})
	if ok.Error != "" || ok.Download != "data/backups/world.tgz" {
		t.Fatalf("an archive inside the declared globs was refused: %+v", ok)
	}

	// The .env sits in the same folder and is not covered, so the station may
	// not read it — and therefore may not hand it over.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := c.srv.allowDownload(c.app, c.doc, ui.Result{Download: ".env"})
	switch {
	case refused.Error == "":
		t.Error("a station handed over a file its files permission does not cover")
	case !strings.Contains(refused.Error, "files"):
		t.Errorf("the refusal does not name the permission: %q", refused.Error)
	case refused.Download != "":
		t.Error("the refused file is still on offer")
	}

	// And a path that climbs out is judged where it lands, not as it is
	// written.
	if out := c.srv.allowDownload(c.app, c.doc, ui.Result{Download: "data/backups/../../../../etc/passwd"}); out.Error == "" {
		t.Error("a path leaving the application's folder was offered")
	}
}

// A file that is not there, or is a folder, is said so at the moment the action
// returns rather than as a link that fails when somebody clicks it.
func TestAnOfferOfSomethingThatIsNotAFileSaysSo(t *testing.T) {
	c, _ := offering(t, "data/backups/**", "data/**")

	for path, want := range map[string]string{
		"data/backups/yesterday.tgz": "no such file",
		"data/backups":               "is a folder",
	} {
		got := c.srv.allowDownload(c.app, c.doc, ui.Result{Download: path})
		if !strings.Contains(got.Error, want) {
			t.Errorf("%s came back as %q, want something saying %q", path, got.Error, want)
		}
	}
}

// The header the browser acts on. A download carries an address, so it is the
// JSON form; everything else stays the plain list, which is what almost every
// action produces.
func TestTheTriggerHeaderCarriesTheDownload(t *testing.T) {
	c, _ := offering(t, "data/backups/**")

	plain := stationTriggers(c.app, ui.Result{Refresh: []string{"mc_backups"}})
	if plain != stationRefreshEvent("mc_backups") {
		t.Errorf("an ordinary refresh became %q", plain)
	}

	withFile := stationTriggers(c.app, ui.Result{
		Refresh: []string{"mc_backups"}, Download: "data/backups/world.tgz"})
	for _, want := range []string{
		stationDownloadEvent(),
		"/apps/" + c.app.ID + "/station/download?path=data%2Fbackups%2Fworld.tgz",
		stationRefreshEvent("mc_backups"),
	} {
		if !strings.Contains(withFile, want) {
			t.Errorf("the header does not carry %q: %s", want, withFile)
		}
	}
}

// A long action's file is offered in its pane, because nobody is holding a
// request open for a job that takes minutes — and because the pane is the one
// place the offer survives a reload.
func TestALongActionOffersItsFileInThePane(t *testing.T) {
	c, _ := offering(t, "data/backups/**")

	job := &stationJob{}
	job.finish(c.srv.allowDownload(c.app, c.doc,
		ui.Result{Toast: "written", Download: "data/backups/world.tgz"}), "")

	v := job.view(c.app.ID, "back_up")
	if v.DownloadName != "world.tgz" || !strings.Contains(v.Download, "station/download") {
		t.Fatalf("the pane offers %+v", v)
	}
	page := renderJobPane(t, v)
	if !strings.Contains(page, "Download world.tgz") {
		t.Errorf("the pane does not offer the file:\n%s", page)
	}
}
