// Package catalog holds the one-click application templates. Selecting one
// prefills the "new application" form; {{RANDOM}} placeholders in env vars
// are replaced with a fresh secret at that moment.
package catalog

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

type Template struct {
	ID          string
	Name        string
	Description string
	ImageRef    string
	Port        int
	DataMount   string
	Env         string // .env content, {{RANDOM}} replaced per selection
}

var Templates = []Template{
	{
		ID: "postgres", Name: "PostgreSQL 16", Description: "Relational database",
		ImageRef: "postgres:16-alpine", Port: 5432, DataMount: "/var/lib/postgresql/data",
		Env: "POSTGRES_USER=app\nPOSTGRES_PASSWORD={{RANDOM}}\nPOSTGRES_DB=app",
	},
	{
		ID: "mysql", Name: "MySQL 8", Description: "Relational database",
		ImageRef: "mysql:8", Port: 3306, DataMount: "/var/lib/mysql",
		Env: "MYSQL_ROOT_PASSWORD={{RANDOM}}\nMYSQL_DATABASE=app\nMYSQL_USER=app\nMYSQL_PASSWORD={{RANDOM}}",
	},
	{
		ID: "redis", Name: "Redis 7", Description: "In-memory data store",
		ImageRef: "redis:7-alpine", Port: 6379, DataMount: "/data",
	},
	{
		ID: "uptime-kuma", Name: "Uptime Kuma", Description: "Uptime monitoring dashboard",
		ImageRef: "louislam/uptime-kuma:1", Port: 3001, DataMount: "/app/data",
	},
	{
		ID: "ghost", Name: "Ghost", Description: "Publishing / blog platform",
		ImageRef: "ghost:5-alpine", Port: 2368, DataMount: "/var/lib/ghost/content",
		Env: "# Set url to your app's public address after choosing the subdomain\nurl=https://CHANGE-ME",
	},
	{
		ID: "n8n", Name: "n8n", Description: "Workflow automation",
		ImageRef: "n8nio/n8n:latest", Port: 5678, DataMount: "/home/node/.n8n",
	},
	{
		ID: "vaultwarden", Name: "Vaultwarden", Description: "Bitwarden-compatible password manager",
		ImageRef: "vaultwarden/server:latest", Port: 80, DataMount: "/data",
	},
}

// Get returns the template with the given ID, or nil.
func Get(id string) *Template {
	for i := range Templates {
		if Templates[i].ID == id {
			return &Templates[i]
		}
	}
	return nil
}

// RenderEnv resolves {{RANDOM}} placeholders with fresh secrets.
func (t *Template) RenderEnv() string {
	env := t.Env
	for strings.Contains(env, "{{RANDOM}}") {
		buf := make([]byte, 16)
		rand.Read(buf)
		env = strings.Replace(env, "{{RANDOM}}", hex.EncodeToString(buf), 1)
	}
	return env
}
