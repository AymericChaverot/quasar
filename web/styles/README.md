# Stylesheets

Every sheet of the dashboard lives here, and none of them is served as it is:
`app.css` imports Tailwind and the rest in cascade order, and the Tailwind CLI
compiles the lot into `web/static/app.css`, the one stylesheet the layout links.
That file is committed, so building Quasar still takes nothing but Go.

After changing a sheet here, or a class name in a template, a script or Go
code, compile it again from the repository root:

```sh
go generate ./web
```

which runs `tailwindcss -i styles/app.css -o static/app.css` in `web/`. It wants
the standalone Tailwind CLI on the `PATH`, at the version CI checks the file
with (`TAILWIND_VERSION` in `.github/workflows/ci.yml`, currently **4.3.3**):
download `tailwindcss-<os>-<arch>` from
<https://github.com/tailwindlabs/tailwindcss/releases> and rename it.

CI compiles the file again and fails when it comes out different from the
committed one, so a forgotten `go generate` is caught before it ships a page
with missing styles.

Utilities are only generated for class names written out whole somewhere
under `web/templates`, `web/static/js` or `internal` (the `@source` lines in
`app.css`). A class assembled at run time from pieces is never seen.
