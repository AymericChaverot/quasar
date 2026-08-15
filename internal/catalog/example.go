package catalog

// Example is a catalogue that works, shown on the page where one is written.
// The format is small but nobody guesses it from an empty textarea, and a
// document to edit is a better starting point than a field reference.
//
// It is the Minecraft case on purpose, because that is the case the format
// exists for: one entry, a handful of choices, as many servers as the operator
// wants. A test parses and validates this, so the example on the page is one
// Quasar would accept.
const Example = `# A catalogue of your own. Entries here appear beside the built-in
# ones, and an entry reusing a built-in id replaces it.
name: My catalogue

# Your own categories, in the order the page should show them. They
# appear as filter buttons above the catalogue.
categories:
  - Minecraft

entries:
  - id: minecraft-modded
    name: Modded Minecraft
    description: Forge or Fabric server, version of your choosing
    category: Minecraft
    deploy_type: compose
    compose_service: minecraft
    port: 25565
    raw: true

    # {{VERSION}} and the rest are answered when the entry is picked, and
    # substituted here. The name and the address carry them so that a second
    # server neither collides with the first nor arrives under the same name.
    app_name: "Minecraft {{VERSION}} ({{TYPE}})"
    subdomain: "mc-{{TYPE}}-{{VERSION}}"
    params:
      - name: TYPE
        label: Mod loader
        kind: select
        default: FABRIC
        options: [FABRIC, FORGE, NEOFORGE, QUILT]
      - name: VERSION
        label: Minecraft version
        default: "1.20.1"
        help: Pick the version your mods are built for.
      - name: MEMORY
        label: Memory
        default: 4G
      - name: HOST_PORT
        label: Port
        kind: port
        default: "25566"
        help: Give every server on this machine its own.

    # The env is written beside the compose file and passed with --env-file,
    # so ${TYPE} below is docker compose reading back what {{TYPE}} put here.
    env: |
      TYPE={{TYPE}}
      MINECRAFT_VERSION={{VERSION}}
      MEMORY={{MEMORY}}
      HOST_PORT={{HOST_PORT}}

    compose: |
      services:
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
`
