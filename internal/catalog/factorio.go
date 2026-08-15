package catalog

var factorio = Template{
	ID: "factorio", Name: "Factorio", Description: "Dedicated Factorio server",
	Category: "Game servers", DeployType: "compose", Port: 34197, ComposeService: "factorio",
	Raw: true, Note: "Port 34197/udp.",
	Compose: `services:
  factorio:
    image: factoriotools/factorio:stable
    ports:
      - "34197:34197/udp"
    environment:
      UPDATE_MODS_ON_START: "false"
    volumes:
      - ./data:/factorio
    restart: unless-stopped
`,
}
