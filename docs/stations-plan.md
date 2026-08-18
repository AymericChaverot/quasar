# Implementing stations

A sequence of commits for [the specification](stations.md). Each step leaves
`go build ./... && go test ./...` green and does one coherent thing, so the
branch is reviewable a commit at a time and can stop at any of the milestones
below without leaving half a feature in the tree.

## Two corrections to the specification

Both found by reading the code rather than the spec, and both change what gets
built.

**The application page has no tabs.** `web/templates/pages/app_detail.html` is
one scrolling column of `<section>` blocks — Build, Routing, Stack containers,
TLS, resources, Storage, Environment, and so on. The spec's "tabs beside
Overview / Logs / Storage" describes a page that does not exist.

The cheapest correct answer, and the one this plan takes: a station renders as a
**single block with its own tab strip, placed at the top of the application's
page**, above Build. Ordinary applications are untouched, there is no regression
to test for, and the station reads as what it is — a control surface somebody
wrote for this service, sitting above the machinery Quasar provides for every
service. Giving the whole application page a real tab bar is a good idea on its
own merits; it is a different change and should not ride in on this one.

**`exec` is per application, not per service.** `docker.RunCommand` resolves one
container through `containerFor`, which picks the compose project's web service
or the first one it finds. The spec's `quasar.exec(service, argv)` needs a new
`(*Client).ExecInService(ctx, app, service, argv)` that resolves by compose
service name and refuses a service the permission did not list. It also takes
**argv, not a shell string**, unlike `RunCommand`, which runs `/bin/sh -c`:
interpolating a mod filename into a shell string is an injection waiting to
happen, and stations interpolate constantly.

## What already exists, and is reused rather than rewritten

| Need | Existing code |
|---|---|
| Deployment, params, `{{RANDOM}}`, compose rewrite, port collisions | `internal/catalog` — the `deploy` block *is* `catalog.Template` |
| Import by paste or URL, re-fetch, validation, errors in plain words | `internal/server/handlers_catalogs.go`, `internal/catalog/yaml.go` |
| Path confinement with symlinks resolved before the check | `files.Root.Resolve` — written, and its escape tests already exist |
| Atomic write, permissions preserved | `internal/files/write.go` |
| Container logs | `docker.StreamLogs` |
| Background job with phases and an SSE progress pane | `docker.runAsync`, `handlers_deploy.go` |
| Audit entries | `internal/db/audit.go` |
| Theme variables | the 22 custom properties in `web/static/themes.css` |
| Per-platform syscalls behind one API | `internal/files/access_linux.go` / `access_other.go` — the pattern the worker's rlimits follow |

The only new dependency is `github.com/dop251/goja`. Pure Go, no CGO, which is
the same constraint that picked modernc's SQLite.

## Steps

### 1. The document format

`internal/station`: the `Station` type with `Deploy catalog.Template`, the
permission types, YAML parsing, and validation returning a slice of plain-words
problems the way catalogues already do. No storage, no server, no runtime.

Files: `internal/station/{station,parse,permissions,validate}.go` + tests.

Tests: a valid document round-trips; every rejection names its field; **the
worked example in `docs/stations.md` is extracted and parsed** — the catalogue
holds its documented example to the same standard for the same reason, and
documentation that would be refused on paste is worth less than none.

> `Add the station document format`

### 2. Storage and import

The `stations` table, its CRUD, and Settings → Stations built from the
catalogues page. Import by paste or URL, list, toggle, delete. Permissions
rendered in plain words on the import screen and recorded as accepted.

