package catalog

var minecraft = Template{
	ID: "minecraft", Name: "Minecraft (Java)", Description: "Vanilla or modded Java server",
	Category: "Game servers", DeployType: "compose", Port: 25565, ComposeService: "minecraft",
	Env: "MINECRAFT_VERSION=LATEST\nMEMORY=2G",
	Raw: true, Note: "Default port 25565/tcp.",
	Compose: `services:
  minecraft:
    image: itzg/minecraft-server:latest
    ports:
      - "25565:25565"
    environment:
      EULA: "TRUE"
      VERSION: ${MINECRAFT_VERSION}
      MEMORY: ${MEMORY}
    volumes:
      - ./data:/data
    restart: unless-stopped
`,
}
