package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"quasar/internal/db"
)

// An application's environment over the API: the dotenv text the dashboard's
// editor shows, comments and order included, so that a client which reads it,
// changes a line and writes it back loses nothing else.
//
// Reading it is the one thing in the API that hands over secrets, so it takes
// more than an admin token: an admin's personal token. A service token lives
// in a pipeline's configuration, which is a weaker place than the encrypted
// column the values sit in; a personal token is the admin themselves, who can
// read the same values on the dashboard. Writing it takes an admin token of
// either kind, since a pipeline setting a variable learns nothing it did not
// already know.

// apiEnv is an environment as the API carries it.
type apiEnv struct {
	Content string `json:"content"`
}

// apiEnvMaxBytes bounds a written environment. The dashboard's own form has
// no lower one in practice; this is about refusing a runaway body, not about
// any real environment.
const apiEnvMaxBytes = 1 << 20

func (s *Server) handleAPIEnvGet(w http.ResponseWriter, r *http.Request) {
	if id := apiIdentity(r); id == nil || !id.Personal() {
		writeAPIError(w, http.StatusForbidden,
			"a service token cannot read environments; use a personal token")
		return
	}
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	// Who read the values, never the values.
	s.auditAPI(r, "app.env-read", a.Name, "")
	writeJSON(w, http.StatusOK, apiEnv{Content: a.EnvContent})
}

// handleAPIEnvPut replaces the environment, as saving the dashboard's editor
// does: stored, and written beside the application for its next deploy. Like
// there, it does not redeploy; what is running keeps the values it started
// with until it is deployed again.
func (s *Server) handleAPIEnvPut(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	var body struct {
		Content *string `json:"content"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiEnvMaxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "the environment is larger than 1 MiB")
			return
		}
		writeAPIError(w, http.StatusBadRequest, `the body must be {"content": "<dotenv text>"}`)
		return
	}
	if body.Content == nil {
		writeAPIError(w, http.StatusBadRequest, `the body must be {"content": "<dotenv text>"}`)
		return
	}
	a.EnvContent = strings.ReplaceAll(*body.Content, "\r\n", "\n")
	if err := db.UpdateAppEnv(s.db, s.keyring, a.ID, a.EnvContent); err != nil {
		apiInternalError(w, r, err)
		return
	}
	if err := s.dock.WriteEnvFile(a); err != nil {
		apiInternalError(w, r, err)
		return
	}
	s.auditAPI(r, "app.env-change", a.Name, "")
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "app": a.ID})
}
