// Package backup archives the platform state: a consistent SQLite snapshot
// plus every app's persistent data directory, as timestamped .tar.gz files.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/notify"
	"quasar/internal/secrets"
)

type Info struct {
	Name   string
	SizeMB float64
	Date   time.Time
}

// Dump runs an app's pre-backup command inside its container and returns what
// it wrote to stdout. Returning nil output with a nil error means there was
// nothing to dump (the app is not running), which is not a failure.
type Dump func(a *db.App) ([]byte, error)

// Run creates a new backup archive in dir and applies the retention policy.
//
// dump may be nil, in which case pre-backup commands are skipped. Without it
// the archive holds only a file-level copy of each app's data directory, which
// for a running database means copying its files mid-write — a snapshot that
// can be unrecoverable however healthy the archive looks.
func Run(database *sql.DB, k *secrets.Keyring, appsDir, dir string, dump Dump) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// VACUUM INTO produces a consistent snapshot even while the DB is in use.
	snap := filepath.Join(dir, ".db-snapshot.sqlite")
	os.Remove(snap)
	if _, err := database.Exec("VACUUM INTO ?", snap); err != nil {
		return "", fmt.Errorf("sqlite snapshot: %w", err)
	}
	defer os.Remove(snap)

	name := "quasar-" + time.Now().Format("20060102-150405") + ".tar.gz"
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	fail := func(err error) (string, error) {
		tw.Close()
		gz.Close()
		f.Close()
		os.Remove(path)
		return "", err
	}

	if err := addFile(tw, snap, "database.sqlite"); err != nil {
		return fail(err)
	}

	// Logical dumps first, and before the data directories are walked, so a
	// database's dump is as close as possible to the rest of the archive.
	if dump != nil {
		apps, err := db.ListApps(database, k)
		if err != nil {
			return fail(fmt.Errorf("list apps for pre-backup commands: %w", err))
		}
		for _, a := range apps {
			if a.PreBackupCmd == "" {
				continue
			}
			out, err := dump(a)
			if err != nil {
				// Failing loudly is the point: a backup that quietly skipped
				// the one command that makes it restorable is worse than none.
				return fail(fmt.Errorf("pre-backup command for %s: %w", a.Name, err))
			}
			if len(out) == 0 {
				continue // app not running, nothing to dump
			}
			if err := addBytes(tw, out, "apps/"+a.ID+"/dump.sql"); err != nil {
				return fail(err)
			}
		}
	}

	// Include every app's data/ and .env (not source/ — that's rebuildable).
	entries, _ := os.ReadDir(appsDir)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		appID := e.Name()
		dataDir := filepath.Join(appsDir, appID, "data")
		if err := addDir(tw, dataDir, "apps/"+appID+"/data"); err != nil {
			return fail(err)
		}
		envFile := filepath.Join(appsDir, appID, ".env")
		if _, err := os.Stat(envFile); err == nil {
			if err := addFile(tw, envFile, "apps/"+appID+"/.env"); err != nil {
				return fail(err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := gz.Close(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}

	applyRetention(database, dir)
	return name, nil
}

// List returns existing backups, newest first.
func List(dir string) []Info {
	entries, _ := os.ReadDir(dir)
	var out []Info
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "quasar-") || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Info{
			Name:   e.Name(),
			SizeMB: float64(fi.Size()) / (1 << 20),
			Date:   fi.ModTime(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.After(out[j].Date) })
	return out
}

// Delete removes a backup archive; the name is validated against the backup
// naming scheme so it cannot reach outside the backups directory.
func Delete(dir, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid backup name")
	}
	return os.Remove(filepath.Join(dir, name))
}

func ValidName(name string) bool {
	return strings.HasPrefix(name, "quasar-") && strings.HasSuffix(name, ".tar.gz") &&
		!strings.ContainsAny(name, `/\`)
}

// StartScheduler runs a daily backup when the auto-backup setting is enabled.
func StartScheduler(database *sql.DB, k *secrets.Keyring, appsDir, dir string, dump Dump) {
	go func() {
		for {
			time.Sleep(24 * time.Hour)
			if db.GetSetting(database, db.SettingBackupAuto) != "true" {
				continue
			}
			if name, err := Run(database, k, appsDir, dir, dump); err != nil {
				log.Printf("scheduled backup: %v", err)
				notify.Send(database, "Quasar: scheduled backup FAILED: "+err.Error())
			} else {
				log.Printf("scheduled backup created: %s", name)
			}
		}
	}()
}

func applyRetention(database *sql.DB, dir string) {
	keep := 7
	if v, err := strconv.Atoi(db.GetSetting(database, db.SettingBackupRetention)); err == nil && v > 0 {
		keep = v
	}
	backups := List(dir)
	for i := keep; i < len(backups); i++ {
		os.Remove(filepath.Join(dir, backups[i].Name))
	}
}

// addBytes writes in-memory content as an archive entry, for artifacts that
// never touch the host's disk (a dump streamed out of a container).
func addBytes(tw *tar.Writer, content []byte, name string) error {
	if err := tw.WriteHeader(&tar.Header{
		Name:    name,
		Mode:    0o600, // a dump is as sensitive as the database it came from
		Size:    int64(len(content)),
		ModTime: time.Now(),
	}); err != nil {
		return err
	}
	_, err := tw.Write(content)
	return err
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func addDir(tw *tar.Writer, dir, prefix string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // missing data dir is fine
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		return addFile(tw, path, prefix+"/"+filepath.ToSlash(rel))
	})
}
