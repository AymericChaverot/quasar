# Stations

**Status: specification. Nothing below is implemented yet.**

A station is an application that arrives with a control surface of its own —
tabs, actions and views written for one service in particular. A Minecraft
station does not just deploy a Minecraft server: it gives that server's page a
mod manager, a version upgrade button, a map viewer and a handful of settings
worth reaching for, because whoever wrote the station knew what running a
Minecraft server actually involves and Quasar does not.

It is one YAML document. Pasted or imported by URL, exactly like a catalogue.

## What a station is not

- **Not a catalogue entry.** A catalogue entry says what to deploy. A station
  says that *and* what the page should look like afterwards. Keeping code out of
  catalogues is deliberate: a catalogue is data an operator reads to decide what
  to run, and it should stay something that can be read that way.
- **Not a separate class of application.** A deployed station produces an
  ordinary application: the same `db.App` row, the same containers, logs,
  storage explorer, backups, resource limits, TLS, webhooks. It carries one
  extra field pointing at the station it came from. Everything Quasar already
  does to an application keeps working, untouched, and a station that is removed
  leaves a perfectly normal application behind.
- **Not an add-on for an existing application.** A station describes its own
  deployment. Grafting a station onto an application deployed some other way is
  a plausible later feature; it is not this one.

## The document

```yaml
schema: 1
id: minecraft-station          # unique; also the default subdomain
name: Minecraft
description: Fabric or Forge server, with mods and a live map
author: aymeric
version: "1.2.0"               # the author's, for display and update diffs

deploy: { ... }                # what to run          — § Deployment
permissions: { ... }           # what the script may do — § Permissions
ui: { ... }                    # what the page shows   — § Interface
hooks: { ... }                 # when the script runs  — § Hooks
script: |                      # the logic             — § Runtime
  export function list_mods() { ... }
```

`schema` is required and checked from the first release. A format that cannot
say which version it is written in cannot be changed later without breaking
every document already in the wild.

`id` collisions are refused at import, naming the station that already holds it.
Unlike catalogue entries, a station does not override another by reusing its id:
a catalogue entry is a description of third-party software that two people may
legitimately both describe, while a station is a program, and silently replacing
somebody's program with somebody else's is not a feature.

## Deployment

The `deploy` block is the existing `catalog.Template` shape, unchanged:

```yaml
deploy:
  deploy_type: compose         # or image
  compose_service: minecraft
  port: 25565
  raw: true
  app_name: "Minecraft {{VERSION}} ({{TYPE}})"
  subdomain: "mc-{{TYPE}}-{{VERSION}}"
  params:
    - {name: TYPE, label: Mod loader, kind: select, default: FABRIC,
       options: [FABRIC, FORGE, NEOFORGE]}
    - {name: VERSION, label: Version, default: "1.20.1"}
    - {name: HOST_PORT, label: Port, kind: port, default: "25565"}
  env: |
    TYPE={{TYPE}}
    MINECRAFT_VERSION={{VERSION}}
  compose: |
    services:
      minecraft: ...
```

This is not a convenience, it is the point. Parameter substitution,
`{{RANDOM}}` secrets, compose rewriting for Traefik, front-service detection,
host port collision refusal, `--env-file` handling — all of it already exists,
is already tested, and applies to stations without a line of new code. A station
is a catalogue entry with three more blocks bolted on, and it should stay
readable as one.

Deploying a station therefore walks the same path as deploying a catalogue
entry: the parameter form, then the prefilled new-application form, then the
live deploy panel. The only difference is what the application's page looks like
once it is up.

### Options the station supplies

A `select` normally offers what the document wrote, which is right for the lists
that are short and stay still — a mod loader, a difficulty. It is wrong for the
ones that are neither. Every release of Minecraft there has ever been is a list
that grows without the station being touched, and the alternative a document
reaches for instead — a free text box — accepts `1.21.9` happily and hands the
operator a container that will not start.

So a parameter may name an action of the station's own:

```yaml
    - name: VERSION
      label: Minecraft version
      kind: select
      default: "1.21.4"
      options: ["1.21.4", "1.21.1", "1.20.6"]   # what the form falls back to
      options_from: official_versions           # what it offers when this answers
```

```js
export function official_versions() {
  return quasar.http.get(MANIFEST).json().versions
    .filter(v => v.type === 'release').map(v => v.id)
}
```

**Which one to start on.** A list is the whole of it in the ordinary case. Where
the source also knows what is current, the action can say so, and the form
starts there instead of on the document's `default`:

```js
export function official_versions() {
  const m = quasar.http.get(MANIFEST).json()
  return { options: releases(m), default: m.latest.release }
}
```

