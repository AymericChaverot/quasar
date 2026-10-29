package main

import (
	"context"
	"net/http"
	"testing"
)

// Headers and idle connections are bounded; bodies and responses are not, so
// uploads and streams are left alone.
func TestHTTPServerTimeouts(t *testing.T) {
	hs := newHTTPServer(http.NotFoundHandler(), context.Background())
	if hs.ReadHeaderTimeout == 0 || hs.IdleTimeout == 0 {
		t.Errorf("ReadHeaderTimeout %v, IdleTimeout %v: a slow client can hold a connection forever",
			hs.ReadHeaderTimeout, hs.IdleTimeout)
	}
	if hs.ReadTimeout != 0 || hs.WriteTimeout != 0 {
		t.Errorf("ReadTimeout %v, WriteTimeout %v would cut uploads and streams short",
			hs.ReadTimeout, hs.WriteTimeout)
	}
}
