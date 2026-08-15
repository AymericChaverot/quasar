package catalog

var immich = Template{
	ID: "immich", Name: "Immich", Description: "Photo and video library for phones",
	Category: "Media", DeployType: "compose", Port: 2283, ComposeService: "immich-server",
	Env: "DB_PASSWORD={{RANDOM}}",
	Compose: `services:
  immich-server:
    image: ghcr.io/immich-app/immich-server:release
    volumes:
      - ./data/upload:/data
    environment:
      DB_HOSTNAME: database
      DB_USERNAME: postgres
      DB_PASSWORD: ${DB_PASSWORD}
      DB_DATABASE_NAME: immich
      REDIS_HOSTNAME: redis
    depends_on:
      redis:
        condition: service_healthy
      database:
        condition: service_healthy
    restart: unless-stopped

  immich-machine-learning:
    image: ghcr.io/immich-app/immich-machine-learning:release
    volumes:
      - ./data/model-cache:/cache
    restart: unless-stopped

  redis:
    image: redis:7-alpine
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 5s
      retries: 30
    restart: unless-stopped

  database:
    image: ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: immich
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
}