That is worth the second shape because the alternative goes stale by itself: a
`default` written into a document is the version its author was running, and a
form proposing it a year later is proposing last year's server to somebody who
came to install a new one. A default the action is not also offering is ignored,
since a form that proposed a value it would then refuse is worse than one
proposing the document's.

This is the only time a station's script runs with no application, and it has
correspondingly little to run with: `quasar.app` is empty, no answers have been
given yet so the action receives none, and every capability that would need an
application — `files`, `exec`, `env`, `store`, `logs`, the lifecycle verbs —
refuses in those words. What is left is `http.get` and `http.post`, against the
hosts `net.external` names, checked exactly as they are everywhere else.

What comes back is **added to** `options`, never substituted for it. The written
list is what the form offers when the answer does not arrive — a server with no
route out, an API having a bad afternoon, an action that throws — and a dropdown
that empties itself because somebody else is down is worse than a short one. A
failed ask is a line in Quasar's log rather than an error on the page: nothing
the operator did caused it and there is nothing they can do about it.

The answer leads the dropdown and the written values follow, because an answer
is the source speaking and it knows the order — newest first, for a list of
releases — while the written ones are a fallback and a home for values the
source has no concept of.

The answer is cached for an hour per station and action, and the same expanded
list is what the deployment accepts the value against — so a version the form
offered is a version the deploy takes, rather than one silently replaced by the
default at the moment somebody pressed the button.

## Permissions

Every privileged thing a script can do sits behind a permission the document
declares. Nothing is granted by default: a station with no `permissions` block
gets a runtime that can compute and return values, and nothing else.

```yaml
permissions:
  exec:         {services: [minecraft]}
  logs:         {services: [minecraft]}
  files:        {paths: ["data/mods/**", "data/server.properties"]}
  env:          {read: [TYPE, MINECRAFT_VERSION], write: [MINECRAFT_VERSION]}
  net.internal: {services: [minecraft], ports: [8123, 25575]}
  net.external: {allow: ["api.modrinth.com", "meta.fabricmc.net"]}
  lifecycle:    [restart, redeploy]
  notify:       true
```

| Permission | Grants | Notes |
|---|---|---|
| `exec` | `docker exec` in the named services | The strongest one. Reuses the Tasks machinery. |
| `logs` | Reading container logs | Separate from `exec` because it is far weaker and far more often all a station needs. |
| `files` | Read/write under `apps/<id>/` | Restricted to the declared globs. See below. |
| `env` | Reading/writing named `.env` keys | Per key, not wholesale: a station has no business reading a database password it did not generate. |
| `net.internal` | HTTP to the app's own containers | Named services and ports only. |
| `net.external` | HTTPS to the named hosts | Exact hosts, no wildcards, no plain HTTP. |
| `lifecycle` | start / stop / restart / redeploy / set image | Listed verbs only. |
| `notify` | Sending to the configured webhook | Rate-limited. |

`quasar.store` and `quasar.series` need no permission: both are scoped to one
application and one station, and neither can reach anything else. The store is
a small key–value space; the series are what the station has measured about
its application over time. Both live exactly as long as that pair does:
deleting the application clears what every station kept for it, and removing
the station clears what it kept for every application. The applications carry
on either way — a removed station leaves them running, as it always did — but
nothing is left behind that would be read back if the same station id were
installed again.

**A series is what the store cannot hold.** A store answers "what did I work
out last time"; a series answers "what was it on Tuesday", which is what a
chart is drawn from. A station keeping its own history under a store key would
fill its 256 KB in a few days, lose the lot on one failed write, and re-marshal
the whole list on every sample. So Quasar keeps it instead: a station records a
value from a scheduled hook, and a `chart` panel reads the history back without
the script running at all. One station may keep 8 series per application, named
in lowercase like identifiers.

**Kept at full resolution for a week, and by the hour for a year.** Every other
time series on the server is dropped at seven days; a station's is folded
instead — each hour older than that becomes one row holding the mean, the least
and the most it reached. It costs a fraction of the samples it replaces, which
is what makes a year of it affordable, and the least and the most are kept
because an average hour hides the spike that was the reason anybody opened the
graph. Reading a series never has to know where the seam is: ask for thirty
days and the recent part comes back as samples and the rest as hours.

```yaml
hooks:
  every:
    - {minutes: 5, action: sample}
```
```js
export function sample() {
  quasar.series.record('players', online().length)
}
```

