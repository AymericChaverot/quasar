// Package web embeds the HTML templates and static assets into the binary.
package web

import "embed"

// static/app.css is compiled from styles/ by the Tailwind CLI, and committed:
// the build itself needs nothing but Go. See styles/README.md.
//go:generate tailwindcss -i styles/app.css -o static/app.css

//go:embed templates static
var Files embed.FS
