package catalog

var outline = Template{
	ID: "outline", Name: "Outline", Description: "Team wiki and knowledge base",
	Category: "Notes & docs", DeployType: "compose", Port: 3000, ComposeService: "outline",
	Env: "# Outline refuses to start until URL matches the address you serve it on.\n" +
		"URL={{URL}}\nSECRET_KEY={{RANDOM}}{{RANDOM}}\nUTILS_SECRET={{RANDOM}}{{RANDOM}}\n" +
		"DB_PASSWORD={{RANDOM}}",
	Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	Compose: `services:
  outline:
    image: outlinewiki/outline:latest
    environment:
      URL: ${URL}
      SECRET_KEY: ${SECRET_KEY}
      UTILS_SECRET: ${UTILS_SECRET}
      DATABASE_URL: postgres://outline:${DB_PASSWORD}@postgres:5432/outline
      REDIS_URL: redis://redis:6379
      FILE_STORAGE: local
      FILE_STORAGE_LOCAL_ROOT_DIR: /var/lib/outline/data
      PGSSLMODE: disable
    volumes:
      - ./data/storage:/var/lib/outline/data
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  postgres:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: outline
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: outline
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
