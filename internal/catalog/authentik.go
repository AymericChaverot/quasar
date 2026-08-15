package catalog

var authentik = Template{
	ID: "authentik", Name: "Authentik", Description: "Single sign-on and identity provider",
	Category: "Security", DeployType: "compose", Port: 9000, ComposeService: "server",
	Env: "PG_PASS={{RANDOM}}\nAUTHENTIK_SECRET_KEY={{RANDOM}}{{RANDOM}}",
	Compose: `services:
  server:
    image: ghcr.io/goauthentik/server:latest
    command: server
    environment:
      AUTHENTIK_REDIS__HOST: redis
      AUTHENTIK_POSTGRESQL__HOST: postgresql
      AUTHENTIK_POSTGRESQL__USER: authentik
      AUTHENTIK_POSTGRESQL__NAME: authentik
      AUTHENTIK_POSTGRESQL__PASSWORD: ${PG_PASS}
      AUTHENTIK_SECRET_KEY: ${AUTHENTIK_SECRET_KEY}
    volumes:
      - ./data/media:/media
      - ./data/templates:/templates
    depends_on:
      postgresql:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  worker:
    image: ghcr.io/goauthentik/server:latest
    command: worker
    environment:
      AUTHENTIK_REDIS__HOST: redis
      AUTHENTIK_POSTGRESQL__HOST: postgresql
      AUTHENTIK_POSTGRESQL__USER: authentik
      AUTHENTIK_POSTGRESQL__NAME: authentik
      AUTHENTIK_POSTGRESQL__PASSWORD: ${PG_PASS}
      AUTHENTIK_SECRET_KEY: ${AUTHENTIK_SECRET_KEY}
    volumes:
      - ./data/media:/media
      - ./data/templates:/templates
    depends_on:
      postgresql:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  postgresql:
    image: postgres:16-alpine
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      POSTGRES_USER: authentik
      POSTGRES_PASSWORD: ${PG_PASS}
      POSTGRES_DB: authentik
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
