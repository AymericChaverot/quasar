package catalog

var terraria = Template{
	ID: "terraria", Name: "Terraria", Description: "Dedicated Terraria world",
	Category: "Game servers", DeployType: "compose", Port: 7777, ComposeService: "terraria",
	Env: "WORLD_SIZE=2\nWORLD_NAME=Quasar\nDIFFICULTY=1\nMAX_PLAYERS=8",
	Raw: true, Note: "Port 7777/tcp.",
	// No WORLD_FILENAME: naming a world file puts the image on the branch
	// that expects it to exist already, and it restart-loops on a fresh
	// install complaining that -autocreate was not set. WORLDNAME with
	// AUTOCREATE is the branch that generates one.
	Compose: `services:
  terraria:
    image: ryshe/terraria:latest
    ports:
      - "7777:7777"
    environment:
      AUTOCREATE: ${WORLD_SIZE}
      WORLDNAME: ${WORLD_NAME}
      DIFFICULTY: ${DIFFICULTY}
      MAXPLAYERS: ${MAX_PLAYERS}
    volumes:
      - ./data:/root/.local/share/Terraria/Worlds
    stdin_open: true
    tty: true
    restart: unless-stopped
`,
}