**`net.external` must name its hosts.** A permission that reads "may reach the
internet" tells the operator nothing and is an exfiltration channel with their
signature on it. Naming `api.modrinth.com` turns the install screen into real
information. Redirects are followed only to hosts also on the list.

**`files` paths are confined and resolved.** Every path is resolved against
`apps/<id>/`, symlinks followed *before* the check, and the result required to
still be inside. A `data/evil -> /opt/quasar/storage` in an application's own
volume is otherwise a full escape on the first write. Writes reuse the storage
explorer's guarantees: temporary file then rename, permissions preserved, so a
secret at `600` does not come back at `644` because a station touched it.

### Approval

Permissions are shown in plain words on the install screen, grouped by what they
let the station reach, and the station is not usable until they are accepted.

**A document whose permissions change does not take effect until re-approved.**
This is the rule that makes the whole model worth anything. A station imported
by URL is re-fetched by hand later; if a new revision quietly added
`net.external`, the operator would be handing out a capability they never
granted. On re-fetch, the new document is stored but held: the Stations page
shows what changed, and the station keeps running its previously approved
revision until somebody accepts.

## Runtime

The script is JavaScript, executed by **goja** — a JS interpreter written in Go.
No CGO, which matters in a project that picked modernc's SQLite for the same
reason, and a good starting point: goja gives a program nothing at all, so the
only things a station can touch are the ones Quasar injects by hand. It is the
first of two layers, not the whole answer; see *Where it runs*.

Concretely, the script has no `fetch`, no `require`, no `import` of anything, no
filesystem, no timers, no access to the host process. It has one global:
`quasar`, populated namespace by namespace according to the declared
permissions. A call into a namespace that was not granted throws an error naming
the missing permission — the failure is legible to the author, not a mysterious
`undefined`.

### Where it runs

**In a separate, disposable process — one per call.** The dashboard re-executes
its own binary in worker mode, hands it the script and the call over a pipe,
reads the result, and the worker dies. Nothing survives between two calls, which
was already the rule and is now enforced by the operating system rather than by
convention. Anything a station wants to remember goes in `quasar.store`.

This is not only about memory. goja is an interpreter, not a security boundary:
upstream exposes no memory limit, its README says nothing about running
untrusted code, and its own discussions of the question point at process
isolation. Running the script in-process would mean that a bug in the engine
hands a station everything the dashboard can reach — the Docker socket, the
database, the master key.

**The worker has none of that.** No Docker socket, no filesystem, no network,
no database handle. Every capability is a request sent back up the pipe, which
the parent checks against the declared permissions and executes on the worker's
behalf. The permission model is therefore enforced by a process boundary and not
merely by which JavaScript bindings were injected, and an escape from goja buys
an attacker a process that can do nothing.

It also means a station cannot take the dashboard down with it: a goja panic, a
stack overflow, an allocation storm — the worker dies, the panel shows why, and
every application on the server keeps running.

### API

```js
quasar.app                              // {id, name, domain, status, params, image}
quasar.app.start() / .stop() / .restart() / .redeploy()
quasar.app.setImage(ref)
quasar.env.get(key) / .set(key, value)  // declared keys only

quasar.exec(service, argv, opts)        // -> {code, stdout, stderr}
quasar.logs(service, {tail, since})     // -> string

quasar.files.list(path)                 // -> [{name, size, dir, mtime}]
quasar.files.read(path)                 // -> string
quasar.files.readBytes(path)            // -> Uint8Array
quasar.files.write(path, content)       // atomic
quasar.files.delete(path)
quasar.files.mkdir(path)

quasar.http.get(url, opts)              // -> {status, headers, body, json(), bytes()}
quasar.http.post(url, opts)
quasar.service(name, port)              // -> a base URL on the internal network

quasar.store.get(k) / .set(k, v) / .delete(k) / .keys()

quasar.series.record(name, value)       // one sample, from a scheduled hook
quasar.series.read(name, {hours})       // -> [{at, value}], oldest first
quasar.series.names()                   // -> what this station is keeping

quasar.notify(message)
quasar.log(...args)                     // to the station's own log, readable in the UI
quasar.progress(percent, message)       // long actions only, see § Actions
```

Everything is synchronous. There is no event loop, no promises, no
`setTimeout`: a station action is a function that runs and returns, which is
both simpler to write and simpler to bound.

**A response has two bodies, and downloads want the second one.** `body` is the
answer as text, which is what an API returns and what almost every action
reads. `bytes()` is the answer as it actually arrived, a `Uint8Array`, and it
is what anything that is not text has to go through: a jar, an image, an
archive. Reading a mod through `body` and writing that is how a station
installs a file the server then refuses to load — text is UTF-8, a jar is not,
and every byte that is not valid UTF-8 becomes U+FFFD on the way past without
anything failing. `quasar.files.write` takes either, and `quasar.files.readBytes`
hands back the same `Uint8Array` on the way out.

