package catalog

var palworld = Template{
	ID: "palworld", Name: "Palworld", Description: "Dedicated Palworld server",
	Category: "Game servers", DeployType: "compose", Port: 8211, ComposeService: "palworld",
	Env: "SERVER_NAME=Quasar\nSERVER_PASSWORD={{RANDOM}}\nADMIN_PASSWORD={{RANDOM}}\nPLAYERS=16",
	Raw: true, Note: "Port 8211/udp.",
	Compose: `services:
  palworld:
    image: thijsvanloef/palworld-server-docker:latest
    ports:
      - "8211:8211/udp"
    environment:
      PUID: 1000
      PGID: 1000
      PORT: 8211
      PLAYERS: ${PLAYERS}
      SERVER_NAME: ${SERVER_NAME}
      SERVER_PASSWORD: ${SERVER_PASSWORD}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD}
    volumes:
      - ./data:/palworld
    restart: unless-stopped
`,
}
