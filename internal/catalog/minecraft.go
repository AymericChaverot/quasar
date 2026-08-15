package catalog

// The entry parameters were built for. Somebody running Minecraft servers is
// rarely running one: a vanilla world on the version their friends are on, a
// modded one on an older release, a test server for the next update. Without
// parameters that is an entry per combination — and the combinations are the
// mod loader times every version ever published.
//
// itzg/minecraft-server is driven entirely by environment, down to downloading
// the server jar the version asks for on first boot, so the whole matrix
// collapses into four questions asked once.
var minecraft = Template{
	ID: "minecraft", Name: "Minecraft (Java)", Description: "Vanilla or modded Java server",
	Category: "Game servers", DeployType: "compose", Port: 25565, ComposeService: "minecraft",

	// One entry, many servers: the name and the address both carry what was
	// picked, so a second server neither collides with the first nor arrives on
	// the dashboard under the same name.
	AppName:   "Minecraft {{VERSION}} ({{TYPE}})",
	Subdomain: "mc-{{TYPE}}-{{VERSION}}",
	Params: []Param{
		{Name: "TYPE", Label: "Server type", Kind: "select", Default: "VANILLA",
			Options: []string{"VANILLA", "PAPER", "PURPUR", "SPIGOT", "FABRIC", "FORGE", "NEOFORGE"},
			Help:    "Vanilla is Mojang's own server. The others accept plugins or mods."},
		{Name: "VERSION", Label: "Minecraft version", Default: "LATEST",
			Help: "A release such as 1.20.1, or LATEST. Mod loaders usually trail the newest release by a while, so pick the version your mods are built for."},
		{Name: "MEMORY", Label: "Memory", Default: "2G",
			Help: "Heap the server may use, e.g. 2G. Modded servers want considerably more than vanilla."},
		{Name: "HOST_PORT", Label: "Port", Kind: "port", Default: "25565",
			Help: "The port players connect to. Give every server on this machine its own; 25565 is the one clients try by default."},
	},
	Env: "TYPE={{TYPE}}\nMINECRAFT_VERSION={{VERSION}}\nMEMORY={{MEMORY}}\nHOST_PORT={{HOST_PORT}}",

	Raw:  true,
	Note: "Players connect on the port below. Running more than one server means giving each a different one.",
	Compose: `services:
  minecraft:
    image: itzg/minecraft-server:latest
    ports:
      - "${HOST_PORT}:25565"
    environment:
      EULA: "TRUE"
      TYPE: ${TYPE}
      VERSION: ${MINECRAFT_VERSION}
      MEMORY: ${MEMORY}
    volumes:
      - ./data:/data
    restart: unless-stopped
`,
}
