package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"quasar/internal/db"
)

func (s *Server) handleTasksPartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	tasks, _ := db.ListTasks(s.db, a.ID)
	s.renderPartial(w, "tasks", map[string]any{"AppID": a.ID, "Tasks": tasks, "IsAdmin": s.isAdmin(r)})
}

func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	command := strings.TrimSpace(r.FormValue("command"))
	if command == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}
	interval := 0
	if v := r.FormValue("interval_minutes"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			interval = n
		}
	}
	if err := db.InsertTask(s.db, &db.Task{AppID: a.ID, Command: command, IntervalMinutes: interval}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.audit(r, "task.add", a.Name, command)
	s.handleTasksPartial(w, r)
}

func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	if id, err := strconv.ParseInt(r.PathValue("task"), 10, 64); err == nil {
		if t, err := db.GetTask(s.db, id); err == nil && t.AppID == a.ID {
			db.DeleteTask(s.db, id)
			s.audit(r, "task.delete", a.Name, t.Command)
		}
	}
	s.handleTasksPartial(w, r)
}

// handleTaskRun executes a task immediately and re-renders the task list.
func (s *Server) handleTaskRun(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("task"), 10, 64)
	if err != nil {
		http.Error(w, "bad task id", http.StatusBadRequest)
		return
	}
	t, err := db.GetTask(s.db, id)
	if err != nil || t.AppID != a.ID {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	out, runErr := s.dock.RunCommand(ctx, a, t.Command)
	status := "success"
	if runErr != nil {
		status = "failed"
		db.RecordTaskRun(s.db, t.ID, status, out+"\n"+runErr.Error())
	} else {
		db.RecordTaskRun(s.db, t.ID, status, out)
	}
	s.audit(r, "task.run", a.Name, t.Command+" ("+status+")")
	s.handleTasksPartial(w, r)
}