### Limits

| Bound | Panel source | Action | Hook |
|---|---|---|---|
| Wall clock | 10 s | 60 s | 120 s |
| Returned value | 1 MB | 1 MB | — |
| `quasar.exec` output | 1 MB per call | | |
| `quasar.http` response | 8 MB per call | | |
| `quasar.store` | 256 KB per application | | |
| `quasar.series` | 8 series per application; 7 days of samples, then a year by the hour | | |
| `quasar.files.read` | 4 MB | | |
| Worker memory | 128 MB resident | | |

Wall clock is enforced twice: `vm.Interrupt` from a timer inside the worker, so
a `while(true)` reports a clean timeout the author can read, and a hard kill from
the parent shortly after, so a worker that ignores its interrupt still dies.
Long actions opt out of the 60 s ceiling by running as background jobs
(§ Actions).

**Memory is enforced from outside.** The parent samples the worker's resident
size every 50 ms and `SIGKILL`s it above the ceiling. A watchdog living inside
the process it watches would only notice after the kernel had already picked a
victim — and the victim would be the dashboard, which is the one process on the
machine that must not die. Out here, killing is instant and safe, because the
worker holds nothing.

`RLIMIT_CPU` and `RLIMIT_DATA` are set on the worker as a second line. **Not
`RLIMIT_AS`:** the Go runtime reserves address space generously, and a low
ceiling there kills the worker before it has run a line of script.

On platforms without `setrlimit` — Windows, where this is developed but not
deployed — the worker still runs as a separate process with the parent's sample-
and-kill watchdog, and the two rlimits are skipped. The split follows the
pattern `internal/files/access_linux.go` and `access_other.go` already use.

## Interface

The script never produces HTML. It returns data; Quasar renders it with its own
components. That single rule is what makes stations safe to share — there is no
markup to sanitise, no XSS surface — and what makes them look like Quasar,
since a station rendered through Quasar's components inherits Nebula, Marathon,
Paper and the rest for free.

```yaml
ui:
  theme: { ... }
  tabs:
    - id: mods
      name: Mods
      panels:
        - id: mod_list
          type: table
          title: Installed mods
          source: {action: list_mods}
          columns:
            - {key: name,    label: Mod}
            - {key: version, label: Version, align: right}
            - {key: state,   label: "", type: badge}
          row_actions:
            - {label: Remove, action: remove_mod, tone: err,
               confirm: "Remove {{name}}?"}
          empty: "No mods installed yet."
          refresh: {seconds: 30}

        - id: add
          type: form
          title: Add a mod
          fields:
            - {name: url, label: Modrinth URL, placeholder: "https://modrinth.com/mod/..."}
          submit: {label: Install, action: add_mod}
```

### Components

| Family | Types |
|---|---|
| Structure | `section`, `grid`, `divider`, `banner` |
| Data | `table`, `stat`, `list`, `keyvalue`, `markdown`, `code`, `log`, `gauge`, `timeline`, `image`, `chart` |
| Input | `form`, `button`, `search`, `confirm` |
| Embedding | `iframe` |

Every panel accepts `id`, `title`, `help`, `variant` (`hero` / `plain` /
`inset`), `tone` (`accent` / `ok` / `warn` / `err`), `width` (`full` / `half` /
`third`) and `refresh: {seconds: N}`.

`form` fields cover `text`, `number`, `select`, `toggle`, `secret`, `port`,
`textarea` and `file`. `log` streams over SSE, reusing the log pane. `iframe`
resolves `{{service}}` to the internal address of a named service, so a map
viewer is one line and no port has to be published to the world.

A panel's `source` is either `{action: name}` — the script is called, its return
value renders the panel — `{static: ...}` for content that never changes, or
`{series: [...]}` for a chart of what the station has recorded.

### Charts

```yaml
        - id: activity
          type: chart
          title: Players online
          kind: area              # line | area | bar | stacked
          range: 7d               # how far back; hours or days, a day by default
          unit: " online"         # appended to every value shown
          max: 20                 # pins the top of the scale; omitted, it fits the data
          source: {series: [players]}
          refresh: {seconds: 60}
```

A chart is drawn on the server as SVG, the way the dashboard's own sparklines
are. Naming more than one series draws them together, each in the next of the
theme's `chart` colours, with a legend.

