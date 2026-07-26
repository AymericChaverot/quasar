# Quasar

Mini-PaaS auto-hébergé, ultra-léger, en Go. D'un VPS Linux vierge à une
plateforme opérationnelle en une commande : dashboard admin sur
`admin.votre-domaine.com`, déploiement d'apps conteneurisées sur des
sous-domaines dédiés avec TLS automatique (Traefik v3 + Let's Encrypt).

## Déploiement sur un VPS

### 0. Prérequis

- Un VPS Linux (Ubuntu/Debian recommandé, 1 Go de RAM suffit) avec accès root.
- Un nom de domaine dont vous contrôlez la zone DNS.
- Ce dépôt publié sur GitHub (le script d'installation le clone, et la
  pipeline de release alimente l'auto-updater).

### 1. Publier une première release (une fois)

```bash
git init && git add -A && git commit -m "Initial release"
git remote add origin https://github.com/AymericChaverot/quasar.git
git push -u origin main
git tag v0.1.0 && git push --tags
```

Le workflow `release.yml` construit l'image et la pousse sur
`ghcr.io/aymericchaverot/quasar`. **Important :** les packages GHCR sont
privés par défaut — rendez-le public (page du package → Package settings →
Change visibility), sinon le VPS ne pourra pas le puller et `setup.sh`
construira l'image localement (plus lent, mais fonctionnel).

### 2. Configurer le DNS

Chez votre registrar, pointez le domaine et le wildcard vers l'IP du VPS :

```
A   votre-domaine.com     -> IP du VPS
A   *.votre-domaine.com   -> IP du VPS
```

Faites-le avant l'installation : Let's Encrypt ne peut émettre les
certificats que si le DNS résout déjà vers le serveur.

**Les deux enregistrements sont nécessaires.** Le wildcard `*.votre-domaine.com`
couvre `admin.` et tous les sous-domaines d'apps, mais **pas le domaine racine
lui-même** : une app publiée sur `@` (l'apex) reste sans certificat tant que
`votre-domaine.com` n'a pas son propre enregistrement A.

**Et supprimez les enregistrements par défaut du registrar.** Beaucoup de zones
(OVH notamment) arrivent avec un A sur l'apex pointant vers leur hébergement
mutualisé, plus un `CNAME www`. Si cet enregistrement cohabite avec celui du
VPS, le navigateur tombe parfois sur le VPS et Let's Encrypt sur la page
« Site en construction » du registrar — l'émission échoue alors que tout a
l'air de marcher. La section TLS de la page d'une application signale les deux
cas.

### 3. Ouvrir les ports

Seuls 22 (SSH), 80 et 443 doivent être accessibles. Par exemple avec ufw :

```bash
ufw allow 22/tcp && ufw allow 80/tcp && ufw allow 443/tcp && ufw enable
```

### 4. Lancer l'installation

Sur le VPS, en root :

```bash
curl -sSL https://raw.githubusercontent.com/AymericChaverot/quasar/main/setup.sh | sudo bash
```

Le script installe Docker si nécessaire, pose 4 questions (domaine racine,
email Let's Encrypt, identifiant et mot de passe admin), clone le dépôt dans
`/opt/quasar`, génère la configuration et démarre la stack (Traefik +
socket-proxy + dashboard).

### 5. Se connecter

Ouvrez `https://admin.votre-domaine.com` (le premier chargement peut prendre
quelques secondes, le temps de l'émission du certificat TLS) et connectez-vous
avec le compte créé à l'étape 4. Recommandé ensuite : activer le 2FA dans
Settings, et configurer le webhook de notifications.

### Mises à jour

Les nouvelles versions se publient en poussant un tag (`git tag v0.2.0 &&
git push --tags`). Les instances les détectent automatiquement (vérification
toutes les 6 h) et s'installent en un clic depuis la page **System** — seul le
dashboard redémarre quelques secondes, les applications ne sont pas touchées.

## Stack

| Composant       | Rôle |
|-----------------|------|
| Go + HTMX + Tailwind | Dashboard admin (binaire unique, < 50 Mo RAM) |
| Traefik v3      | Reverse proxy, routage par labels, certificats ACME |
| tecnativa/docker-socket-proxy | Accès Docker restreint — le dashboard ne monte jamais le socket |
| SQLite (modernc, sans CGO) | Métadonnées dans `storage/database.sqlite` |
| gopsutil        | Monitoring CPU / RAM / disque du VPS |

## Fonctionnalités

- **3 modes de déploiement** : image Docker (publique ou registre privé),
  build Git (repo avec `Dockerfile`, token pour les repos privés), ou
  `docker-compose.yml` injecté.
- **Routage automatique** : sous-domaine + port interne → labels Traefik
  générés, certificat TLS émis à la première requête. Domaines custom
  additionnels par app (`www.monblog.fr`).
- **Redeploy vs Update** : *Redeploy* recrée le conteneur à partir de ce qui est
  déjà sur le serveur (même image, même commit) — c'est ce qui applique un
  changement de config. *Update* va chercher la nouvelle version d'abord :
  `git pull` + rebuild, `docker pull`, ou `docker compose pull`.
- **Webhooks auto-deploy** : URL secrète par app — un push GitHub/GitLab
  déclenche pull + rebuild + redeploy (comme *Update*).
- **Historique + rollback** : journal des déploiements (source, image, durée,
  résultat), rétention des 4 dernières images buildées, rollback en un clic.
- **Limites ressources** : CPU / RAM max par app (cgroups).
- **Variables d'environnement** : éditeur intégré, écrites en base et dans
  `apps/<id>/.env`.
- **Persistance** : chemin conteneur monté sur `apps/<id>/data/`.
- **Actions** : start / stop / restart / redeploy / rollback / delete.
- **Monitoring** : jauges CPU/RAM/disque du VPS, stats par conteneur, logs en
  direct (SSE), watcher d'état en arrière-plan.
- **Notifications** : webhook Discord/Slack-compatible — échec de deploy,
  app en erreur, récupération, échec de backup.
- **Backups** : archive à la demande ou quotidienne (snapshot SQLite cohérent
  + `data/` + `.env` de chaque app), rétention configurable, téléchargement
  depuis le dashboard.
- **Maintenance disque** : usage Docker (images, conteneurs, volumes), prune
  des images orphelines, taille par app.
- **TLS par app** : état du certificat de chaque hostname servi, et diagnostic
  quand il manque (nom qui ne résout pas, ou qui pointe ailleurs que sur ce
  serveur — la cause habituelle d'une app sans HTTPS).
- **Certificats** : liste des certificats détenus par Traefik avec l'app qui
  route chacun ; suppression de ceux que plus rien ne route (Traefik redémarre,
  quelques secondes d'indisponibilité).
- **Thèmes** : Nebula (sombre, défaut), Marathon (brutalism orange/os, d'après
  Marathon de Bungie), Nord, Synthwave, Terminal (vert CRT), Paper et Solarized
  (clairs) — via variables CSS, persisté en cookie.
- **Catalogue one-click** : PostgreSQL, MySQL, Redis, Uptime Kuma, Ghost, n8n,
  Vaultwarden — formulaire prérempli, secrets générés automatiquement.
- **Tâches** : commandes exécutées dans le conteneur (`docker exec`), à la
  demande ou planifiées (toutes les N minutes), sortie et statut conservés.
- **Terminal web** : shell interactif dans le conteneur (xterm.js + WebSocket).
- **Protection par mot de passe** : basic auth Traefik activable par app.
- **Healthchecks HTTP** : sonde périodique, redémarrage auto après 3 échecs,
  historique de disponibilité.
- **Restauration de backup** : en un clic depuis la page System (tables SQLite
  via ATTACH, data/ et .env remis en place — redéployer ensuite).
- **2FA (TOTP)** : QR code d'activation, code exigé au login.
- **Historique métriques** : échantillons CPU/RAM (serveur et par app) en
  SQLite, sparklines SVG 24h rendues côté serveur.
- **Auto-update** : vérification des releases GitHub (6h), mise à jour en un
  clic via un conteneur updater éphémère — seules quelques secondes
  d'indisponibilité du dashboard, les apps ne sont pas touchées.

## Arborescence sur le VPS

```
/opt/quasar/
├── setup.sh
├── docker-compose.yml       # Traefik + socket-proxy + dashboard
├── .env                     # Secrets globaux (chmod 600)
├── traefik/
│   ├── traefik.yml          # Conf statique (généré par setup.sh)
│   └── acme.json            # Certificats (chmod 600)
├── storage/
│   └── database.sqlite
├── backups/                 # Archives quasar-<date>.tar.gz
└── apps/<app-id>/
    ├── source/              # Clone Git (mode build)
    ├── docker-compose.yml   # Mode compose
    ├── .env
    └── data/                # Volumes persistants
```

## Développement local

```bash
cp .env.example .env       # config de dev (gitignorée), à ajuster au besoin
docker network create traefik-net
go run ./cmd/server
```

Le binaire charge `.env` depuis le répertoire courant au démarrage (les vraies
variables d'environnement restent prioritaires ; `ENV_FILE` permet de pointer
ailleurs). Dashboard sur `http://localhost:8080`, identifiants du `.env`.
`COOKIE_SECURE=false` autorise le cookie de session en HTTP local — jamais en
production. La base et les apps vont dans `.dev/` (gitignoré).

Le compte admin n'est créé qu'au premier démarrage si la table `users` est
vide ; en production, `setup.sh` retire ensuite le mot de passe de `.env`.

```bash
go build ./...
go test ./...
```

## CI/CD & releases

- `ci.yml` : build + vet + test + build Docker sur chaque push/PR.
- `release.yml` : un tag `vX.Y.Z` publie l'image sur GHCR
  (`ghcr.io/<owner>/quasar:vX.Y.Z` + `latest`, version injectée via ldflags)
  et crée la GitHub Release que les instances détectent.

## Notes de sécurité

- Le dashboard parle à Docker uniquement via le socket-proxy (sections API
  limitées : containers, images, networks, build, volumes, info, system,
  exec + POST). `EXEC=1` est requis par le terminal web et les tâches —
  retirez-le si vous n'utilisez pas ces fonctionnalités.
- Sessions HTTP-only, Secure, SameSite=Lax ; mots de passe bcrypt.
- `/` de l'hôte est monté **en lecture seule** dans le dashboard uniquement
  pour les métriques disque (`HOST_ROOT`).
- Mode compose : les services doivent rejoindre le réseau externe
  `traefik-net` et porter leurs propres labels Traefik pour être routés.
