# Example stations

Stations you can install into a running Quasar. Nothing here is compiled into
the binary: these are documents, and a Quasar install only has the ones its
operator gave it.

A station is an application that arrives with a control surface of its own —
tabs, actions and views written for one service in particular. Deploying one
produces an ordinary Quasar application: the same containers, logs, storage
explorer, backups, resource limits and TLS as anything else on the dashboard,
with the station's tabs on top. Remove the station and a perfectly normal
application is left running.

The format is in [`docs/stations.md`](../docs/stations.md).

## Using one

Either way, *Settings → Stations*:

- **Paste it.** Open the file, copy it, paste it into the document box, add it.
- **Import it by URL**, which keeps the source and gives you a re-fetch button:

  ```
  https://raw.githubusercontent.com/AymericChaverot/quasar/main/stations/minecraft.yaml
  ```

  Nothing re-fetches on its own, and a revision that asks for more than the one
  you accepted is held until you accept the new set — the station keeps running
  the approved revision in the meantime.

Either way you are shown, before anything is installed, every privileged thing
the document asks to be allowed to do, in plain words. A station is not usable
until those are accepted.

## What is in here

The first three are meant to be run. The last three are meant to be read.

| File | What it is |
| --- | --- |
| [`minecraft.yaml`](minecraft.yaml) | The format at full stretch: a console, player administration, world archives with a restore and a download, mod management against Modrinth, a version picked from Mojang's own list of releases rather than typed, and a day of player history the station draws itself as an SVG. Every component, every hook. |
| [`postgres.yaml`](postgres.yaml) | A Postgres server with a query console, per-database dumps on a schedule, and a restore. Everything over the container's own socket — it asks for nothing on the network, inside or out. |
| [`gitea.yaml`](gitea.yaml) | A Git forge that reads its own API over the internal network, embeds its pages without publishing a port for them, and watches GitHub for a release — then moves to it by changing one environment key and redeploying. |
| [`components.yaml`](components.yaml) | One of every panel the format offers, drawn from a script granted **nothing at all**. The baseline everything else is measured against. |
| [`permissions.yaml`](permissions.yaml) | Every capability there is, each beside what a refusal looks like — because the stack it deploys has a second service that nothing in the document names. |
| [`limits.yaml`](limits.yaml) | A station that breaks itself in every documented way: a loop that never ends, an allocation storm, a bottomless recursion. The dashboard is still there afterwards, which is the claim all six make. |

Take them as starting points rather than as recommendations: the images are
ones people run, but the versions, ports and defaults are yours to set.

## Three permission profiles, on purpose

The interesting thing about the first three is not what they do, it is what
each one had to ask for:

- **`postgres.yaml`** asks for `exec`, the dumps folder, two environment keys
  it was itself given, and `restart`. No network at all.
- **`gitea.yaml`** asks for the two network permissions as well — `net.internal`
  for its own API and the pages it embeds, `net.external` for one named host —
  and for `env` **write**, because changing the image tag is how a compose
  application changes version.
- **`minecraft.yaml`** asks for `files` over eight paths and for three named
  hosts — two at Modrinth and Mojang's list of releases — and pointedly **not**
  for the world directory: a station that can write into a live world is a
  station that can corrupt one. The archives are in those eight paths, which is
  what lets it hand one over: a file a station may offer is a file it could
  have read.

None of the three asks for `stop`. A station has no business being able to take
an application off the network for good.

## The two substitutions

They meet in every `deploy:` block, and the difference is the thing to hold on
to:

- `{{NAME}}` is resolved by **Quasar**, when the station is deployed, in the
  env, the image reference, the compose file, and the name and subdomain
  proposed for the application. `{{RANDOM}}` (a fresh secret per occurrence),
  `{{HOST}}` and `{{URL}}` are answered without being declared; everything else
  has to be a parameter the document declares.
- `${NAME}` is left exactly as it is, for **docker compose** to read from the
  `.env` at run time.

So a station's usual shape is a one-line `NAME={{NAME}}` in its env, read back
as `${NAME}` in the compose file — and, for the values a script needs to see, a
matching entry under the `env` permission.

## Checks

A document that does not pass is not installed, and the page lists everything
wrong with it rather than the first thing. The files in this folder are held to
exactly what a pasted document is held to, plus one thing pasting cannot check
for you — that the script is JavaScript the runtime can load:

```
go test ./internal/station/ -run TestShippedStations
go test ./internal/station/runtime/ -run TestEveryShippedStation
```

An example that would be rejected on paste, or that installs and then fails at
the first click, is worse than no example.
