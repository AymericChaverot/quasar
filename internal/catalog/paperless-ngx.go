package catalog

var paperlessNgx = Template{
	ID: "paperless-ngx", Name: "Paperless-ngx", Description: "Scan, index and search documents",
	Category: "Utilities", DeployType: "compose", Port: 8000, ComposeService: "webserver",
	Env:  "DB_PASSWORD={{RANDOM}}\nPAPERLESS_SECRET_KEY={{RANDOM}}{{RANDOM}}\nPAPERLESS_URL={{URL}}",
	Note: "Paperless rejects its own login form as a CSRF failure if PAPERLESS_URL is wrong; it was filled in from the subdomain, so update it if you change that.",
	Compose: `services:
  webserver:
    image: ghcr.io/paperless-ngx/paperless-ngx:latest
    environment:
      PAPERLESS_REDIS: redis://broker:6379
      PAPERLESS_DBHOST: db
      PAPERLESS_DBUSER: paperless
      PAPERLESS_DBPASS: ${DB_PASSWORD}
      PAPERLESS_DBNAME: paperless
      PAPERLESS_SECRET_KEY: ${PAPERLESS_SECRET_KEY}
      PAPERLESS_URL: ${PAPERLESS_URL}
    volumes:
      - ./data/data:/usr/src/paperless/data
      - ./data/media:/usr/src/paperless/media
      - ./data/export:/usr/src/paperless/export
      - ./data/consume:/usr/src/paperless/consume
    depends_on:
      db:
        condition: service_healthy
      broker:
        condition: service_healthy
    restart: unless-stopped

  broker:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped

  db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: paperless
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: paperless
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
}
