# Quasar

A self-hosted, very small mini-PaaS in Go. From a blank Linux VPS to a working
platform in one command: an admin dashboard on `admin.your-domain.com`, and
containerised applications deployed on subdomains of their own with automatic
TLS (Traefik v3 + Let's Encrypt).

## Deploying on a VPS

### 0. Requirements

- A Linux VPS (Ubuntu/Debian recommended, 1 GB of RAM is enough) with root
  access.
- A domain name whose DNS zone you control.
- This repository published on GitHub (the install script clones it, and the
  release pipeline is what feeds the auto-updater).

### 1. Publish a first release (once)

```bash
git init && git add -A && git commit -m "Initial release"
git remote add origin https://github.com/AymericChaverot/quasar.git
git push -u origin main
git tag v0.1.0 && git push --tags
```

The `release.yml` workflow builds the image and pushes it to
`ghcr.io/aymericchaverot/quasar`. **Important:** GHCR packages are private by
default — make it public (package page → Package settings → Change visibility),
otherwise the VPS cannot pull it and `setup.sh` falls back to building the
image locally (slower, but it works).

### 2. Point the DNS

At your registrar, point the domain and the wildcard at the VPS address:

```
A   your-domain.com     -> VPS IP
A   *.your-domain.com   -> VPS IP
```

Do this before installing: Let's Encrypt can only issue certificates once DNS
already resolves to the server.

**Both records are needed.** The wildcard `*.your-domain.com` covers `admin.`
and every application subdomain, but **not the root domain itself**: an
application published on `@` (the apex) stays without a certificate until
`your-domain.com` has an A record of its own.

**And delete the registrar's default records.** Many zones (OVH in particular)
arrive with an A record on the apex pointing at their shared hosting, plus a
`CNAME www`. If that record sits beside the VPS one, the browser sometimes
lands on the VPS and Let's Encrypt on the registrar's "under construction"
page — issuance fails while everything looks fine. The TLS section of an
application's page reports both cases.

### 3. Open the ports

Only 22 (SSH), 80 and 443 need to be reachable. With ufw, say:

```bash
ufw allow 22/tcp && ufw allow 80/tcp && ufw allow 443/tcp && ufw enable
```

### 4. Run the installer

On the VPS, as root:

```bash
curl -sSL https://raw.githubusercontent.com/AymericChaverot/quasar/main/setup.sh | sudo bash
```

The script installs Docker if needed, asks 4 questions (root domain, Let's
Encrypt email, admin username and password), clones the repository into
`/opt/quasar`, generates the configuration and starts the stack (Traefik +
socket-proxy + dashboard).

### 5. Log in

Open `https://admin.your-domain.com` (the first load can take a few seconds
while the TLS certificate is issued) and sign in with the account created in
step 4. Recommended next: turn on 2FA in Settings, and configure the
notifications webhook.

### Updates

New versions are published by pushing a tag (`git tag v0.2.0 && git push
--tags`). Instances pick them up on their own (checked every 6 h) and install
them in one click from the **System** page — only the dashboard restarts, for a
few seconds; applications are untouched.

## Stack

| Component       | Role |
|-----------------|------|
| Go + HTMX + Tailwind | Admin dashboard (single binary, < 50 MB of RAM) |
| Traefik v3      | Reverse proxy, label-based routing, ACME certificates |
| tecnativa/docker-socket-proxy | Restricted Docker access — the dashboard never mounts the socket |
| SQLite (modernc, CGO-free) | Metadata in `storage/database.sqlite` |
| gopsutil        | CPU / RAM / disk monitoring of the VPS |

## Features

- **3 deployment modes**: Docker image (public or private registry), Git build
  (a repository carrying a `docker-compose.yml` **or** a `Dockerfile`, with a
  token for private ones), or a pasted `docker-compose.yml`.
- **Git: compose detected automatically**: a repository with a compose file at
  its root is deployed as a stack (`docker compose up`) rather than built from
  its `Dockerfile` — a Dockerfile beside it only describes one service of the
  stack. The application's page shows what was detected and lets you switch to
  `Dockerfile` explicitly, for when the compose file is only there for local
  development.
- **Compose adapted automatically**: an ordinary `docker-compose.yml` — the one
  that runs as-is on a laptop, with its own nginx on port 80 — is rewritten by
  Quasar to run behind Traefik: 80/443 host port publications dropped (Traefik
  holds them for the whole server), the front service attached to the
  `traefik-net` network, router labels put on it. The rewrite goes to a
  `docker-compose.quasar.yml` generated beside the original file, on every
  deployment: the repository is never modified. The application's *Routing*
  panel names the service that is routed, its port, and what was changed. A
  file already carrying Traefik labels of its own is left alone.
- **Front-service detection, without guessing**: no service name and no image
  name is used — in order, the service that published host port 80/443,
  otherwise the one the rest of the stack lines up behind (`depends_on` towards
  others, nothing pointing at it), otherwise the only one offering the port
  configured for the application, otherwise the only service in the file. If
  nothing settles it, Quasar changes nothing rather than routing the domain at
  random, and the *Routing* panel lets you choose. YAML anchors and merge keys
  (`&anchor`, `<<: *defaults`, `web: *base`) are flattened before reading and
  writing, or the labels would land on the shared anchor.
- **Automatic routing**: subdomain + internal port → generated Traefik labels,
  TLS certificate issued on the first request. Extra custom domains per
  application (`www.myblog.com`).
- **Redeploy vs Update**: *Redeploy* recreates the container from what is
  already on the server (same image, same commit) — that is what applies a
  configuration change. *Update* fetches the new version first: `git pull` +
  rebuild (image or stack), `docker pull`, or `docker compose pull`.
- **Auto-deploy webhooks**: a secret URL per application — a GitHub/GitLab push
  triggers pull + rebuild + redeploy (same as *Update*).
- **History + rollback**: a deployment log (source, image, duration, result),
  the last 4 built images kept, one-click rollback.
- **Live deploy**: on the application's page, the output of the clone, the
  build and `docker compose` scrolls by while it deploys (SSE), with a progress
  bar per step — pull, build, start, health check. The panel stays afterwards:
  it is where you read why a build failed. Admins only, build output being
  quite willing to print secrets.
- **Resource limits**: max CPU / RAM per application (cgroups).
- **Environment variables**: built-in editor, written to the database and to
  `apps/<id>/.env`.
- **Persistence**: a container path bound to `apps/<id>/data/`.
- **Actions**: start / stop / restart / redeploy / rollback / delete.
- **Monitoring**: CPU/RAM/disk gauges for the VPS, per-container stats, live
  logs (SSE), a background state watcher.
- **Notifications**: Discord/Slack-compatible webhook — failed deploy,
  application in error, recovery, failed backup.
- **Backups**: an archive on demand or daily (a consistent SQLite snapshot plus
  each application's `data/` and `.env`), configurable retention, downloadable
  from the dashboard.
- **Disk maintenance**: Docker usage (images, containers, volumes, build cache)
  and size per application. Cleanup counts the reclaimable space per category
  *before* you click, then removes everything nothing claims any more — images
  with no container, untagged layers, build cache, containers left behind by a
  deployment, empty networks. It spares stopped applications, their images and
  each application's last git builds (rollback targets); orphaned volumes are
  offered separately, behind a checkbox, because those do not come back with a
  download.
- **Storage explorer**: what persisted data actually holds, read-only. Two ways
  in: the *Storage* section of an application's page lists what it has mounted
  (path in the container, volume or bind, rw/ro) — read from the containers, so
  the volumes an image declares for itself show up too; and the *Volumes*
  section of the System page names every volume on the server, with the
  application each belongs to, its size, and a button to open it. It is the
  other half of the cleanup above: before ticking "delete 3 orphaned volumes",
  you can look at what is in them. The explorer walks the tree (a real URL at
  every folder, so it is shareable and the Back button works), previews text
  files (256 KB max) and images, and downloads any file. Admins only: these
  files hold whatever the application wrote, secrets included.

  **Writing**: uploading files into the open folder, editing a text file in a
  textarea, and deleting (with a confirmation). All three are recorded in the
  audit log (`storage.upload`, `storage.edit`, `storage.delete`). Writes are
  atomic — temporary file then rename — so an application re-reading its
  configuration never sees it half-written, and an interrupted write leaves the
  previous version intact. A replaced file keeps its permissions: a secret at
  `600` does not come back at `644` because it was edited.

  **Where writing works**, and why the interface shows it. Application data
  (`/opt/quasar/apps`) is mounted read-write in the dashboard: editable
  everywhere, with nothing to configure. A named volume comes through the
  host's `:ro` mount, so the kernel refuses the write — the dashboard detects
  it at the start of every navigation (via `access(2)`, never by assuming) and
  shows neither upload, nor editing, nor deletion, with a *read-only* chip at
  the top of the page. To make volumes editable, uncomment the
  `/var/lib/docker/volumes` line in `docker-compose.yml` and run `docker
  compose up -d` on the VPS — the auto-updater only replaces the image, not the
  compose file. A mount the container itself holds `ro` stays read-only
  whatever happens: the application's compose file said no.

  Two guardrails worth knowing about. A file too large to be shown in full
  (> 256 KB) is **not** editable: saving it would write the displayed 256 KB
  over the whole file and lose the rest — that is refused server-side, not just
  hidden. And a volume on a network driver (NFS, EBS…) cannot be opened at all:
  its data is not on this machine's disk, and the row says so.
- **Git credentials**: a page of their own (Settings → Git credentials). Each
  token declares its *scope* — a forge (`github.com`), an organisation
  (`github.com/acme`), one precise repository, or `*` as a fallback — and the
  narrowest scope wins. A personal GitHub account and a work organisation can
  therefore each have their own token, and neither is ever offered to the other
  (the comparison is segment by segment: `github.com/acme` never takes
  `github.com/acmecorp`). Tokens are encrypted with the master key, never shown
  again (only a masked hint), testable in one click (a real `git ls-remote`),
  with the list of applications depending on each. Links and exact scopes are
  given per forge. The single token of earlier versions is migrated to `*`
  automatically.
- **TLS per application**: the certificate state of every hostname served, and
  a diagnosis when it is missing (a name that does not resolve, or that points
  somewhere other than this server — the usual reason an application has no
  HTTPS).
- **Certificates**: the certificates Traefik holds, with the application
  routing each; deleting the ones nothing routes any more (Traefik restarts, a
  few seconds of downtime).
- **Themes**: Nebula (dark, the default), Marathon (orange/bone brutalism,
  after Bungie's Marathon), Nord, Synthwave, Terminal (CRT green), Paper and
  Solarized (light) — CSS variables, kept in a cookie.
- **One-click catalogue**: ~60 self-hosted services by category (media, files,
  notes, dashboards, security, development, analytics, databases, game
  servers…), with search. An entry is either a single image or a whole Compose
  stack — Immich, Nextcloud, Authentik and Paperless arrive with their database
  and their cache. The form is prefilled and secrets are generated.
  The public address an entry needs (`URL`, `BASE_URL`, `url`…) is worked out
  from the subdomain and the domain: nothing to copy by hand. Stacks wait for
  their database through `healthcheck` + `depends_on: condition`, so there is
  no restart loop on the first deployment.
  Game servers and databases do not speak HTTP: Traefik holds only :80 and :443
  and routes on the Host header, so those applications publish a port of their
  own and are reached at the server's IP, not at the subdomain. Dedicated
  TCP/UDP entrypoints in Traefik would make them routable like the rest — an
  open lead, not done yet.

  The catalogue can be checked, not just re-read:

  ```sh
  # Every image still exists in its registry (network only, no Docker).
  CATALOG_IMAGES=1 go test ./internal/catalog/ -run TestEveryImageStillExists

  # Every entry is actually deployed and probed (needs Docker).
  CATALOG_DEPLOY=1 go test ./internal/catalog/ -run TestDeploy -parallel 4 -timeout 3h
  CATALOG_DEPLOY=1 CATALOG_ONLY=immich,outline go test ./internal/catalog/ -run TestDeploy -v
  ```
- **Catalogues of your own**: *Settings → Catalogues*. A catalogue is a YAML
  document — written in the interface, pasted, or imported from a URL — that
  adds your entries and your categories to the ones shipped. The categories
  appear as filter buttons above the catalogue. An entry reusing a shipped `id`
  **replaces** that card instead of sitting beside it. Nothing refreshes on its
  own: a catalogue describes the compose files this server will run, so
  re-importing is a button, never a background job. A document that does not
  pass the checks is not saved, and the page renders the list of what is wrong
  with it in the words it came in.

  Entries can also be edited one at a time in a form, without writing YAML —
  the same document from both sides, so starting in one and finishing in the
  other is fine.

  Ready-made ones live in [`catalogs/`](catalogs/): game servers, dev stacks
  and a couple of web applications, each one there to show a different part of
  the format. Paste one, or import it by URL and keep the re-fetch button.
- **Parameterised entries**: an entry may ask questions before it prefills the
  form — a version, a mod loader, an amount of RAM, a port. That is what makes
  an entry cover a fleet rather than one installation: a single Minecraft for
  vanilla and modded, at whatever version, as many times as you like.

  Every answer is substituted for the entry's `{{VERSION}}` — in its env, its
  image, its compose file, and in the name and subdomain proposed, so that the
  second server lands neither on the first one's address nor under its name.
  Not to be confused with `${VERSION}`, which Quasar leaves exactly as it is:
  that one is docker compose reading the `.env` at run time.

  ```yaml
  name: My servers
  categories: [Minecraft]
  entries:
    - id: minecraft-modded
      name: Modded Minecraft
      description: Fabric or Forge server, version of your choosing
      category: Minecraft
      deploy_type: compose
      compose_service: minecraft
      port: 25565
      raw: true
      app_name: "Minecraft {{VERSION}} ({{TYPE}})"
      subdomain: "mc-{{TYPE}}-{{VERSION}}"
      params:
        - {name: TYPE, label: Mod loader, kind: select, default: FABRIC,
           options: [FABRIC, FORGE, NEOFORGE]}
        - {name: VERSION, label: Version, default: "1.20.1"}
        - {name: HOST_PORT, label: Port, kind: port, default: "25566"}
      env: |
        TYPE={{TYPE}}
        MINECRAFT_VERSION={{VERSION}}
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
            volumes:
              - ./data:/data
            restart: unless-stopped
  ```

  The example shown on the page is this one, and a test parses and validates
  it — documentation that would be rejected on paste is worth less than no
  documentation. The files in `catalogs/` are held to the same test for the
  same reason. Two raw servers on the same host port are refused at creation
  time, naming the application that holds it — otherwise the second stack
  starts, fails to bind and stops, in a log nobody reads.
- **Tasks**: commands run inside the container (`docker exec`), on demand or
  scheduled (every N minutes), with output and status kept.
- **Web terminal**: an interactive shell in the container (xterm.js +
  WebSocket).
- **Password protection**: Traefik basic auth, per application.
- **HTTP healthchecks**: a periodic probe, automatic restart after 3 failures,
  an availability history.
- **Backup restore**: one click from the System page (SQLite tables via ATTACH,
  `data/` and `.env` put back — redeploy afterwards).
- **2FA (TOTP)**: an activation QR code, the code required at login.
- **Metrics history**: CPU/RAM samples (server-wide and per application) in
  SQLite, 24 h SVG sparklines rendered server-side.
- **Auto-update**: GitHub releases checked (every 30 min), a button in the top
  bar as soon as a version is available, one-click update through an ephemeral
  updater container — only a few seconds of dashboard downtime, applications
  untouched.
- **Traefik update**: from the System page, to the version the current Quasar
  release was tested with (never the latest on Docker Hub — Traefik is the
  piece that takes every site down with it if it fails to start). The pin is
  written to `docker-compose.override.yml`, so it survives a `docker compose up
  -d` run by hand and leaves the git repository clean. If the new version does
  not stay up, the old one is put back automatically.

## Layout on the VPS

```
/opt/quasar/
├── setup.sh
├── docker-compose.yml       # Traefik + socket-proxy + dashboard
├── docker-compose.override.yml  # Traefik version pinned by the dashboard
│                            # (absent until an update has been done;
│                            #  deleting it returns to the docker-compose.yml pin)
├── .env                     # Global secrets (chmod 600)
├── traefik/
│   ├── traefik.yml          # Static config (generated by setup.sh)
│   └── acme.json            # Certificates (chmod 600)
├── storage/
│   └── database.sqlite
├── backups/                 # quasar-<date>.tar.gz archives
└── apps/<app-id>/
    ├── source/              # Git clone (build mode) — its compose file, if any,
    │   │                    #   is run from here
    │   └── docker-compose.quasar.yml  # Generated: the repo's compose, adapted
    ├── docker-compose.yml   # Pasted-compose mode
    ├── docker-compose.quasar.yml      # Generated: the same, for a pasted compose
    ├── .env                 # Passed as --env-file to compose stacks
    └── data/                # Persistent volumes
```

## Local development

```bash
cp .env.example .env       # dev config (gitignored), adjust as needed
docker network create traefik-net
go run ./cmd/server
```

The binary loads `.env` from the current directory at startup (real environment
variables still win; `ENV_FILE` points it elsewhere). Dashboard on
`http://localhost:8080`, credentials from the `.env`. `COOKIE_SECURE=false`
allows the session cookie over local HTTP — never in production. The database
and the applications go to `.dev/` (gitignored).

The admin account is only created on the first start, if the `users` table is
empty; in production, `setup.sh` then removes the password from `.env`.

```bash
go build ./...
go test ./...
```

## CI/CD & releases

- `ci.yml`: build + vet + test + Docker build on every push/PR.
- `release.yml`: a `vX.Y.Z` tag publishes the image to GHCR
  (`ghcr.io/<owner>/quasar:vX.Y.Z` + `latest`, version injected via ldflags)
  and creates the GitHub Release instances watch for.

## Security notes

- The dashboard talks to Docker only through the socket-proxy (a limited set of
  API sections: containers, images, networks, build, session, grpc, volumes,
  info, system, exec + POST). `EXEC=1` is required by the web terminal and by
  tasks — drop it if you do not use those. `SESSION=1` / `GRPC=1` give access
  to the daemon's BuildKit: without them, `docker compose build` starts a
  **privileged** BuildKit container per build instead.
- Sessions are HTTP-only, Secure, SameSite=Lax; passwords are bcrypt.
- The host's `/` is mounted **read-only** in the dashboard (`HOST_ROOT`): disk
  metrics, the ACME store, and Docker volume contents for the storage explorer.
  That explorer never leaves the root it was given — the URL path is normalised
  *before* being joined (so a `..` has nothing to climb), then resolved through
  its symbolic links and checked to be a descendant of that root. It is the
  second half that counts: the files in these trees are written by application
  containers, which are perfectly able to drop a link to `/` in them. Files are
  served with `Content-Disposition: attachment` + `nosniff`, except for a
  closed list of image types (SVG excluded, which would run its script in the
  dashboard's origin). Admin-only routes.
- Writing into volumes: an uploaded file's name is reduced to its last
  component before use, and the write goes through a temporary file renamed
  over the target — which replaces a symbolic link instead of writing through
  it. A target that is not a regular file is refused. Volumes are only editable
  if the operator has explicitly mounted `/var/lib/docker/volumes` writable
  (commented out by default).
- Compose mode (pasted, or detected in a Git repository): Quasar rewrites the
  file to put the Traefik labels on a single service, the one serving the site.
  Ports the stack publishes on the host other than 80/443 are **kept** — a
  stack may want to expose a database or a game server — but they bypass
  Traefik, and so TLS and the protections configured for the application; the
  *Routing* panel points them out. A file already carrying `traefik.*` labels
  is run as it is, with no rewrite.
