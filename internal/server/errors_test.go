package server

import (
	"bytes"
	"errors"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
)

// A failure inside the server is logged in full and answered in general: the
// query, path or address in the error stays out of the browser.
func TestInternalErrorKeepsTheDetailInTheLog(t *testing.T) {
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(prev)

	secret := errors.New("open /opt/quasar/storage/database.sqlite: disk I/O error")
	for _, answer := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) {
			internalError(w, httptest.NewRequest("POST", "/apps/x/env", nil), secret)
		},
		func(w *httptest.ResponseRecorder) {
			apiInternalError(w, httptest.NewRequest("GET", "/api/v1/apps", nil), secret)
		},
	} {
		w := httptest.NewRecorder()
		answer(w)
		if w.Code != 500 || strings.Contains(w.Body.String(), "/opt/quasar") {
			t.Errorf("answered %d %q", w.Code, w.Body.String())
		}
	}
	if !strings.Contains(logged.String(), "POST /apps/x/env: open /opt/quasar") {
		t.Errorf("the log does not say what failed: %q", logged.String())
	}
}
