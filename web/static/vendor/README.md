# Vendored scripts

Served from the binary rather than from a CDN, so the dashboard works on a
server with no way out to the internet, and nothing outside it learns when it
is used. Each file is the published build, unmodified but for a dropped
source-map comment, and carries its version in its name: the name is what the
cache is keyed on (see `staticAssets`), so an upgrade is a new file.

| File | Package | Licence |
| --- | --- | --- |
| `htmx-2.0.11.min.js` | `htmx.org@2.0.11` `dist/htmx.min.js` | 0BSD |
| `htmx-ext-sse-2.2.4.js` | `htmx-ext-sse@2.2.4` `dist/sse.js` | 0BSD |
| `xterm-6.0.0.js`, `xterm-6.0.0.css` | `@xterm/xterm@6.0.0` | MIT |
| `xterm-addon-fit-0.11.0.js` | `@xterm/addon-fit@0.11.0` | MIT |
