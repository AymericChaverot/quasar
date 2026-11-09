package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"quasar/internal/db"
	"quasar/internal/event"
)

// apiCreateMaxBytes bounds a create request: a compose file and an
// environment fit in far less.
const apiCreateMaxBytes = 1 << 20

// apiNewApp is what creating an application takes over the API: the fields of
// the dashboard's form, under the same names.
type apiNewApp struct {
	Name           string  `json:"name"`
	Subdomain      string  `json:"subdomain"`
	DeployType     string  `json:"deploy_type"`
	ImageRef       string  `json:"image_ref"`
	GitURL         string  `json:"git_url"`
	GitBranch      string  `json:"git_branch"`
	ComposeYAML    string  `json:"compose_yaml"`
	ComposeService string  `json:"compose_service"`
	Port           int     `json:"port"`
	EnvContent     string  `json:"env_content"`
	DataMount      string  `json:"data_mount"`
	CustomDomains  string  `json:"custom_domains"`
	CPULimit       float64 `json:"cpu_limit"`
	MemLimitMB     int64   `json:"mem_limit_mb"`
}

// handleAPIAppCreate creates an application, as the dashboard's form does,
// and starts its first deploy — except for one deployed from a local folder,
// which waits for its first upload. It answers 201 with the application; the
// caller polls it for how the deploy goes.
func (s *Server) handleAPIAppCreate(w http.ResponseWriter, r *http.Request) {
	var body apiNewApp
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiCreateMaxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "the request is larger than 1 MiB")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "the body must be a JSON application: "+err.Error())
		return
	}
	a := &db.App{
		Name:           strings.TrimSpace(body.Name),
		Subdomain:      strings.ToLower(strings.TrimSpace(body.Subdomain)),
		DeployType:     strings.TrimSpace(body.DeployType),
		ImageRef:       strings.TrimSpace(body.ImageRef),
		GitURL:         strings.TrimSpace(body.GitURL),
		GitBranch:      strings.TrimSpace(body.GitBranch),
		ComposeYAML:    body.ComposeYAML,
		ComposeService: strings.TrimSpace(body.ComposeService),
		EnvContent:     strings.ReplaceAll(body.EnvContent, "\r\n", "\n"),
		DataMount:      strings.TrimSpace(body.DataMount),
		CustomDomains:  normalizeDomains(body.CustomDomains),
		Port:           80,
		CPULimit:       body.CPULimit,
		MemLimitMB:     body.MemLimitMB,
	}
	if body.Port != 0 {
		if body.Port < 1 || body.Port > 65535 {
			writeAPIError(w, http.StatusBadRequest, "port must be between 1 and 65535")
			return
		}
		a.Port = body.Port
	}
	if a.CPULimit < 0 || a.MemLimitMB < 0 {
		writeAPIError(w, http.StatusBadRequest, "limits cannot be negative")
		return
	}
	if msg := s.insertApp(a); msg != "" {
		status := http.StatusBadRequest
		if strings.HasPrefix(msg, "database error") {
			log.Printf("api: creating %s: %s", a.Name, msg)
			writeAPIError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if strings.Contains(msg, "already used") || strings.Contains(msg, "already published") {
			status = http.StatusConflict
		}
		writeAPIError(w, status, msg)
		return
	}
	s.auditAPI(r, "app.create", a.Name, a.Subdomain+" ("+a.DeployType+")")
	writeJSON(w, http.StatusCreated, s.apiAppFrom(r, a))
}

// handleAPIAppDelete deletes an application with everything it ran and kept,
// as the dashboard's delete does. A deploy in progress is let finish first:
// it would bring back containers the delete had just removed.
func (s *Server) handleAPIAppDelete(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("id"))
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "application not found")
		return
	}
	if d := s.dock.Deploying(a.ID); d != nil && d.Running {
		writeAPIError(w, http.StatusConflict, "a deploy is in progress")
		return
	}
	if err := s.removeApp(a); err != nil {
		log.Printf("api: deleting %s: %v", a.Name, err)
		writeAPIError(w, http.StatusInternalServerError, a.Name+" was not deleted: "+err.Error())
		return
	}
	s.auditAPI(r, "app.delete", a.Name, a.Subdomain+" ("+a.DeployType+")")
	event.Info("app", a.Name, "deleted", "by "+apiActor(apiIdentity(r)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "app": a.ID})
}
