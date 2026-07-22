#!/usr/bin/env bash
#
# Quasar — one-command installer for a fresh Linux VPS.
#
#   curl -sSL https://raw.githubusercontent.com/AymericChaverot/quasar/main/setup.sh | bash
#
# Installs Docker if missing, asks for the root domain, ACME email and admin
# credentials, generates the config in /opt/quasar and starts the stack.

set -euo pipefail

REPO_URL="${QUASAR_REPO:-https://github.com/AymericChaverot/quasar.git}"
INSTALL_DIR="/opt/quasar"

bold()  { printf '\033[1m%s\033[0m\n' "$*"; }
info()  { printf '\033[36m[quasar]\033[0m %s\n' "$*"; }
fail()  { printf '\033[31m[quasar] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

# Read from the terminal even when the script is piped into bash.
ask()        { read -r -p "$1" "$2" < /dev/tty; }
ask_secret() { read -r -s -p "$1" "$2" < /dev/tty; printf '\n'; }

[ "$(id -u)" -eq 0 ] || fail "run as root (sudo)."
command -v curl >/dev/null || fail "curl is required."

bold ""
bold "  QUASAR — self-hosted mini PaaS installer"
bold "  ----------------------------------------"

# 1. Docker ------------------------------------------------------------------
if ! command -v docker >/dev/null 2>&1; then
    info "Docker not found, installing via get.docker.com..."
    curl -fsSL https://get.docker.com | sh
    systemctl enable --now docker
else
    info "Docker already installed: $(docker --version)"
fi
docker compose version >/dev/null 2>&1 || fail "Docker Compose plugin missing. Install docker-compose-plugin and re-run."

# 2. Interactive configuration -----------------------------------------------
echo
ask "Root domain (e.g. my-vps.com): " DOMAIN
[ -n "$DOMAIN" ] || fail "domain is required."

ask "Email for Let's Encrypt certificates: " ACME_EMAIL
[ -n "$ACME_EMAIL" ] || fail "email is required."

ask "Admin username: " ADMIN_USER
[ -n "$ADMIN_USER" ] || fail "admin username is required."

while true; do
    ask_secret "Admin password (min 8 chars): " ADMIN_PASSWORD
    [ "${#ADMIN_PASSWORD}" -ge 8 ] || { info "too short, try again."; continue; }
    ask_secret "Confirm password: " ADMIN_PASSWORD2
    [ "$ADMIN_PASSWORD" = "$ADMIN_PASSWORD2" ] && break
    info "passwords do not match, try again."
done

# 3. Fetch sources & directory layout ----------------------------------------
echo
if [ -d "$INSTALL_DIR/.git" ]; then
    info "Updating existing install in $INSTALL_DIR..."
    git -C "$INSTALL_DIR" pull --ff-only
elif [ -f "$INSTALL_DIR/docker-compose.yml" ]; then
    info "Using existing sources in $INSTALL_DIR."
else
    info "Cloning quasar into $INSTALL_DIR..."
    command -v git >/dev/null 2>&1 || { info "installing git..."; (apt-get update -qq && apt-get install -y -qq git) || (dnf install -y git) || (yum install -y git); }
    git clone --depth 1 "$REPO_URL" "$INSTALL_DIR"
fi
cd "$INSTALL_DIR"

info "Creating directory layout..."
mkdir -p storage apps traefik backups
touch traefik/acme.json
chmod 600 traefik/acme.json

# 4. Configuration files ------------------------------------------------------
info "Writing .env..."
cat > .env <<EOF
DOMAIN=${DOMAIN}
ACME_EMAIL=${ACME_EMAIL}
ADMIN_USER=${ADMIN_USER}
ADMIN_PASSWORD=${ADMIN_PASSWORD}
EOF
chmod 600 .env

info "Configuring Traefik (Let's Encrypt: ${ACME_EMAIL})..."
sed -i "s/{{ACME_EMAIL}}/${ACME_EMAIL}/" traefik/traefik.yml

info "Creating Docker network traefik-net..."
docker network inspect traefik-net >/dev/null 2>&1 || docker network create traefik-net

# 5. Start the stack ----------------------------------------------------------
info "Starting the stack (this can take a few minutes)..."
# Prefer the prebuilt image from GHCR; fall back to a local build.
if docker compose pull dashboard 2>/dev/null; then
    docker compose up -d
else
    info "Prebuilt image unavailable, building locally..."
    docker compose up -d --build
fi

# The dashboard creates the SQLite DB and the admin account on first boot;
# once that's done the plaintext password can be dropped from .env.
sleep 5
if docker compose logs dashboard 2>/dev/null | grep -q "created initial admin user"; then
    sed -i '/^ADMIN_PASSWORD=/d' .env
    info "Admin account created; plaintext password removed from .env."
fi

# 6. Success ------------------------------------------------------------------
IP=$(curl -fsS -4 https://ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')
echo
bold "  ----------------------------------------------------------"
bold "  Quasar is up."
echo
echo  "  Dashboard :  https://admin.${DOMAIN}"
echo  "  Login     :  ${ADMIN_USER}"
echo
echo  "  Required DNS records (point them at this VPS):"
echo  "      A     ${DOMAIN}      -> ${IP}"
echo  "      A     *.${DOMAIN}    -> ${IP}   (wildcard for your apps)"
echo
echo  "  TLS certificates are issued automatically on first request"
echo  "  once DNS resolves to this server."
bold "  ----------------------------------------------------------"
