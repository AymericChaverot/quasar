package server

import (
	"database/sql"
	"html/template"
	"io"
	"testing"
	"time"

	"quasar/internal/backup"
	"quasar/internal/catalog"
	"quasar/internal/certs"
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

// composeRouteView is a stack app with one particular reading of its compose
// file, for the panel that reports it.
func composeRouteView(base AppView, adaptation docker.ComposeAdaptation) AppView {
	base.Compose = adaptation
	base.Network = "traefik-net"
	return base
}

func TestParseTemplates(t *testing.T) {
	s := testServer(t)
	for _, p := range []string{"login", "dashboard", "app_new", "app_detail", "settings", "system", "twofa", "terminal", "git_credentials"} {
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

	// A git app deployed as a stack: the branch that renders the build panel,
	// and the one carrying a compose file name the Dockerfile branch has not.
	gitApp := AppView{
		App: &db.App{
			ID: "beef0001", Name: "API", Subdomain: "api",
			DeployType: "git", GitURL: "https://example.com/org/api.git", GitBranch: "main",
			Port: 8080, WebhookSecret: "s3cret",
		},
		Status:  docker.AppStatus{State: "running", Uptime: "5m"},
		Domain:  "example.com",
		Build:   docker.GitBuild{Mode: "compose", HasCompose: true, HasDockerfile: true, ComposeFile: "docker-compose.yml"},
		Stack:   true,
		IsAdmin: true,
	}

	stackContainers := []docker.AppContainer{
		{Name: "qs-beef0001-backend-1", Service: "backend", Image: "beef0001-backend", State: "running", Uptime: "5m"},
		{Name: "qs-beef0001-nginx-1", Service: "nginx", Image: "nginx:alpine", State: "running", Uptime: "5m", IsWeb: true},
		{Name: "qs-beef0001-worker-1", Service: "worker", Image: "beef0001-worker", State: "exited"},
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
		{"app_detail", map[string]any{"Title": "API", "App": gitApp, "IsAdmin": true}},
		{"app_container_detail", map[string]any{
			"Title": "qs-beef0001-nginx-1", "App": gitApp, "Container": &stackContainers[1]}},
		{"app_container_detail", map[string]any{
			"Title": "qs-beef0001-worker-1", "App": gitApp, "Container": &stackContainers[2]}},
		{"terminal", map[string]any{"Title": "Blog terminal", "App": app}},
		{"settings", map[string]any{
			"Title": "Settings", "Theme": "terminal", "Themes": themes,
			"Username": "admin", "Domain": "example.com",
			"AppsDir": "/opt/quasar/apps", "DBPath": "/opt/quasar/storage/database.sqlite",
			"Saved":        "Settings saved.",
			"Registries":   []*db.Registry{{ID: 1, Server: "ghcr.io", Username: "me"}},
			"GitCredCount": 2, "NotifyURL": "https://discord.com/api/webhooks/x",
			"TOTPEnabled": false,
			"TOTPSetup":   map[string]string{"Secret": "ABC123", "QR": "data:image/png;base64,x"},
		}},
		{"system", map[string]any{
			"Title": "System",
			"Disk": docker.DiskUsage{
				ImagesCount: 5, ImagesBytes: 2_600_000_000, ContainersCount: 3,
				ContainersBytes: 41_000_000, VolumesCount: 1, VolumesBytes: 900_000_000,
				CacheCount: 12, CacheBytes: 1_400_000_000,
			},
			"Cleanup": docker.CleanupScan{
				Items: []docker.Reclaimable{
					{Key: "images", Label: "Images no container uses", Count: 2, Bytes: 700_000_000, Note: "nginx:1.24, redis:7"},
					{Key: "dangling", Label: "Untagged layers left by rebuilds", Count: 6, Bytes: 1_100_000_000},
					{Key: "cache", Label: "Build cache no longer referenced", Count: 12, Bytes: 1_400_000_000},
					{Key: "networks", Label: "Networks with nothing attached", Count: 1, Note: "qs-dead0001_default"},
				},
				Volumes: docker.Reclaimable{Key: "volumes", Count: 2, Bytes: 340_000_000, Note: "orphan-pgdata, tmpcache"},
				Count:   21, Bytes: 3_200_000_000,
			},
			"AppSizes": []AppSize{{Name: "Blog", ID: "abcd1234", SizeMB: 12.5}},
			"Backups":  []backup.Info{{Name: "quasar-20260722-120000.tar.gz", SizeMB: 4.2, Date: time.Now()}},
			"AutoOn":   true, "Retention": "7", "Saved": "Backup created.",
			"Current": "v1.0.0", "Latest": "v1.1.0", "UpdateAvail": true,
			"CheckedAt": "2026-07-22", "Repo": "AymericChaverot/quasar",
			"Host": vps.HostInfo{OS: "Ubuntu 24.04", Kernel: "6.8.0", Arch: "x86_64", Uptime: "3d 4h"},
			"Hardware": vps.Hardware{
				CPUModel: "Intel Xeon E5-2686 v4", CPUCores: 2, CPUThreads: 4, CPUGHz: 2.3,
				MemTotalGB: 7.8, SwapTotalGB: 2, DiskTotalGB: 78.6,
			},
			"Engine":    docker.EngineInfo{DockerVersion: "29.0.1", APIVersion: "1.44", OSType: "linux/amd64", TraefikImage: "traefik:v3.7"},
			"GoRuntime": "go1.26.5",
			"IsAdmin":   true, "CertsWritable": true,
			"Certs": []CertView{
				{Cert: certs.Cert{Domain: "admin.example.com", Issuer: "R3", NotAfter: time.Now().Add(60 * 24 * time.Hour), DaysLeft: 60, Status: "ok"}, UsedBy: "this dashboard"},
				{Cert: certs.Cert{Domain: "gone.example.com", SANs: []string{"gone.example.com", "www.gone.example.com"},
					Issuer: "R3", NotAfter: time.Now().Add(5 * 24 * time.Hour), DaysLeft: 5, Status: "critical"}},
			},
		}},
		{"git_credentials", map[string]any{
			"Title": "Git credentials", "IsAdmin": true,
			"AnyScope": db.AnyScope, "DefaultUser": db.DefaultGitUsername, "Providers": gitProviders,
			"ScopeOptions": scopeOptions(),
			"Saved":        "Credential saved for github.com/acme.",
			"Credentials": []GitCredentialView{
				// An owner-scoped credential: the branch carrying the "scoped"
				// badge and a scope with a path in it.
				{
					GitCredential: &db.GitCredential{
						ID: 3, Name: "GitHub — work", Scope: "github.com/acme", Hint: "ghp_…1b7c",
						CreatedAt:  time.Now().Add(-7 * 24 * time.Hour),
						LastUsedAt: sql.NullTime{Time: time.Now(), Valid: true},
					},
					Apps:      []AppRef{{ID: "beef0003", Name: "Internal API", Host: "github.com/acme/api"}},
					SampleURL: "https://github.com/acme/api.git",
				},
				{
					GitCredential: &db.GitCredential{
						ID: 1, Name: "GitHub — personal", Scope: "github.com", Hint: "ghp_…4f2a",
						CreatedAt:  time.Now().Add(-30 * 24 * time.Hour),
						LastUsedAt: sql.NullTime{Time: time.Now(), Valid: true},
					},
					Apps:      []AppRef{{ID: "abcd1234", Name: "Blog", Host: "github.com/me/blog"}},
					SampleURL: "https://github.com/me/blog.git",
				},
				// The catch-all, never used and holding nothing up: every
				// "empty" branch of the card at once.
				{GitCredential: &db.GitCredential{
					ID: 2, Name: "Imported token", Scope: db.AnyScope, Hint: "glpat-…9c31",
					CreatedAt: time.Now().Add(-90 * 24 * time.Hour),
				}},
			},
			"Uncovered": []AppRef{{ID: "beef0002", Name: "Docs", Host: "codeberg.org/me/docs"}},
		}},
		// A fresh install: no credential stored, nothing cloning from anywhere.
		{"git_credentials", map[string]any{
			"Title": "Git credentials", "IsAdmin": true,
			"AnyScope": db.AnyScope, "DefaultUser": db.DefaultGitUsername, "Providers": gitProviders,
			"ScopeOptions": scopeOptions(),
			"Error":        "Could not reach https://github.com/me/private.git — fatal: Authentication failed",
		}},
		// A freshly installed server: nothing to reclaim, no certificate issued
		// yet, no backup taken. Every section's other branch.
		{"system", map[string]any{
			"Title": "System", "IsAdmin": true,
			"Current": "v1.0.0", "Repo": "AymericChaverot/quasar",
			"Host": vps.HostInfo{OS: "Ubuntu 24.04", Kernel: "6.8.0", Arch: "x86_64"},
			// A machine whose /proc/cpuinfo carries no model name and no clock,
			// with one core and no swap: every "unknown" branch of the hardware
			// cards at once.
			"Hardware":  vps.Hardware{CPUCores: 1, CPUThreads: 1, MemTotalGB: 1.9},
			"Engine":    docker.EngineInfo{DockerVersion: "29.0.1", APIVersion: "1.44"},
			"GoRuntime": "go1.26.5",
			"Disk":      docker.DiskUsage{ImagesCount: 2, ImagesBytes: 180_000_000},
			"Cleanup":   docker.CleanupScan{},
		}},
		// One of everything, to catch a count that only reads well in the plural.
		{"system", map[string]any{
			"Title": "System", "IsAdmin": true,
			"Current": "v1.0.0", "Repo": "AymericChaverot/quasar",
			"Host":      vps.HostInfo{OS: "Ubuntu 24.04", Kernel: "6.8.0", Arch: "x86_64"},
			"Engine":    docker.EngineInfo{DockerVersion: "29.0.1", APIVersion: "1.44"},
			"GoRuntime": "go1.26.5",
			"Disk":      docker.DiskUsage{ImagesCount: 2, ImagesBytes: 180_000_000},
			"Cleanup": docker.CleanupScan{
				Items:   []docker.Reclaimable{{Key: "dangling", Label: "Untagged layers left by rebuilds", Count: 1, Bytes: 41_000_000}},
				Volumes: docker.Reclaimable{Key: "volumes", Count: 1, Bytes: 12_000_000, Note: "stray"},
				Count:   1, Bytes: 41_000_000,
			},
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
		// Both build badges, plus the rows that carry none: an image app and a
		// git app whose repository is not on disk yet.
		{"apps_table", []AppView{app, gitApp, {
			App:    &db.App{ID: "beef0003", Name: "Docs", Subdomain: "docs", DeployType: "git"},
			Status: docker.AppStatus{State: "running"},
			Domain: "example.com",
			Build:  docker.GitBuild{Mode: "dockerfile", HasDockerfile: true},
		}, {
			App:    &db.App{ID: "beef0004", Name: "New", Subdomain: "new", DeployType: "git"},
			Status: docker.AppStatus{State: "stopped"},
			Domain: "example.com",
		}, {
			App:    &db.App{ID: "ff00ff00", Name: "Site", Subdomain: "@", DeployType: "image", ImageRef: "nginx", Port: 80},
			Status: docker.AppStatus{State: "running"},
			Domain: "example.com",
		}}},
		{"apps_table", []AppView(nil)},
		{"app_status_panel", app},
		{"app_status_panel", gitApp},
		{"git_build_panel", gitApp},
		// The two states with nothing to choose between: a checkout offering
		// one way to build, and no checkout at all.
		{"git_build_panel", func() AppView {
			v := gitApp
			v.App = &db.App{ID: "beef0002", Name: "Worker", Subdomain: "worker",
				DeployType: "git", GitBuild: "compose"}
			v.Build = docker.GitBuild{Mode: "dockerfile", Choice: "compose", HasDockerfile: true}
			return v
		}()},
		{"git_build_panel", func() AppView {
			v := gitApp
			v.Build = docker.GitBuild{}
			return v
		}()},
		// Every state the routing panel has: rewritten by Quasar, left to its
		// author, undecidable, routed at a service the file has lost, and no
		// compose file on disk at all.
		{"compose_route_panel", composeRouteView(gitApp, docker.ComposeAdaptation{
			Services: []string{"nginx", "backend"}, Service: "nginx", Port: 80,
			Unpublished: []string{"nginx 80:80"}, Published: []string{"backend 8080:8080"},
		})},
		{"compose_route_panel", composeRouteView(gitApp, docker.ComposeAdaptation{
			Services: []string{"nginx", "backend"}, Service: "nginx", Choice: "nginx", Port: 8080,
		})},
		{"compose_route_panel", composeRouteView(gitApp, docker.ComposeAdaptation{
			Services: []string{"web", "db"}, Service: "web", Author: true,
		})},
		{"compose_route_panel", composeRouteView(gitApp, docker.ComposeAdaptation{
			Services: []string{"api", "worker"}, Ambiguous: true,
		})},
		{"compose_route_panel", composeRouteView(gitApp, docker.ComposeAdaptation{
			Services: []string{"api", "worker"}, Choice: "renamed", Gone: true,
		})},
		{"compose_route_panel", composeRouteView(gitApp, docker.ComposeAdaptation{})},
		{"compose_route_panel", composeRouteView(gitApp, docker.ComposeAdaptation{Err: "yaml: line 3: mapping values are not allowed"})},
		{"app_containers", map[string]any{"AppID": "beef0001", "Containers": stackContainers}},
		{"app_containers", map[string]any{"AppID": "beef0001", "Containers": []docker.AppContainer(nil)}},
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
		{"log_pane", "/apps/abcd1234/logs"},
		{"tls_status", TLSView{
			AppID: "abcd1234", Missing: true, IsAdmin: true,
			Checks: []certs.HostCheck{
				{Host: "blog.example.com", HasCert: true, DaysLeft: 60},
				{Host: "example.com", Problem: "example.com does not resolve."},
			},
			Route: docker.RouteInfo{
				HasContainer: true, Enabled: true,
				Rules:        []string{"Host(`blog.example.com`)"},
				Port:         "8080",
				CertResolver: "letsencrypt",
				Networks:     []string{"traefik-net"},
				OnTraefikNet: true,
			},
			TraefikNet: "traefik-net",
		}},
		// An app Traefik knows nothing about: the branch that explains a
		// default certificate.
		{"tls_status", TLSView{
			Checks:       []certs.HostCheck{{Host: "example.com"}},
			Route:        docker.RouteInfo{},
			RouteProblem: "This application has no container.",
			TraefikNet:   "traefik-net",
		}},
	}
	host := s.pages["dashboard"]
	for _, p := range partials {
		if err := host.ExecuteTemplate(io.Discard, p.name, p.data); err != nil {
			t.Errorf("execute partial %s: %v", p.name, err)
		}
	}
}
