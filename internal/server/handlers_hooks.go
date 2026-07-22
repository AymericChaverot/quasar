package server

import (
	"crypto/subtle"
	"net/http"

	"quasar/internal/db"
)

// handleWebhook triggers a redeploy from an unauthenticated but secret URL,
// meant to be called by GitHub/GitLab push webhooks:
//
//	POST https://admin.<domain>/hooks/<app-id>/<secret>
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	a, err := db.GetApp(s.db, r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	secret := r.PathValue("secret")
	if a.WebhookSecret == "" ||
		subtle.ConstantTimeCompare([]byte(secret), []byte(a.WebhookSecret)) != 1 {
		http.NotFound(w, r) // do not reveal that the app exists
		return
	}
	if d := s.dock.Deploying(a.ID); d != nil && d.Running {
		http.Error(w, "a deploy is already in progress", http.StatusTooManyRequests)
		return
	}
	s.dock.DeployAsync(a, "webhook")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte("deploy triggered\n"))
}
