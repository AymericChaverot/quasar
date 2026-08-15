package catalog

var woodpecker = Template{
	ID: "woodpecker", Name: "Woodpecker CI", Description: "Container-native CI server",
	Category: "Development", DeployType: "compose", Port: 8000, ComposeService: "woodpecker-server",
	Env: "WOODPECKER_HOST={{URL}}\nWOODPECKER_AGENT_SECRET={{RANDOM}}{{RANDOM}}\n" +
		"# Fill in from an OAuth app on your forge:\nWOODPECKER_GITEA_URL=https://CHANGE-ME\n" +
		"WOODPECKER_GITEA_CLIENT=\nWOODPECKER_GITEA_SECRET=",
	NeedsSetup: "Woodpecker authenticates against your Git forge and restart-loops until it can. " +
		"Create an OAuth application on your Gitea or Forgejo instance and fill the WOODPECKER_GITEA_* values below before deploying.",
	Compose: `services:
  woodpecker-server:
    image: woodpeckerci/woodpecker-server:latest
    environment:
      WOODPECKER_OPEN: "true"
      WOODPECKER_HOST: ${WOODPECKER_HOST}
      WOODPECKER_GITEA: "true"
      WOODPECKER_GITEA_URL: ${WOODPECKER_GITEA_URL}
      WOODPECKER_GITEA_CLIENT: ${WOODPECKER_GITEA_CLIENT}
      WOODPECKER_GITEA_SECRET: ${WOODPECKER_GITEA_SECRET}
      WOODPECKER_AGENT_SECRET: ${WOODPECKER_AGENT_SECRET}
    volumes:
      - ./data/server:/var/lib/woodpecker
    restart: unless-stopped

  woodpecker-agent:
    image: woodpeckerci/woodpecker-agent:latest
    command: agent
    environment:
      WOODPECKER_SERVER: woodpecker-server:9000
      WOODPECKER_AGENT_SECRET: ${WOODPECKER_AGENT_SECRET}
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    depends_on:
      woodpecker-server:
        condition: service_started
    restart: unless-stopped
`,
}
