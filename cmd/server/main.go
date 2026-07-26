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

	dock, err := docker.New(cfg, database)
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	monitor.Start(database, dock, cfg.HostRootPath, keyring)
	backup.StartScheduler(database, cfg.AppsDir, cfg.BackupsDir)
	updater.StartChecker(database, cfg.GitHubRepo)

	srv, err := server.New(cfg, database, dock, keyring)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	log.Printf("quasar listening on %s (domain: %s)", cfg.ListenAddr, cfg.Domain)
	if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
		log.Fatal(err)
	}
}
