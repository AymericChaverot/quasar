package server

import (
	"fmt"
	"io"
)

// sse writes one server-sent event and reports whether the reader is still
// there.
//
// A write to a browser that navigated away fails, and the stream that keeps
// writing anyway spends the rest of its life talking to a closed socket. The
// boolean is what lets a handler leave at the first sign of that, rather than
// waiting for the request context to catch up.
func sse(w io.Writer, format string, args ...any) bool {
	_, err := fmt.Fprintf(w, format, args...)
	return err == nil
}
