package server

import (
	"database/sql"
	"html/template"
	"io"
	"testing"
	"time"

	"quasar/internal/backup"
	"quasar/internal/catalog"
	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/vps"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{pages: map[string]*template.Template{}}
	if err := s.parseTemplates(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseTemplates(t *testing.T) {
	s := testServer(t)
	for _, p := range []string{"login", "dashboard", "app_new", "app_detail", "settings", "system", "twofa", "terminal"} {
		if s.pages[p] == nil {
			t.Errorf("missing page template %q", p)
		}
	}
}

func TestExecuteTemplates(t *testing.T) {
	s := testServer(t)
	app := AppView{
		App: &db.App{
			ID: "abcd1234", Name: "Blog", Subdomain: "blog",
			DeployType: "image", ImageRef: "nginx:latest", Port: 80,
			WebhookSecret: "s3cret", CustomDomains: "www.example.org",
		},
		Status: docker.AppStatus{State: "running", Uptime: "3h 12m"},
		Domain: "example.com",
	}

	cases := []struct {
		page string
		data any
	}{
		{"login", map[string]any{"Title": "Sign in", "HideNav": true, "Error": "bad"}},
		{"twofa", map[string]any{"Title": "2FA", "HideNav": true, "Error": "bad code"}},
		{"dashboard", map[string]any{"Title": "Dashboard", "Domain": "example.com"}},
		{"app_new", map[string]any{"Title": "New", "Domain": "example.com", "Catalog": catalog.Templates, "Form": app.App}},
		{"app_detail", map[string]any{"Title": "Blog", "App": app}},
		{"terminal", map[string]any{"Title": "Blog terminal", "App": app}},
		{"settings", map[string]any{
			"Title": "Settings", "Theme": "terminal", "Themes": themes,
			"Username": "admin", "Domain": "example.com",
			"AppsDir": "/opt/quasar/apps", "DBPath": "/opt/quasar/storage/database.sqlite",
			"Saved":      "Settings saved.",
			"Registries": []*db.Registry{{ID: 1, Server: "ghcr.io", Username: "me"}},
			"GitTokenSet": true, "NotifyURL": "https://discord.com/api/webhooks/x",
			"TOTPEnabled": false,
			"TOTPSetup":   map[string]string{"Secret": "ABC123", "QR": "data:image/png;base64,x"},
		}},
		{"system", map[string]any{
			"Title": "System",
			"Disk":  docker.DiskUsage{ImagesCount: 5, ImagesSizeGB: 2.4, ContainersCount: 3, VolumesCount: 1},
			"AppSizes": []AppSize{{Name: "Blog", ID: "abcd1234", SizeMB: 12.5}},
			"Backups":  []backup.Info{{Name: "quasar-20260722-120000.tar.gz", SizeMB: 4.2, Date: time.Now()}},
			"AutoOn":   true, "Retention": "7", "Saved": "Backup created.",
			"Current": "v1.0.0", "Latest": "v1.1.0", "UpdateAvail": true,
			"CheckedAt": "2026-07-22", "Repo": "AymericChaverot/quasar",
			"Host":      vps.HostInfo{OS: "Ubuntu 24.04", Kernel: "6.8.0", Arch: "x86_64", Uptime: "3d 4h"},
			"Engine":    docker.EngineInfo{DockerVersion: "29.0.1", APIVersion: "1.44", OSType: "linux/amd64", TraefikImage: "traefik:v3.7"},
			"GoRuntime": "go1.26.5",
		}},
	}
	for _, c := range cases {
		if err := s.pages[c.page].ExecuteTemplate(io.Discard, "layout", c.data); err != nil {
			t.Errorf("execute page %s: %v", c.page, err)
		}
	}

	partials := []struct {
		name string
		data any
	}{
		{"system_stats", vps.Stats{CPUPercent: 42.5, MemPercent: 61, MemUsedGB: 1.2, MemTotalGB: 2, DiskPercent: 90, DiskUsedGB: 18, DiskTotalGB: 20}},
		{"apps_table", []AppView{app, {
			App:    &db.App{ID: "ff00ff00", Name: "Site", Subdomain: "@", DeployType: "image", ImageRef: "nginx", Port: 80},
			Status: docker.AppStatus{State: "running"},
			Domain: "example.com",
		}}},
		{"apps_table", []AppView(nil)},
		{"app_status_panel", app},
		{"container_stats", docker.ContainerStats{CPUPercent: 1.5, MemUsedMB: 128, MemLimitMB: 2048, MemPercent: 6.3}},
		{"container_stats_unavailable", nil},
		{"deploy_fields_image", nil},
		{"deploy_fields_git", nil},
		{"deploy_fields_compose", nil},
		{"env_saved", nil},
		{"deployments", map[string]any{"AppID": "abcd1234", "Deployments": []DeploymentView{
			{Deployment: &db.Deployment{ID: 2, AppID: "abcd1234", Source: "webhook", ImageTag: "qs-abcd1234:100", Status: "running", StartedAt: time.Now()}},
			{Deployment: &db.Deployment{ID: 1, AppID: "abcd1234", Source: "manual", ImageTag: "qs-abcd1234:99", Status: "success",
				StartedAt: time.Now().Add(-time.Hour), FinishedAt: sql.NullTime{Time: time.Now().Add(-59 * time.Minute), Valid: true}}, CanRollback: true},
		}}},
		{"deployments", map[string]any{"AppID": "abcd1234", "Deployments": []DeploymentView(nil)}},
		{"system_containers", []docker.SystemContainer{
			{Name: "quasar-dashboard", Image: "ghcr.io/aymericchaverot/quasar:latest", State: "running", Uptime: "2h 5m"},
			{Name: "quasar-traefik", Image: "traefik:v3.7", State: "running", Uptime: "2h 5m"},
			{Name: "quasar-socket-proxy", Image: "tecnativa/docker-socket-proxy:v0.4.2", State: "exited"},
		}},
		{"system_containers", []docker.SystemContainer(nil)},
		{"tasks", map[string]any{"AppID": "abcd1234", "Tasks": []*db.Task{
			{ID: 1, AppID: "abcd1234", Command: "echo ok", IntervalMinutes: 30,
				LastRun: sql.NullTime{Time: time.Now(), Valid: true}, LastStatus: "success", LastOutput: "ok"},
			{ID: 2, AppID: "abcd1234", Command: "false", LastStatus: "failed"},
		}}},
		{"tasks", map[string]any{"AppID": "abcd1234", "Tasks": []*db.Task(nil)}},
		{"sparks", []Spark{
			buildSpark("CPU · 24h", "%", []db.MetricPoint{
				{TS: time.Now().Add(-2 * time.Minute), V1: 10, V2: 40},
				{TS: time.Now().Add(-time.Minute), V1: 25, V2: 42},
				{TS: time.Now(), V1: 18, V2: 41},
			}, func(p db.MetricPoint) float64 { return p.V1 }, 100),
			buildSpark("Empty", "%", nil, func(p db.MetricPoint) float64 { return p.V1 }, 100),
		}},
		{"status_badge", "running"},
		{"status_badge", "not deployed"},
	}
	host := s.pages["dashboard"]
	for _, p := range partials {
		if err := host.ExecuteTemplate(io.Discard, p.name, p.data); err != nil {
			t.Errorf("execute partial %s: %v", p.name, err)
		}
	}
}
