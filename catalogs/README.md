# Example catalogues

Catalogues you can import into a running Quasar. Nothing here is compiled into
the binary: these are documents, and a Quasar install only has the ones its
operator gave it.

A catalogue adds entries and categories to the ones Quasar ships, and an entry
is a prefill for the new-application form — an image or a compose stack, its
env, the port the domain routes to. What makes these worth reading is that
every one of them asks questions first: a version, a world name, a host port.
One entry then covers a fleet rather than a single installation.

## Using one

Either way, *Settings → Catalogues*:

- **Paste it.** Open the file, copy it, paste it into the document box, give it
  a name, add it.
- **Import it by URL**, which keeps the source and gives you a re-fetch button:

  ```
  https://raw.githubusercontent.com/AymericChaverot/quasar/main/catalogs/game-servers.yaml
  ```

  Nothing re-fetches on its own. A catalogue is the compose files this server
  will run, so bringing in a new version is a button you press.

Entries can also be edited one at a time in a form, without writing YAML — it
is the same document from both sides, so starting in one and finishing in the
other is fine.

## What is in here

| File | What it shows |
| --- | --- |
| [`game-servers.yaml`](game-servers.yaml) | Parameters carried into the app name and the subdomain, so a second server collides with neither. A `select`, a `number` and a `port` field. An entry reusing a built-in id (`valheim`) to **replace** that card instead of sitting beside it. |
| [`dev-stacks.yaml`](dev-stacks.yaml) | A parameter inside the image reference itself (`postgres:{{VERSION}}-alpine`), which is the shortest useful shape one takes: one card, every published version. |
| [`homelab.yaml`](homelab.yaml) | A category Quasar does not ship, appearing as a filter button of its own. `{{HOST}}` and `{{URL}}` answered by Quasar, and a `{{RANDOM}}` secret written once in the env and read back by two services of one stack. |

Take them as starting points rather than as recommendations: the images are
ones people run, but the versions, ports and defaults are yours to set.

## The two substitutions

They meet in every entry and the difference is the thing to hold on to:

- `{{NAME}}` is resolved by **Quasar**, when the entry is picked, in the env,
  the image reference, the compose file, and the name and subdomain proposed
  for the application. `{{RANDOM}}` (a fresh secret per occurrence), `{{HOST}}`
  and `{{URL}}` are answered without being declared; everything else has to be
  a parameter the entry declares, and a `{{VERISON}}` that no parameter matches
  is a save-time error rather than a literal that reaches somebody's `.env`.
- `${NAME}` is left exactly as it is, for **docker compose** to read from the
  `.env` at run time. Quasar writes the env beside the compose file and passes
  it with `--env-file`, which is how a value picked once reaches every service
  of a stack.

So a compose entry's usual shape is a one-line `NAME={{NAME}}` in its env, read
back as `${NAME}` in the compose file.

## Checks

A document that does not pass is not saved, and the page lists everything wrong
with it rather than the first thing. The entries Quasar ships are held to the
same checks, and so are these files: `TestExampleCatalogsAreAccepted` in
`internal/catalog` parses and validates every YAML file in this folder, because
an example that would be rejected on paste is worse than no example.

```
go test ./internal/catalog/ -run TestExampleCatalogs
```
