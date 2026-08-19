package backup

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

// restoredTables are copied from the archive into the live database.
// Sessions are deliberately excluded so current logins survive the restore.
var restoredTables = []string{"users", "apps", "registries", "git_credentials", "settings", "deployments", "tasks"}

// Restore extracts a backup archive: app data directories and .env files are
// put back in place, and database tables are copied from the snapshot into
// the live database via ATTACH (the server keeps running throughout).
// Apps must be redeployed afterwards to pick up restored state.
//
// Archives never contain the master key, so rows from an archive taken on
// another host are sealed with a key this install does not have. Pass that key
// as archiveKey and the restored rows are moved onto the live key; pass nil
// when restoring an archive from this same install.
func Restore(database *sql.DB, appsDir, dir, name string, live, archiveKey *secrets.Keyring) error {
	if !ValidName(name) {
		return fmt.Errorf("invalid backup name")
	}

	tmp, err := os.MkdirTemp(dir, ".restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	if err := extract(filepath.Join(dir, name), tmp); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// 1. Database tables from the snapshot.
	snap := filepath.Join(tmp, "database.sqlite")
	if _, err := os.Stat(snap); err == nil {
		if err := restoreTables(database, snap); err != nil {
			return fmt.Errorf("restore database: %w", err)
		}
		// The rows just landed sealed with the archive's key; without this
		// step every restored app's env and compose data is unreadable.
		if archiveKey != nil {
			if _, err := db.ResealApps(database, archiveKey, live); err != nil {
				return fmt.Errorf("the supplied master key does not open this archive: %w", err)
			}
		}
	}

	// 2. App data directories and .env files.
	appsRoot := filepath.Join(tmp, "apps")
	entries, _ := os.ReadDir(appsRoot)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		appID := e.Name()
		if err := os.MkdirAll(filepath.Join(appsDir, appID), 0o755); err != nil {
			return err
		}
		src := filepath.Join(appsRoot, appID, "data")
		dst := filepath.Join(appsDir, appID, "data")
		if _, err := os.Stat(src); err == nil {
			if err := os.RemoveAll(dst); err != nil {
				return err
			}
			if err := copyTree(src, dst); err != nil {
				return fmt.Errorf("restore data for %s: %w", appID, err)
			}
		}
		envSrc := filepath.Join(appsRoot, appID, ".env")
		if _, err := os.Stat(envSrc); err == nil {
			if err := copyFile(envSrc, filepath.Join(appsDir, appID, ".env"), 0o600); err != nil {
				return err
			}
		}
		// A logical dump is put back beside the data directory rather than
		// loaded: replaying it into a live database is the operator's call,
		// and it is the copy to trust over the file-level one next to it.
		dumpSrc := filepath.Join(appsRoot, appID, "dump.sql")
		if _, err := os.Stat(dumpSrc); err == nil {
			if err := copyFile(dumpSrc, filepath.Join(appsDir, appID, "dump.sql"), 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

// restoreTables copies whole tables from the snapshot into the live DB inside
// one transaction. Tables whose schema differs (older backups) are skipped.
func restoreTables(database *sql.DB, snapPath string) error {
	if _, err := database.Exec("ATTACH DATABASE ? AS restore_src", snapPath); err != nil {
		return err
	}
	defer database.Exec("DETACH DATABASE restore_src")

	tx, err := database.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, table := range restoredTables {
		var n int
		// A count that will not run reads as a table this backup does not
		// carry, which is the case the next line already skips.
		if err := tx.QueryRow("SELECT COUNT(*) FROM restore_src.sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n); err != nil {
			continue
		}
		if n == 0 {
			continue // table absent from an older backup
		}
		if columnCount(tx, "main", table) != columnCount(tx, "restore_src", table) {
			continue // schema drift: skip rather than corrupt
		}
		if _, err := tx.Exec("DELETE FROM main." + table); err != nil {
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf("INSERT INTO main.%s SELECT * FROM restore_src.%s", table, table)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func columnCount(tx *sql.Tx, schema, table string) int {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA %s.table_info(%s)", schema, table))
	if err != nil {
		return -1
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	return n
}

func extract(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		// Reject entries that would escape the destination directory.
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("unsafe path in archive: %s", hdr.Name)
		}
		target := filepath.Join(dest, clean)
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			_ = out.Close() // the copy error is the one that explains the failure
			return err
		}
		// Checked, not deferred: a close is where a buffered write finally
		// reaches the disk, and a restore that dropped its last block is
		// worse than one that said it failed.
		if err := out.Close(); err != nil {
			return err
		}
		// The timestamp is a courtesy; the contents are the restore.
		_ = os.Chtimes(target, time.Now(), hdr.ModTime)
	}
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
