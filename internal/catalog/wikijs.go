package catalog

var wikijs = Template{
	ID: "wikijs", Name: "Wiki.js", Description: "Wiki with a rich editor",
	Category: "Notes & docs", DeployType: "compose", Port: 3000, ComposeService: "wiki",
	Env: "DB_PASSWORD={{RANDOM}}",
	Compose: `services:
  wiki:
    image: ghcr.io/requarks/wiki:2
    environment:
      DB_TYPE: postgres
      DB_HOST: db
      DB_PORT: 5432
      DB_USER: wiki
      DB_PASS: ${DB_PASSWORD}
      DB_NAME: wiki
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
      POSTGRES_USER: wiki
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: wiki
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
}
