package main

import (
	"log"
	"net/http"

	"quasar/internal/auth"
	"quasar/internal/backup"
	"quasar/internal/config"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/monitor"
	"quasar/internal/secrets"
	"quasar/internal/server"
	"quasar/internal/updater"
)

func main() {
	// Before anything else opens a database, a Docker connection or the master
	// key: a worker gets none of them, and the way to be sure of that is for
	// this process never to have had them.
	runWorkerMode()

	cfg := config.Load()

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer database.Close()

	if err := auth.EnsureAdmin(database, cfg.AdminUser, cfg.AdminPassword); err != nil {
		log.Fatalf("admin bootstrap: %v", err)
	}

	// The master key lives alongside the database (persisted, mounted volume)
	// but — deliberately — outside anything backup.Run archives, so a leaked
	// backup or a copied-out database file alone can't be decrypted. Which is
	// also why it has to be kept somewhere safe: see /system's key download.
	keyring, err := secrets.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		log.Fatalf("encryption key: %v", err)
	}
	if n, err := db.EncryptLegacyApps(database, keyring); err != nil {
		log.Printf("encrypt legacy app secrets: %v", err)
	} else if n > 0 {
		log.Printf("encrypted %d app(s)' stored env/compose data at rest", n)
	}

	// The platform-wide git token of earlier versions becomes the any-host
	// credential, sealed rather than left in the settings table in plaintext.
	if moved, err := db.MigrateGitToken(database, keyring); err != nil {
		log.Printf("migrate git token: %v", err)
	} else if moved {
		log.Print("moved the stored git token into encrypted git credentials (host: any)")
	}

	dock, err := docker.New(cfg, database, keyring)
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	monitor.Start(database, dock, cfg.HostRootPath, keyring)
	backup.StartScheduler(database, keyring, cfg.AppsDir, cfg.BackupsDir, dock.DumpForBackup)
	updater.StartChecker(database, cfg.GitHubRepo)

	srv, err := server.New(cfg, database, dock, keyring)
	if err != nil {
		log.Fatalf("server: %v", err)
	}
	// A station's hooks run without anybody having pressed anything, so the
	// loop that fires them belongs here rather than inside a request.
	srv.StartStationHooks()

	log.Printf("quasar listening on %s (domain: %s)", cfg.ListenAddr, cfg.Domain)
	if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
		log.Fatal(err)
	}
}
