package catalog

var planka = Template{
	ID: "planka", Name: "Planka", Description: "Kanban boards",
	Category: "Tasks & projects", DeployType: "compose", Port: 1337, ComposeService: "planka",
	Env: "BASE_URL={{URL}}\nSECRET_KEY={{RANDOM}}{{RANDOM}}\nDB_PASSWORD={{RANDOM}}\n" +
		"ADMIN_EMAIL=admin@example.com\nADMIN_PASSWORD={{RANDOM}}",
	Note: "The first admin is created from ADMIN_EMAIL and ADMIN_PASSWORD below. The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	Compose: `services:
  planka:
    image: ghcr.io/plankanban/planka:latest
    environment:
      BASE_URL: ${BASE_URL}
      SECRET_KEY: ${SECRET_KEY}
      DATABASE_URL: postgresql://planka:${DB_PASSWORD}@db/planka
      DEFAULT_ADMIN_EMAIL: ${ADMIN_EMAIL}
      DEFAULT_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      DEFAULT_ADMIN_NAME: Admin
      DEFAULT_ADMIN_USERNAME: admin
    volumes:
      - ./data/favicons:/app/public/favicons
      - ./data/user-avatars:/app/public/user-avatars
      - ./data/attachments:/app/private/attachments
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
      POSTGRES_USER: planka
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: planka
    volumes:
      - ./data/db:/var/lib/postgresql/data
    restart: unless-stopped
`,
}