```sql
CREATE TABLE IF NOT EXISTS stations (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    station_id   TEXT NOT NULL UNIQUE,     -- the document's own id
    name         TEXT NOT NULL,
    source_url   TEXT NOT NULL DEFAULT '',
    yaml         TEXT NOT NULL,            -- the approved revision
    perms_hash   TEXT NOT NULL,            -- what was accepted
    prev_yaml    TEXT NOT NULL DEFAULT '', -- one-click revert
    pending_yaml TEXT NOT NULL DEFAULT '', -- fetched, not yet approved
    pending_hash TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    position     INTEGER NOT NULL DEFAULT 0,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

Three columns rather than a revisions table: approved, previous and pending is
the whole state machine, and a station with a hundred revisions is not a problem
anyone has.

Files: `internal/db/stations.go`, a line in `internal/db/db.go`,
`internal/server/handlers_stations.go`,
`web/templates/pages/stations_settings.html`.

Tests: importing a duplicate `station_id` is refused and names the holder; a
document that fails validation is not stored.

> `Store and import stations`

### 3. Re-fetch, held revisions, revert

Re-fetch stores into `pending_yaml`. If the permissions hash matches, it is
promoted immediately; if it does not, the station keeps running the approved
revision and the page shows what changed until somebody accepts. Accepting moves
approved into `prev_yaml` and pending into `yaml`. Revert swaps them back.

Tests: a re-fetch that adds `net.external` does **not** take effect; the same
revision fetched twice is a no-op; revert restores the previous document.

> `Hold a station revision that changes its permissions`

### 4. Deploying a station

The `Stations` page in the navigation with a card per station, the parameter
form, and prefilling the new-application form — the catalogue's path, reusing
`catalog.Prefill` unchanged. One column on `apps`:

```sql
ALTER TABLE apps ADD COLUMN station_id TEXT NOT NULL DEFAULT '';
```

No per-application revision pinning: a re-fetch is meant to reach every
application running that station, which is the entire point of fixing the mod
manager once for three servers.

**Milestone.** At the end of this step a station deploys and produces a working
application. It has no custom interface yet, but everything catalogue-shaped
works, and every remaining step only adds to a page that already renders.

Files: `internal/server/handlers_stations.go`,
`web/templates/pages/stations.html`, `internal/db/apps.go`.

> `Deploy an application from a station`

### 5. The worker process

The isolation layer, before any script logic. The dashboard re-executes its own
binary as `quasar station-worker` over a pipe: the parent writes the call, the
worker answers, the worker dies. The worker inherits no Docker socket, no
database handle, no writable directory and no network.

The protocol is line-delimited JSON in both directions, because the worker also
*asks*: `→ call {action, input, script}`, `← request {capability, args}`,
`→ response {...}`, `← result {...}` or `← error {...}`. Capabilities are
unimplemented in this step — a request for one comes back refused — but the
shape is the whole point of doing this first.

Bounding, all of it from the parent: resident size sampled every 50 ms with a
`SIGKILL` above 128 MB, a hard kill after the wall clock, `RLIMIT_CPU` and
`RLIMIT_DATA` set on the child. **Not `RLIMIT_AS`** — the Go runtime reserves
address space generously and a low ceiling there kills the worker before it runs
a line. The rlimits split by build tag, following
`internal/files/access_linux.go` / `access_other.go`; on Windows the process
boundary and the sample-and-kill watchdog still apply.

Files: `cmd/server/worker.go`, `internal/station/worker/{spawn,protocol,limits}.go`,
`internal/station/worker/limits_{linux,other}.go` + tests.

Tests: a worker that allocates without pause is killed and the parent reports
which limit it hit; a worker that ignores its interrupt is killed on the wall
clock; a worker crash is an error on the panel, not a dashboard restart; a
capability request in this step is refused.

> `Run station scripts in a disposable worker process`

### 6. The runtime inside the worker

goja in the child: the script's `export function`s loaded, one called, the result
marshalled back over the protocol. `vm.Interrupt` from a timer so an ordinary
runaway loop reports a clean timeout the author can read, rather than being shot
by the parent. Result size capped. Only the namespaces needing no permission:
`quasar.app`, `quasar.log`, `quasar.store`.

Files: `internal/station/runtime/{runtime,bridge}.go` + tests.

Tests: a script returning a value; `while(true)` reports a timeout rather than
hanging the request; a 10 MB return is refused; touching an ungranted namespace
throws an error **naming the missing permission**, not `undefined`.

> `Run station scripts in a sandboxed JS runtime`

### 7–10. Capabilities, one commit each

Each adds a namespace in the worker, its protocol message, its permission check
and audit entry **in the parent**, and its denial test. Split because each has
its own way of going wrong.

The shape is identical every time and worth stating once: the worker's
`quasar.x.y(...)` marshals a request up the pipe; the parent validates it
against the station's declared permissions, executes it with the privileges the
worker does not have, writes the audit entry, and sends the result back. A
permission check that only existed as "we did not inject that binding" is now a
check on the privileged side of a process boundary.

**7. `files` and `env`** — over `files.Root`, so confinement and the symlink
tests come for free; writes over `internal/files/write.go`, so atomicity and
preserved permissions do too. The permission's globs are matched against the
cleaned relative path. `env` is per declared key, read and write separately.
Audit: `station.files.write`, `station.files.delete`, `station.env.write`.

Tests: a path outside the declared globs is refused; `../` and a symlink leading
out are refused; an undeclared env key is refused on read *and* on write.

> `Give stations confined file and env access`

**8. `exec` and `logs`** — the new `ExecInService(ctx, app, service, argv)`
described above, plus log reads, both restricted to the services the permission
names. Audit: `station.exec`.

Tests: an undeclared service is refused; argv is not shell-interpreted, so a mod
named `x; rm -rf /.jar` stays one argument; output over the cap is truncated and
says so.

> `Give stations exec and log access to named services`

**9. `net.internal` and `net.external`** — an `http.Client` per station whose
transport checks the host against the allowlist, refuses plain HTTP, follows
redirects only to hosts also on the list, and caps the body.
`quasar.service(name, port)` returns an internal base URL. Audit:
`station.http.external`.

Tests: an unlisted host is refused; a redirect to an unlisted host is refused; a
body over the cap is refused; `net.internal` cannot reach a public address and
`net.external` cannot reach a private one.

> `Give stations an allowlisted HTTP client`

**10. `lifecycle` and `notify`** — start / stop / restart / redeploy / setImage,
listed verbs only, over the existing async deploy machinery; `notify` over the
configured webhook, rate-limited. Audit: `station.lifecycle`.

Tests: an unlisted verb is refused; a `redeploy` from a script takes the same
path as the button.

> `Let stations drive their application's lifecycle`

