package server

import (
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzhttp"
)

// compressed gzips what the dashboard sends, for a browser that says it can
// read it.
//
// It is here rather than as a middleware on Traefik's router because an
// update only replaces the image: a label added to the compose file would
// reach a new install and never an existing one. The panels polled every few
// seconds are the bulk of what goes out, and they are HTML, which gzip takes
// to a fraction.
//
// Text only. A server-sent event stream is never buffered behind a compressor
// — its lines have to arrive as they are written — and fonts, images and
// archives are compressed already.
func compressed(h http.Handler) http.Handler {
	wrap, err := gzhttp.NewWrapper(gzhttp.ContentTypeFilter(compressible))
	if err != nil {
		// Only a malformed option fails, and the options are fixed here.
		return h
	}
	return wrap(h)
}

func compressible(contentType string) bool {
	ct, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(contentType)), ";")
	switch {
	case ct == "text/event-stream":
		return false
	case strings.HasPrefix(ct, "text/"):
		return true
	}
	switch ct {
	case "application/json", "application/javascript", "image/svg+xml", "application/xml":
		return true
	}
	return false
}
