package server

import (
	"net/http"

	"quasar/internal/db"
	"quasar/internal/vps"
)

func (s *Server) handleSystemPartial(w http.ResponseWriter, r *http.Request) {
	stats, _ := vps.Collect(s.cfg.HostRootPath)
	s.renderPartial(w, "system_stats", stats)
}

func (s *Server) handleAppsPartial(w http.ResponseWriter, r *http.Request) {
	apps, err := db.ListApps(s.db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	views := make([]AppView, 0, len(apps))
	for _, a := range apps {
		views = append(views, s.appView(r.Context(), a))
	}
	s.renderPartial(w, "apps_table", views)
}

// handleDeployFields swaps in the form fields specific to the chosen deploy type.
func (s *Server) handleDeployFields(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("type")
	switch t {
	case "image", "git", "compose":
		s.renderPartial(w, "deploy_fields_"+t, map[string]any{})
	default:
		http.Error(w, "unknown type", http.StatusBadRequest)
	}
}

func (s *Server) handleAppStatsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	if a.DeployType == "compose" {
		s.renderPartial(w, "container_stats_unavailable", nil)
		return
	}
	stats, err := s.dock.Stats(r.Context(), a)
	if err != nil {
		s.renderPartial(w, "container_stats_unavailable", nil)
		return
	}
	s.renderPartial(w, "container_stats", stats)
}

func (s *Server) handleAppStatusPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	s.renderPartial(w, "app_status_panel", s.appView(r.Context(), a))
}

// DeploymentView adds template-friendly context to a deployment row.
type DeploymentView struct {
	*db.Deployment
	CanRollback bool
}

func (s *Server) handleAppDeploymentsPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	deps, err := db.ListDeployments(s.db, a.ID, 10)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The most recent successful tag is what's currently running; older
	// successful tags with a distinct image are rollback candidates.
	currentTag := ""
	for _, d := range deps {
		if d.Status == "success" && d.ImageTag != "" {
			currentTag = d.ImageTag
			break
		}
	}
	views := make([]DeploymentView, 0, len(deps))
	seen := map[string]bool{}
	for _, d := range deps {
		v := DeploymentView{Deployment: d}
		if d.Status == "success" && d.ImageTag != "" && d.ImageTag != currentTag && !seen[d.ImageTag] {
			v.CanRollback = a.DeployType != "compose"
			seen[d.ImageTag] = true
		}
		views = append(views, v)
	}
	s.renderPartial(w, "deployments", map[string]any{"AppID": a.ID, "Deployments": views})
}