Pointing at one puts a rule down the column under the pointer, a mark on every
series at that moment, and a card giving the time and what each of them read.
Every word of that was written by Quasar before the page was sent — the same
rounding, the same unit, the same clock as the rest of the chart — and the
browser only arranges it, so a station never has a say in what a number says.

**A chart on `{series: ...}` runs nothing.** No worker starts, no script is
loaded: the points are in Quasar's own tables and the panel is a query. It is
the only panel on the page that can sit on a thirty-second refresh without
costing a process each time, which is why a station that wants a live graph
should sample from a hook and chart the series rather than ask its script on
every draw.

A series nobody has recorded yet draws as an empty chart saying so, not as an
error: that is what every series looks like for its first few minutes.

A form's source fills its fields: `{motd: 'A server', pvp: true}` puts a value
in each field it names, and leaves the declared default in the ones it does not.
A `select` can be filled with its choices as well as its value, for the lists
that are not knowable when the document is written:

```js
export function version_form() {
  const releases = quasar.http.get(MANIFEST).json().versions
    .filter(v => v.type === 'release').map(v => v.id)
  return { data: { version: { value: quasar.env.get('MINECRAFT_VERSION'), options: releases } } }
}
```

The field then offers exactly those, in that order. `options` in the document is
what it falls back to when the action says nothing about it — the offline case,
and the reason a `select` filled this way should still declare a few. Only the
presence of `options` makes a value a choice, so a form filled from an object
that happens to hold another object is unaffected.

### Actions

An action is an exported function. Its return value drives the interface:

```js
export function add_mod({ url }) {
  const meta = quasar.http.get(`https://api.modrinth.com/v2/project/${slug(url)}`).json()
  const file = quasar.http.get(meta.files[0].url).bytes()
  quasar.files.write(`data/mods/${meta.filename}`, file)
  return { toast: `${meta.title} installed`, refresh: ['mod_list'] }
}
```

| Key | Effect |
|---|---|
| `data` | Renders the panel that asked for it |
| `toast` | A transient message: green, with a tick |
| `warn` | A transient message for what went less well than it might have: orange, with a warning sign |
| `waiting` | Nothing yet, and there will be: the panel draws a spinner and asks again shortly |
| `error` | Rendered like any Quasar error, and the action counts as failed: red, with a crossed circle |
| `refresh` | Panel ids to re-fetch |
| `navigate` | Switch to a tab |
| `download` | A file in the application's folder to hand over, see below |
| `progress` | Run as a background job, see below |

**Waiting is not failing.** A panel that reads a service cannot read it while
the service is still coming up, and there are two ways to say so. Quasar says it
for you: when a panel's action fails and the application is not running, the
panel draws a spinner and asks again every couple of seconds rather than a red
card, so it connects itself when the container arrives instead of waiting for
somebody to reload. A script says it for the cases only the script can know —
a game server whose container is up and whose port will not answer for another
half a minute — by returning `{waiting: '...'}` instead of throwing.

Messages stack. Each one is appended to the corner of the page rather than
replacing the one before it, because three actions in a row are three things
worth knowing and the third crushing the first two makes all of them pointless.
A `toast` and a `warn` go on their own after a few seconds; an `error` waits to
be dismissed, since why something failed is what the operator came to read.

**Handing over a file.** An action may end with `{download: 'data/backups/…'}`,
which offers one file out of the application's own folder to whoever pressed the
button. A path rather than the bytes: a script that had to carry a two-gigabyte
archive through a JavaScript string to give it away would be a script that
cannot, and the file is already sitting in a folder Quasar can read.

```js
export function download_backup({ name }) {
  return { download: 'data/backups/' + name }
}
```

It is held to the `files` permission, exactly as `quasar.files.read` is: a
station hands over what it was allowed to touch and nothing else, and offering
anything outside its globs comes back as the same refusal reading a file would
have produced. From an ordinary action the browser starts fetching it as the
action returns; from a `long` one it appears as a link in the progress pane,
since nobody is waiting on that request and a download beginning by itself four
minutes later would be a surprise.

**Long actions.** Upgrading a server or downloading forty mods does not fit in
an HTTP request. Declaring `long: true` on the action runs it as a background
job and streams `quasar.progress(pct, message)` into a progress pane — the same
SSE machinery the deploy panel already uses. The tab shows the job is running,
survives a page reload, and keeps its output afterwards, because that output is
where the operator reads why the upgrade failed.

## Theme

A station may change how its own tabs look, within limits that make it
impossible to produce something unreadable.

```yaml
ui:
  theme:
    accent: "#3ba55d"       # hover derived, accent-text computed by contrast
    tint: 0.06              # the accent mixed into the surfaces
    chart: ["#8b5cf6", "#22d3ee", "#f59e0b"]

    font_display: {family: Minecraftia, src: "data:font/woff2;base64,..."}
    font_body: inherit      # inherit | display | mono
    scale: 1.05             # clamped to [0.9, 1.25]
    case: normal            # normal | upper
    tracking: normal        # normal | wide

    radius: 12px
    radius_badge: 0
    border_w: 2px
    density: normal         # compact | normal | roomy

    icon: "data:image/svg+xml;base64,..."
    banner: "data:image/webp;base64,..."
