package catalog

var karakeep = Template{
	ID: "karakeep", Name: "Karakeep", Description: "Bookmark everything, searchable",
	Category: "Reading & RSS", DeployType: "compose", Port: 3000, ComposeService: "web",
	Env:  "NEXTAUTH_URL={{URL}}\nNEXTAUTH_SECRET={{RANDOM}}{{RANDOM}}\nMEILI_MASTER_KEY={{RANDOM}}{{RANDOM}}",
	Note: "The public address in the env below was filled in from the subdomain; change it there too if you change the subdomain.",
	Compose: `services:
  web:
    image: ghcr.io/karakeep-app/karakeep:release
    environment:
      DATA_DIR: /data
      NEXTAUTH_URL: ${NEXTAUTH_URL}
      NEXTAUTH_SECRET: ${NEXTAUTH_SECRET}
      MEILI_ADDR: http://meilisearch:7700
      MEILI_MASTER_KEY: ${MEILI_MASTER_KEY}
      BROWSER_WEB_URL: http://chrome:9222
    volumes:
      - ./data/karakeep:/data
    depends_on:
      meilisearch:
        condition: service_healthy
      chrome:
        condition: service_started
    restart: unless-stopped

  chrome:
    # Docker Hub, not the gcr.io mirror the upstream sample still names: that
    # project is gone and its registry answers 401 to an anonymous pull.
    image: zenika/alpine-chrome:123
    command:
      - --no-sandbox
      - --disable-gpu
      - --remote-debugging-address=0.0.0.0
      - --remote-debugging-port=9222
      - --hide-scrollbars
    restart: unless-stopped

  meilisearch:
    image: getmeili/meilisearch:v1.13.3
    healthcheck:
      test: ["CMD-SHELL", "curl -fsS http://127.0.0.1:7700/health || exit 1"]
      interval: 5s
      timeout: 5s
      retries: 30
    environment:
      MEILI_NO_ANALYTICS: "true"
      MEILI_MASTER_KEY: ${MEILI_MASTER_KEY}
    volumes:
      - ./data/meilisearch:/meili_data
    restart: unless-stopped
`,
}
