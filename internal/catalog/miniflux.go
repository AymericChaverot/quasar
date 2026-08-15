package catalog

var miniflux = Template{
	ID: "miniflux", Name: "Miniflux", Description: "Minimalist feed reader",
	Category: "Reading & RSS", DeployType: "compose", Port: 8080, ComposeService: "miniflux",
	Env:  "DB_PASSWORD={{RANDOM}}\nADMIN_USERNAME=admin\nADMIN_PASSWORD={{RANDOM}}",
	Note: "The first account is created from ADMIN_USERNAME and ADMIN_PASSWORD below.",
	Compose: `services:
  miniflux:
    image: miniflux/miniflux:latest
    environment:
      DATABASE_URL: postgres://miniflux:${DB_PASSWORD}@db/miniflux?sslmode=disable
      RUN_MIGRATIONS: "1"
      CREATE_ADMIN: "1"
      ADMIN_USERNAME: ${ADMIN_USERNAME}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
    depends_on:
      db:
        condition: service_healthy
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: miniflux
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: miniflux
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
}
