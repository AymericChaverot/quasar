package catalog

var umami = Template{
	ID: "umami", Name: "Umami", Description: "Privacy-friendly web analytics",
	Category: "Analytics", DeployType: "compose", Port: 3000, ComposeService: "umami",
	Env: "DB_PASSWORD={{RANDOM}}\nAPP_SECRET={{RANDOM}}{{RANDOM}}",
	Compose: `services:
  umami:
    image: ghcr.io/umami-software/umami:postgresql-latest
    environment:
      DATABASE_URL: postgresql://umami:${DB_PASSWORD}@db:5432/umami
      DATABASE_TYPE: postgresql
      APP_SECRET: ${APP_SECRET}
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
      POSTGRES_USER: umami
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: umami
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
}
