package server

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

// The Content-Security-Policy every page of the dashboard is sent with.
//
// What it is for is the day something gets text into a page that it should
// not have — an application's name, a log line, a station's label — past the
// escaping that is meant to stop it. With the policy in place that text can
// still be shown, but nothing in it runs: scripts come from the dashboard
// itself, or are the two inline ones the page was drawn with, which carry
// this response's nonce. Nothing is evaluated from a string, nothing is sent
// anywhere but back here, and the page cannot be framed by another site.
//
// Styles are the exception, and on purpose: the templates set colours with
// style attributes throughout, a station's theme is an inline sheet, and
// htmx adds one of its own. A style cannot run anything.
//
// Tailwind's stylesheet is still compiled in the browser, by a script from
// unpkg; it is allowed by its exact address until it is compiled at build
// time instead.
const tailwindRuntime = "https://unpkg.com/@tailwindcss/browser@4.3.3"

// pagePolicy sets the policy and its companion headers on a page's response,
// and returns the nonce its inline scripts carry.
func pagePolicy(w http.ResponseWriter) string {
	nonce := newNonce()
	h := w.Header()
	h.Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'self'",
		"script-src 'self' 'nonce-" + nonce + "' " + tailwindRuntime,
		"style-src 'self' 'unsafe-inline'",
		// A station's typeface is embedded as a data: URI, and so is the
		// two-factor QR code.
		"font-src 'self' data:",
		"img-src 'self' data:",
		"connect-src 'self'",
		// A station's own pages are framed from here.
		"frame-src 'self'",
		"frame-ancestors 'self'",
		"form-action 'self'",
		"base-uri 'none'",
		"object-src 'none'",
	}, "; "))
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	return nonce
}

func newNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read does not fail; see its documentation
	return base64.RawStdEncoding.EncodeToString(b)
}
