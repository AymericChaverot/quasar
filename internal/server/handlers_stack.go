package server

import (
	"net/http"
	"path/filepath"

	"quasar"
	"quasar/internal/version"
)

// handleSystemStackPartial fills the notice at the top of the System page:
// what the running system stack lacks compared with the compose file this
// version shipped with, and how to bring it up to date. Three container
// inspections over the socket proxy, which is why it is not rendered with the
// page.
func (s *Server) handleSystemStackPartial(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "system_stack", map[string]any{
		"Drift":   s.dock.StackDrift(r.Context(), quasar.ComposeFile, filepath.Dir(s.cfg.AppsDir)),
		"Version": version.Version,
	})
}
