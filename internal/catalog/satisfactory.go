package catalog

var satisfactory = Template{
	ID: "satisfactory", Name: "Satisfactory", Description: "Dedicated Satisfactory server",
	Category: "Game servers", DeployType: "compose", Port: 7777, ComposeService: "satisfactory",
	Raw: true, Note: "Port 7777/udp and 7777/tcp.",
	Compose: `services:
  satisfactory:
    image: wolveix/satisfactory-server:latest
    ports:
      - "7777:7777/udp"
      - "7777:7777/tcp"
    environment:
      MAXPLAYERS: 4
      PGID: 1000
      PUID: 1000
      STEAMBETA: "false"
    volumes:
      - ./data:/config
    restart: unless-stopped
`,
}
