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

	dock, err := docker.New(cfg, database)
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	monitor.Start(database, dock, cfg.HostRootPath)
	backup.StartScheduler(database, cfg.AppsDir, cfg.BackupsDir)
	updater.StartChecker(database, cfg.GitHubRepo)

	srv, err := server.New(cfg, database, dock)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	log.Printf("quasar listening on %s (domain: %s)", cfg.ListenAddr, cfg.Domain)
	if err := http.ListenAndServe(cfg.ListenAddr, srv); err != nil {
		log.Fatal(err)
	}
}
