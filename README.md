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
  build Git (repo avec `docker-compose.yml` **ou** `Dockerfile`, token pour les
  repos privés), ou `docker-compose.yml` injecté.
- **Git : compose détecté automatiquement** : un repo qui porte un fichier
  compose à sa racine est déployé en stack (`docker compose up`), pas construit
  depuis son `Dockerfile` — un Dockerfile à côté ne décrit qu'un service de la
  stack. La page de l'app affiche ce qui a été détecté et permet de basculer
  explicitement sur `Dockerfile` quand le compose n'est là que pour le dev local.
- **Compose adapté automatiquement** : un `docker-compose.yml` ordinaire —
  celui qui tourne tel quel sur un portable, avec son propre nginx sur le port
  80 — est réécrit par Quasar pour tourner derrière Traefik : publications de
  ports 80/443 retirées (Traefik les détient pour tout le serveur), service de
  façade rattaché au réseau `traefik-net`, labels de routeur posés dessus. La
  réécriture va dans un `docker-compose.quasar.yml` généré à côté du fichier
  d'origine, à chaque déploiement : le dépôt n'est jamais modifié. Le panneau
  *Routing* de l'app indique le service routé, son port, et ce qui a été
  changé. Un fichier qui porte déjà ses propres labels Traefik est laissé
  intact.