### 11. The interface

The DSL rendered server-side: `GET /apps/{id}/station/panel/{panel}` calls the
source action and renders the component,
`POST /apps/{id}/station/action/{name}` runs an action and applies its return
value, and the station block with its tab strip goes at the top of
`app_detail.html`.

Components in this step, enough to render the worked example: `table` (with
`row_actions`, `empty`, `refresh`), `form`, `stat`, `keyvalue`, `markdown`,
`button`, `section`. HTMX for refresh and actions, as the rest of the dashboard
already does.

**Milestone.** This is the feature. Everything after it is enrichment.

Files: `internal/server/handlers_station_ui.go`,
`internal/station/ui/{schema,render}.go`,
`web/templates/partials/station_*.html`, a block in `app_detail.html`.

Tests: a table renders its rows and its empty state; an action's `refresh`
targets the right panel; **a script returning a string where a table was
declared produces a legible error in the panel**, not a blank card — an author
debugging through a blank card gives up.

> `Render a station's tabs and panels`

### 12. The remaining components

`grid`, `divider`, `banner`, `list`, `code`, `log` (over the existing SSE log
pane), `gauge`, `timeline`, `image`, `iframe` with `{{service}}` resolution,
`search`, `confirm`.

> `Add the rest of the station components`

### 13. The theme

The token block applied to the station wrapper: accent, with `accent-hover` via
`color-mix` and `accent-text` computed in Go by real contrast; `tint` mixed into
the surfaces; scale, case, tracking, density; radius and border width; embedded
`data:` fonts capped at 512 KB; icon and banner.

Tests: the contrast function picks the readable foreground against every shipped
theme's surfaces; a `scale` outside `[0.9, 1.25]` is clamped rather than
refused; a font `src` that is a URL rather than a `data:` URI is refused at
import.

> `Let a station theme its own tabs`

### 14. Long actions

`long: true` runs the action as a background job over `runAsync`, with
`quasar.progress(pct, message)` feeding the existing progress pane. It survives
a reload and keeps its output afterwards.

Tests: a long action's output is readable after the request that started it has
ended; two long actions on the same application do not interleave.

> `Run long station actions as background jobs`

### 15. Hooks

`after_deploy`, `on_start`, `on_stop`, `on_health_fail`, and `every`.
Non-blocking throughout; scheduled actions run only while the application is
running.

Tests: a hook that throws does **not** fail the deployment, and its failure
reaches the audit log and the station's tab; a scheduled action on a stopped
application does not run.

> `Run station hooks on deployment and on a schedule`

### 16. Documentation and a shipped example

A README section, and `stations/minecraft.yaml` beside `catalogs/` — the worked
example, real and installable — held to a parse-and-validate test the way
`catalogs/` already is.

> `Ship a Minecraft station and document the format`

## Risks

- **The worker is the load-bearing piece.** Steps 7–10 assume the boundary
  holds; if the protocol has to change after four capabilities are written on
  top of it, that is four commits to redo. Getting the request/response shape
  right in step 5, with a refused capability already exercising the full round
  trip, is what stops that.
- **Process spawn on every panel render.** A tab with four panels refreshing
  every 30 s is eight spawns a minute per open page. A Go binary starts in a few
  milliseconds and the budgets are 10 s, so this should not matter — but it is
  the number to measure at the end of step 5, before it is baked in. If it does
  matter, one worker per *page render* rather than per panel is the fix, and it
  keeps the isolation.
- **Windows dev, Linux prod.** The rlimits are absent on Windows, so the
  behaviour differs where the code is written from where it runs. The watchdog
  and the process boundary are the same on both, which keeps the difference to a
  second line of defence rather than the first — but a test that only passes on
  the developer's machine is the classic way to find out too late.
- **goja is a large dependency** for a project whose dependency list is
  deliberately short. Nothing in the design forbids swapping the engine later —
  the boundary is `internal/station/runtime`, entirely inside the worker — but
  it is a commitment.
- **Step 11 is the big one.** If it wants splitting, the seam is transport
  against rendering: panels and actions over HTTP first, returning JSON, then
  the components rendered on top.
- **Scope creep into a general plugin API.** The line the spec draws — scripts
  return data, Quasar renders — is what keeps stations safe and what keeps them
  looking like Quasar. Every request to "just let it return some HTML" is the
  same request, and it should keep getting the same answer.
