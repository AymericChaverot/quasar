// Package web embeds the HTML templates and static assets into the binary.
package web

import "embed"

//go:embed templates static
var Files embed.FS
