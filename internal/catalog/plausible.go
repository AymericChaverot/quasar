package catalog

var plausible = Template{
	ID: "plausible", Name: "Plausible", Description: "Web analytics, community edition",
	Category: "Analytics", DeployType: "compose", Port: 8000, ComposeService: "plausible",
	Env:  "BASE_URL={{URL}}\nSECRET_KEY_BASE={{RANDOM}}{{RANDOM}}{{RANDOM}}{{RANDOM}}\nDB_PASSWORD={{RANDOM}}",
	Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	Compose: `services:
  plausible:
    image: ghcr.io/plausible/community-edition:v3
    command: sh -c "/entrypoint.sh db createdb && /entrypoint.sh db migrate && /entrypoint.sh run"
    environment:
      BASE_URL: ${BASE_URL}
      SECRET_KEY_BASE: ${SECRET_KEY_BASE}
      DATABASE_URL: postgres://plausible:${DB_PASSWORD}@plausible-db:5432/plausible
      CLICKHOUSE_DATABASE_URL: http://plausible-events-db:8123/plausible_events_db
    depends_on:
      plausible-db:
        condition: service_healthy
      plausible-events-db:
        condition: service_healthy
    restart: unless-stopped

  plausible-db:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: plausible
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: plausible
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped

  plausible-events-db:
    image: clickhouse/clickhouse-server:latest
    healthcheck:
      test: ["CMD-SHELL", "wget -qO- http://127.0.0.1:8123/ping || exit 1"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      CLICKHOUSE_SKIP_USER_SETUP: "1"
    volumes:
      - ./data/clickhouse:/var/lib/clickhouse
    ulimits:
      nofile:
        soft: 262144
        hard: 262144
    restart: unless-stopped
`,
}