- **Détection du service de façade, sans deviner** : aucun nom de service ni
  nom d'image n'est utilisé — dans l'ordre, le service qui publiait le port
  80/443 de l'hôte, sinon celui derrière lequel le reste de la stack se range
  (`depends_on` vers d'autres, rien qui pointe vers lui), sinon le seul qui
  offre le port configuré pour l'app, sinon le seul service du fichier. Si rien
  ne tranche, Quasar ne touche à rien plutôt que de router le domaine au
  hasard, et le panneau *Routing* laisse choisir. Ancres et clés de fusion YAML
  (`&anchor`, `<<: *defaults`, `web: *base`) sont aplaties avant lecture et
  écriture, sinon les labels atterriraient sur l'ancre partagée.
- **Routage automatique** : sous-domaine + port interne → labels Traefik
  générés, certificat TLS émis à la première requête. Domaines custom
  additionnels par app (`www.monblog.fr`).
- **Redeploy vs Update** : *Redeploy* recrée le conteneur à partir de ce qui est
  déjà sur le serveur (même image, même commit) — c'est ce qui applique un
  changement de config. *Update* va chercher la nouvelle version d'abord :
  `git pull` + rebuild (image ou stack), `docker pull`, ou `docker compose pull`.
- **Webhooks auto-deploy** : URL secrète par app — un push GitHub/GitLab
  déclenche pull + rebuild + redeploy (comme *Update*).
- **Historique + rollback** : journal des déploiements (source, image, durée,
  résultat), rétention des 4 dernières images buildées, rollback en un clic.
- **Deploy en direct** : sur la page de l'app, la sortie du clone, du build et
  de `docker compose` défile pendant le déploiement (SSE), avec une barre de
  progression par étape — pull, build, démarrage, health check. Le panneau
  reste après coup : c'est là qu'on lit pourquoi un build a échoué. Réservé aux
  admins, la sortie d'un build recrachant volontiers des secrets.
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
- **Maintenance disque** : usage Docker (images, conteneurs, volumes, cache de
  build) et taille par app. Le nettoyage chiffre l'espace récupérable par
  catégorie *avant* qu'on clique, puis supprime tout ce que plus rien ne
  réclame — images sans conteneur, couches non taguées, cache de build,
  conteneurs laissés par un déploiement, réseaux vides. Il épargne les apps
  arrêtées, leurs images et les derniers builds git de chaque app (cibles de
  rollback) ; les volumes orphelins sont proposés à part, en case à cocher,
  parce qu'eux ne se re-téléchargent pas.
- **Identifiants Git** : page dédiée (Paramètres → Git credentials). Chaque
  token déclare sa *portée* — une forge (`github.com`), une organisation
  (`github.com/acme`), un dépôt précis, ou `*` en repli — et c'est la portée la
  plus étroite qui gagne. Un compte GitHub perso et une org de travail peuvent
  donc avoir chacun leur token, et aucun n'est jamais proposé à l'autre (la
  comparaison se fait segment par segment : `github.com/acme` ne prend jamais
  `github.com/acmecorp`). Tokens chiffrés avec la master key, jamais
  réaffichés (seulement un indice masqué), testables en un clic (`git
  ls-remote` réel), avec la liste des apps qui dépendent de chacun. Liens et
  scopes exacts fournis pour chaque forge. Le token unique des versions
  précédentes est migré automatiquement vers `*`.
- **TLS par app** : état du certificat de chaque hostname servi, et diagnostic
  quand il manque (nom qui ne résout pas, ou qui pointe ailleurs que sur ce
  serveur — la cause habituelle d'une app sans HTTPS).
- **Certificats** : liste des certificats détenus par Traefik avec l'app qui
  route chacun ; suppression de ceux que plus rien ne route (Traefik redémarre,
  quelques secondes d'indisponibilité).
- **Thèmes** : Nebula (sombre, défaut), Marathon (brutalism orange/os, d'après
  Marathon de Bungie), Nord, Synthwave, Terminal (vert CRT), Paper et Solarized
  (clairs) — via variables CSS, persisté en cookie.
- **Catalogue one-click** : ~60 services self-host classés par catégorie
  (médias, fichiers, notes, tableaux de bord, sécurité, dev, analytics, bases
  de données, serveurs de jeu…), avec recherche. Une entrée est soit une image
  unique, soit une stack Compose complète — Immich, Nextcloud, Authentik ou
  Paperless arrivent avec leur base de données et leur cache. Formulaire
  prérempli, secrets générés automatiquement.
  L'adresse publique dont une entrée a besoin (`URL`, `BASE_URL`, `url`…) est
  déduite du sous-domaine et du domaine : rien à recopier à la main. Les stacks
  attendent leur base de données via `healthcheck` + `depends_on: condition`,
  donc pas de boucle de redémarrage au premier déploiement.
  Les serveurs de jeu et les bases de données ne parlent pas HTTP : Traefik ne
  tient que :80 et :443 et route sur l'en-tête Host, donc ces apps publient
  leur propre port et se joignent à l'IP du serveur, pas au sous-domaine. Des
  entrypoints TCP/UDP dédiés dans Traefik les rendraient routables comme les
  autres — piste ouverte, pas encore faite.

  Le catalogue est vérifiable, pas seulement relu :

  ```sh
  # Chaque image existe encore dans son registre (réseau seul, pas de Docker).
  CATALOG_IMAGES=1 go test ./internal/catalog/ -run TestEveryImageStillExists

  # Chaque entrée est réellement déployée et sondée (nécessite Docker).
  CATALOG_DEPLOY=1 go test ./internal/catalog/ -run TestDeploy -parallel 4 -timeout 3h
  CATALOG_DEPLOY=1 CATALOG_ONLY=immich,outline go test ./internal/catalog/ -run TestDeploy -v
  ```
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
- **Auto-update** : vérification des releases GitHub (30 min), bouton dans la
  barre du haut dès qu'une version est disponible, mise à jour en un clic via
  un conteneur updater éphémère — seules quelques secondes d'indisponibilité du
  dashboard, les apps ne sont pas touchées.
- **Mise à jour de Traefik** : depuis la page System, vers la version avec
  laquelle la release courante de Quasar a été testée (jamais la dernière de
  Docker Hub — Traefik est la brique qui, si elle ne démarre pas, emporte tous
  les sites). Le pin est écrit dans `docker-compose.override.yml`, donc il
  survit à un `docker compose up -d` lancé à la main et laisse le dépôt git
  propre. Si la nouvelle version ne tient pas debout, l'ancienne est remise en
  place automatiquement.

## Arborescence sur le VPS

```
/opt/quasar/
├── setup.sh
├── docker-compose.yml       # Traefik + socket-proxy + dashboard
├── docker-compose.override.yml  # Version de Traefik épinglée par le dashboard
│                            # (absent tant qu'aucune mise à jour n'a été faite ;
│                            #  le supprimer revient au pin de docker-compose.yml)
├── .env                     # Secrets globaux (chmod 600)
├── traefik/
│   ├── traefik.yml          # Conf statique (généré par setup.sh)
│   └── acme.json            # Certificats (chmod 600)
├── storage/
│   └── database.sqlite
├── backups/                 # Archives quasar-<date>.tar.gz
└── apps/<app-id>/
    ├── source/              # Clone Git (mode build) — son compose éventuel
    │   │                    #   est lancé depuis ici
    │   └── docker-compose.quasar.yml  # Généré : le compose du repo, adapté
    ├── docker-compose.yml   # Mode compose injecté
    ├── docker-compose.quasar.yml      # Généré : idem, pour un compose injecté
    ├── .env                 # Passé en --env-file aux stacks compose
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
  limitées : containers, images, networks, build, session, grpc, volumes, info,
  system, exec + POST). `EXEC=1` est requis par le terminal web et les tâches —
  retirez-le si vous n'utilisez pas ces fonctionnalités. `SESSION=1` / `GRPC=1`
  donnent accès au BuildKit du daemon : sans eux, `docker compose build` démarre
  un conteneur BuildKit **privilégié** par build à la place.
- Sessions HTTP-only, Secure, SameSite=Lax ; mots de passe bcrypt.
- `/` de l'hôte est monté **en lecture seule** dans le dashboard uniquement
  pour les métriques disque (`HOST_ROOT`).
- Mode compose (injecté ou détecté dans un repo Git) : Quasar réécrit le
  fichier pour poser les labels Traefik sur un seul service, celui qui sert le
  site. Les ports que la stack publie sur l'hôte en dehors de 80/443 sont
  **conservés** — une stack peut vouloir exposer une base ou un serveur de jeu
  — mais ils contournent Traefik, donc TLS et les protections configurées pour
  l'app ; le panneau *Routing* les signale. Un fichier portant déjà des labels
  `traefik.*` est exécuté tel quel, sans réécriture.