```

`themes.css` is already built for this: 22 variables, and the components are
styled once from them. A station theme is those variables redefined on the
element wrapping the station's tabs — not a second styling system.

**The palette is always inherited.** A station never sets `bg`, `surface`,
`text` or `border`. It sets an accent, a typeface, a shape, a density, an icon.
Everything else comes from whichever theme the operator chose, which is why a
station written on Nebula is still legible when it lands on Solarized and why
its author never has to think about it. `tint` is what buys identity back: the
accent mixed into the surfaces with `color-mix`, so the zone reads as *this*
station's, in the right direction on a dark ground and on a light one, from a
single value.

**Derived, not trusted.** `accent-hover` comes from `color-mix`; `accent-text`
is computed in Go, black or white by real contrast against the declared accent.
A station cannot ship an unreadable pair, which is exactly the care the comment
on `--accent` in Nebula already takes.

**Also not overridable, and documented rather than discovered:** `ok`, `warn`,
`err` and `info` — a red that means "stopped" everywhere else in Quasar must not
mean something else inside a station; and `log-bg` with the sixteen ANSI colours
— rendering logs is Quasar's job.

**Scope stops at the station's block.** A station renders as one block with its
own tab strip at the top of the application's page; the navigation, the top bar
and every standard section below it — Build, Routing, TLS, Storage, Environment
— keep the operator's theme. A station is a shareable program: one that can
repaint Quasar's own chrome can draw a convincing "update available" or a
convincing login screen.

**Fonts are embedded, never fetched.** `src` takes a `data:` URI, capped at
512 KB. The comment at the top of `themes.css` is explicit that the shipped
typefaces live inside the binary because the dashboard has to render identically
on a machine with no route to the public internet, which is the normal case for
the servers Quasar runs on. A station that pulls a font from a CDN breaks that,
and leaks every page view to whoever hosts it.

## Hooks

```yaml
hooks:
  after_deploy:   {action: sync_config}
  on_start:       {action: announce}
  on_stop:        {action: save_world}
  on_health_fail: {action: collect_diagnostics}
  every:
    - {minutes: 60, action: check_mod_updates}
```

**Hooks never block.** A failing `after_deploy` is reported in the deploy panel
and the audit log; it does not fail the deployment. Third-party code on the
critical path of a deployment is how a working site goes down for a reason
nobody can find.

Scheduled actions run only while the application is running: a stopped server
has nothing to poll, and a fleet of stopped applications quietly burning CPU
every minute is a bug people discover from their hosting bill.

## Installing, updating, reverting

**Settings → Stations**, beside Catalogues, sharing its import machinery: paste
a document or give a URL, with a re-fetch button and no background job. The page
lists each installed station with its source, its version, the permissions it
holds and the applications running it.

A station's cards appear on a **Stations** page of their own in the navigation,
never mixed into the ~60 catalogue entries. That separation is the whole point
of the earlier distinction: the catalogue is where an operator browses software,
the Stations page is where they browse programs somebody wrote for them.

**Re-fetching updates the surface, never what is running.** A new revision
replaces the UI, the theme, the script and the hooks for every application using
that station — which is the entire point: fix the mod manager once, three
servers get the fix. It does not touch the deployed compose file, the image or
the environment. Changing what runs stays an explicit *Update* on the
application, per application, exactly as it is today.

Every accepted revision is kept. If a new one breaks a panel, reverting is one
click, and the application never stopped.

## Audit

Stations write to the existing audit log, in the same style as `storage.*`:

`station.import`, `station.permissions.grant`, `station.revert`,
`station.action`, `station.exec`, `station.files.write`, `station.files.delete`,
`station.env.write`, `station.lifecycle`, `station.http.external`,
`station.hook.fail`.

Enough that the question "what did this station do to my server" has an answer
that is not "look at the logs and guess".

## Threat model, in one paragraph

A station is untrusted code an operator chose to install, running with an
explicit, enumerated set of capabilities on a machine they own. The defences, in
order of how much they carry: the script runs in a disposable process holding no
socket, no disk and no network, so every capability crosses a boundary the
parent polices and an escape from the interpreter buys nothing; the script
cannot emit markup, so there is no injection surface into the dashboard; it gets
no capability it did not declare and the operator did not accept; every
capability is narrowed by name — services for `exec`, globs for `files`, hosts
for `net.external`, keys for `env`, verbs for `lifecycle`; paths are resolved
through symlinks before being checked; time and memory are bounded from outside
the process being bounded; the theme cannot reach the chrome; capabilities
cannot be added by a silent update; and everything privileged is logged. What
this does *not* defend against is an operator who accepts `exec` from a hostile
author — that is root on the container by design, and the install screen says so
in those words.

## Worked example

A complete Minecraft station, short enough to read in one go.

```yaml
schema: 1
id: minecraft-station
name: Minecraft
description: Fabric or Forge server, with mod management and a live map
version: "1.0.0"

