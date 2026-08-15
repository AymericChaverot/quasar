package catalog

var valheim = Template{
	ID: "valheim", Name: "Valheim", Description: "Dedicated Valheim world",
	Category: "Game servers", DeployType: "compose", Port: 2456, ComposeService: "valheim",
	Env: "SERVER_NAME=Quasar\nWORLD_NAME=Dedicated\nSERVER_PASS={{RANDOM}}",
	Raw: true, Note: "Ports 2456-2458/udp.",
	Compose: `services:
  valheim:
    image: lloesche/valheim-server:latest
    ports:
      - "2456-2458:2456-2458/udp"
    environment:
      SERVER_NAME: ${SERVER_NAME}
      WORLD_NAME: ${WORLD_NAME}
      SERVER_PASS: ${SERVER_PASS}
      SERVER_PUBLIC: "false"
    volumes:
      - ./data/config:/config
      - ./data/server:/opt/valheim
    restart: unless-stopped
`,
}
