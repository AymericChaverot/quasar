package catalog

var docmost = Template{
	ID: "docmost", Name: "Docmost", Description: "Collaborative documentation workspace",
	Category: "Notes & docs", DeployType: "compose", Port: 3000, ComposeService: "docmost",
	Env:  "APP_URL={{URL}}\nAPP_SECRET={{RANDOM}}{{RANDOM}}\nDB_PASSWORD={{RANDOM}}",
	Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	Compose: `services:
  docmost:
    image: docmost/docmost:latest
    environment:
      APP_URL: ${APP_URL}
      APP_SECRET: ${APP_SECRET}
      DATABASE_URL: postgresql://docmost:${DB_PASSWORD}@db:5432/docmost?schema=public
      REDIS_URL: redis://redis:6379
    volumes:
      - ./data/storage:/app/data/storage
    depends_on:
      db:
        condition: service_healthy
      redis:
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
      POSTGRES_USER: docmost
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: docmost
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped
`,
}