deploy:
  deploy_type: compose
  compose_service: minecraft
  port: 25565
  raw: true
  app_name: "Minecraft {{VERSION}} ({{TYPE}})"
  subdomain: "mc-{{TYPE}}-{{VERSION}}"
  params:
    - {name: TYPE, label: Mod loader, kind: select, default: FABRIC,
       options: [FABRIC, FORGE, NEOFORGE]}
    - {name: VERSION, label: Version, kind: select, default: "1.20.1",
       options: ["1.20.1"], options_from: official_versions}
    - {name: HOST_PORT, label: Port, kind: port, default: "25565"}
    - {name: MEMORY, label: RAM, kind: select, default: "4G",
       options: ["2G", "4G", "8G"]}
  env: |
    TYPE={{TYPE}}
    MINECRAFT_VERSION={{VERSION}}
    HOST_PORT={{HOST_PORT}}
    MEMORY={{MEMORY}}
    RCON_PASSWORD={{RANDOM}}
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
          ENABLE_RCON: "true"
          RCON_PASSWORD: ${RCON_PASSWORD}
        volumes:
          - ./data:/data
        restart: unless-stopped

permissions:
  exec:         {services: [minecraft]}
  files:        {paths: ["data/mods/**", "data/server.properties", "data/ops.json"]}
  env:          {read: [MINECRAFT_VERSION, TYPE], write: [MINECRAFT_VERSION]}
  net.external: {allow: ["api.modrinth.com", "cdn.modrinth.com", "piston-meta.mojang.com"]}
  lifecycle:    [restart, redeploy]

ui:
  theme:
    accent: "#5b8c3a"
    tint: 0.05
    radius: 2px
    case: upper
  tabs:
    - id: overview
      name: Server
      panels:
        - {id: players, type: stat, title: Players online, source: {action: player_count}, width: third}
        - {id: version, type: keyvalue, title: Build, source: {action: build_info}, width: third}
        - id: console
          type: form
          title: Console
          width: third
          fields: [{name: cmd, label: Command, placeholder: "say hello"}]
          submit: {label: Send, action: rcon}

    - id: mods
      name: Mods
      panels:
        - id: mod_list
          type: table
          title: Installed mods
          source: {action: list_mods}
          columns:
            - {key: name,    label: Mod}
            - {key: version, label: Version, align: right}
            - {key: update,  label: "", type: badge}
          row_actions:
            - {label: Update, action: update_mod, tone: accent}
            - {label: Remove, action: remove_mod, tone: err, confirm: "Remove {{name}}?"}
          empty: "No mods installed. Paste a Modrinth link below."
        - id: add
          type: form
          title: Install a mod
          fields: [{name: url, label: Modrinth URL}]
          submit: {label: Install, action: add_mod}

    - id: settings
      name: Settings
      panels:
        - id: props
          type: form
          title: server.properties
          source: {action: read_props}
          fields:
            - {name: motd,       label: MOTD}
            - {name: difficulty, label: Difficulty, type: select,
               options: [peaceful, easy, normal, hard]}
            - {name: pvp,        label: PvP, type: toggle}
            - {name: max_players, label: Max players, type: number}
          submit: {label: Save and restart, action: write_props}

hooks:
  every:
    - {minutes: 360, action: check_mod_updates}

