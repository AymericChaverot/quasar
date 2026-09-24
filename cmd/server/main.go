package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"quasar/internal/auth"
	"quasar/internal/backup"
	"quasar/internal/boot"
	"quasar/internal/config"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/event"
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

	// Every line the dashboard logs from here on, its own and the standard
	// log package's, in the one shape and without a timestamp of its own.
	event.CaptureStandardLog()

	cfg := config.Load()
	printBanner()
	seq := boot.Start()

	environment := "production"
	if !cfg.CookieSecure {
		environment = "development"
	}
	seq.OK("config", "domain "+cfg.Domain, environment)
	// Everything the dashboard read, as it is used, so what it started with
	// is on record rather than inferred from a file that may since have
	// changed. Only a value that did not come from the environment is
	// annotated: a default is what most often explains a surprise.
	for _, s := range cfg.Settings() {
		note := ""
		switch s.Source {
		case "default":
			note = "default"
		case ".env":
			note = "from .env"
		}
		seq.Setting(s.Name, s.Value, note)
	}

	database, err := db.Open(cfg.DBPath)
	if err != nil {
		seq.Fatal("database", err)
	}
	defer database.Close()

	if err := auth.EnsureAdmin(database, cfg.AdminUser, cfg.AdminPassword); err != nil {
		seq.Fatal("admin", err)
	}

	// The master key lives alongside the database (persisted, mounted volume)
	// but — deliberately — outside anything backup.Run archives, so a leaked
	// backup or a copied-out database file alone can't be decrypted. Which is
	// also why it has to be kept somewhere safe: see /system's key download.
	_, statErr := os.Stat(cfg.KeyPath)
	keyring, err := secrets.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		seq.Fatal("master key", err)
	}
	keyState := "loaded"
	if os.IsNotExist(statErr) {
		// A new key on an install that already has data is the one thing on
		// this list worth stopping to read: nothing encrypted before can be
		// opened with it.
		keyState = "created — download it from System and keep it safe"
	}
	seq.OK("master key", keyState)

	var migrations []string
	if n, err := db.EncryptLegacyApps(database, keyring); err != nil {
		seq.Warn("migration", "encrypting legacy app secrets: "+err.Error())
	} else if n > 0 {
		migrations = append(migrations, fmt.Sprintf("encrypted %d app(s)' stored env and compose at rest", n))
	}
	// The platform-wide git token of earlier versions becomes the any-host
	// credential, sealed rather than left in the settings table in plaintext.
	if moved, err := db.MigrateGitToken(database, keyring); err != nil {
		seq.Warn("migration", "moving the git token: "+err.Error())
	} else if moved {
		migrations = append(migrations, "moved the stored git token into encrypted git credentials")
	}
	if len(migrations) > 0 {
		seq.OK("migration", migrations...)
	}

	apps, err := db.ListApps(database, keyring)
	if err != nil {
		seq.Warn("database", cfg.DBPath, "listing applications: "+err.Error())
	} else {
		seq.OK("database", cfg.DBPath, plural(len(apps), "application"), plural(db.CountEnabledStations(database), "station")+" installed")
	}

	dock, err := docker.New(cfg, database, keyring)
	if err != nil {
		seq.Fatal("docker", err)
	}
	// Asked once, with a short leash: a daemon that does not answer here is
	// reported, not waited on — the dashboard is how that gets looked into.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	engine := dock.EngineInfo(ctx)
	cancel()
	if engine.DockerVersion == "unknown" {
		seq.Warn("docker", "the daemon did not answer — applications cannot be managed until it does")
	} else {
		seq.OK("docker", "Engine "+engine.DockerVersion, "API "+engine.APIVersion, engine.OSType)
	}
	if engine.TraefikImage != "" {
		seq.OK("traefik", engine.TraefikImage)
	}

	monitor.Start(database, dock, cfg.HostRootPath, keyring)
	backup.StartScheduler(database, keyring, cfg.AppsDir, cfg.BackupsDir, dock.DumpForBackup)
	updater.StartChecker(database, cfg.GitHubRepo)

	srv, err := server.New(cfg, database, dock, keyring)
	if err != nil {
		seq.Fatal("server", err)
	}
	// A station's hooks run without anybody having pressed anything, so the
	// loop that fires them belongs here rather than inside a request.
	srv.StartStationHooks()
	seq.OK("background", "metrics and health checks", "backup schedule", "update checks", "station hooks")

	// Bound before "ready" is said, so the line is only ever written by a
	// dashboard that is actually listening.
	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		seq.Fatal("http", err)
	}
	seq.Ready("listening on " + cfg.ListenAddr)
	serve(ln, srv)
}

// shutdownGrace is how long requests in flight get to finish once the
// dashboard is asked to stop. Under the ten seconds `docker stop` waits before
// it kills the process, so the last line in the log is the dashboard's own.
const shutdownGrace = 8 * time.Second

// serve answers requests until the process is asked to stop, and says so: a
// dashboard that went away on its own and one that was stopped look the same
// in `docker ps`, and only the log can tell them apart.
func serve(ln net.Listener, handler http.Handler) {
	// Every request's context descends from this one, so cancelling it ends
	// the streams that would otherwise never finish on their own — live logs,
	// deploy progress — and the shutdown does not sit out its whole grace
	// period waiting for them.
	base, endStreams := context.WithCancel(context.Background())
	hs := &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return base }}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, os.Interrupt)
	served := make(chan error, 1)
	go func() { served <- hs.Serve(ln) }()

	select {
	case err := <-served:
		log.Fatal(err)
	case sig := <-stop:
		event.Info("shutdown", "received "+signalName(sig), "stopping")
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		endStreams()
		if err := hs.Shutdown(ctx); err != nil {
			event.Warning("shutdown", "closed the requests still open after "+shutdownGrace.String())
		}
		event.Info("shutdown", "stopped in "+time.Since(started).Round(time.Millisecond).String())
	}
}

func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "SIGTERM"
	case os.Interrupt:
		return "SIGINT"
	}
	return sig.String()
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