script: |
  const MODS = 'data/mods'

  function rconExec(cmd) {
    const r = quasar.exec('minecraft', ['rcon-cli', cmd])
    if (r.code !== 0) throw new Error(r.stderr || 'rcon failed')
    return r.stdout
  }

  export function player_count() {
    const out = rconExec('list')                       // "There are 3 of a max of 20..."
    const m = out.match(/There are (\d+) of a max of (\d+)/)
    return { data: { value: m ? m[1] : '—', suffix: m ? `/ ${m[2]}` : '' } }
  }

  export function build_info() {
    return { data: [
      { key: 'Loader',  value: quasar.app.params.TYPE },
      { key: 'Version', value: quasar.env.get('MINECRAFT_VERSION') },
      { key: 'Status',  value: quasar.app.status },
    ]}
  }

  // What the install form offers for VERSION. It runs before this application
  // exists, so it reaches the network and nothing else.
  export function official_versions() {
    const m = quasar.http.get('https://piston-meta.mojang.com/mc/game/version_manifest_v2.json').json()
    return {
      options: m.versions.filter(v => v.type === 'release').map(v => v.id),
      default: m.latest.release,          // a new server starts on the newest
    }
  }

  export function rcon({ cmd }) {
    return { toast: rconExec(cmd).trim() || 'Sent' }
  }

  export function list_mods() {
    const known = quasar.store.get('updates') || {}
    return { data: quasar.files.list(MODS)
      .filter(f => f.name.endsWith('.jar'))
      .map(f => ({
        name:    f.name.replace(/-[\d.]+\.jar$/, ''),
        version: (f.name.match(/-([\d.]+)\.jar$/) || [,'?'])[1],
        update:  known[f.name] ? { label: 'update', tone: 'warn' } : null,
        file:    f.name,
      })) }
  }

  export function add_mod({ url }) {
    const slug = url.replace(/\/$/, '').split('/').pop()
    const versions = quasar.http.get(
      `https://api.modrinth.com/v2/project/${slug}/version` +
      `?loaders=["${quasar.app.params.TYPE.toLowerCase()}"]` +
      `&game_versions=["${quasar.env.get('MINECRAFT_VERSION')}"]`).json()

    if (!versions.length)
      return { error: `No build of ${slug} for ${quasar.app.params.TYPE} ${quasar.env.get('MINECRAFT_VERSION')}.` }

    const file = versions[0].files.find(f => f.primary)
    quasar.files.write(`${MODS}/${file.filename}`, quasar.http.get(file.url).bytes())
    return { toast: `${slug} installed — restart to load it`, refresh: ['mod_list'] }
  }

  export function update_mod({ file, name }) {
    const next = (quasar.store.get('updates') || {})[file]
    if (!next) return { toast: `${name} is already on the newest build` }

    quasar.files.write(`${MODS}/${next.filename}`, quasar.http.get(next.url).bytes())
    if (next.filename !== file) quasar.files.delete(`${MODS}/${file}`)
    return { toast: `${name} updated — restart to load it`, refresh: ['mod_list'] }
  }

  export function remove_mod({ file }) {
    quasar.files.delete(`${MODS}/${file}`)
    return { toast: 'Removed', refresh: ['mod_list'] }
  }

  export function read_props() {
    const p = parseProps(quasar.files.read('data/server.properties'))
    return { data: {
      motd: p.motd, difficulty: p.difficulty,
      pvp: p.pvp === 'true', max_players: Number(p['max-players']),
    }}
  }

  export function write_props(v) {
    const p = parseProps(quasar.files.read('data/server.properties'))
    Object.assign(p, {
      motd: v.motd, difficulty: v.difficulty,
      pvp: String(v.pvp), 'max-players': String(v.max_players),
    })
    quasar.files.write('data/server.properties',
      Object.entries(p).map(([k, x]) => `${k}=${x}`).join('\n'))
    quasar.app.restart()
    return { toast: 'Saved — server restarting' }
  }

  export function check_mod_updates() {
    // Runs every 6 h while the server is up; results surface as badges above.
    quasar.store.set('updates', {})
  }

  function parseProps(text) {
    const out = {}
    for (const line of text.split('\n')) {
      if (!line || line.startsWith('#')) continue
      const i = line.indexOf('=')
      out[line.slice(0, i)] = line.slice(i + 1)
    }
    return out
  }
```

## Deliberately deferred

Named so they are decisions rather than omissions.

- **Raw CSS**, even scoped and allowlisted. Tokens and variants first; if they
  prove too narrow, the gap will be a specific missing token, which is a better
  thing to add than an escape hatch.
- **Station-owned palettes.** Inheriting the operator's ground is what makes
  legibility free.
- **Attaching a station to an application deployed some other way.**
- **Languages other than JavaScript**, via WASM. It would make a station a
  binary, and stations are meant to be read before they are trusted.
- **A public station registry.** Import by URL is the whole distribution story
  for now.
- **Stations declaring their own routes or subdomains** beyond what the deploy
  block already does.
