package db

import (
	"database/sql"
	"time"
)

// Task is a command run inside an app's container, either on demand or on a
// fixed interval.
type Task struct {
	ID              int64
	AppID           string
	Command         string
	IntervalMinutes int // 0 = manual only
	LastRun         sql.NullTime
	LastStatus      string // "", "success", "failed"
	LastOutput      string
}

// Due reports whether a scheduled task should run now.
func (t *Task) Due(now time.Time) bool {
	if t.IntervalMinutes <= 0 {
		return false
	}
	if !t.LastRun.Valid {
		return true
	}
	return now.Sub(t.LastRun.Time) >= time.Duration(t.IntervalMinutes)*time.Minute
}

func InsertTask(db *sql.DB, t *Task) error {
	_, err := db.Exec("INSERT INTO tasks (app_id, command, interval_minutes) VALUES (?, ?, ?)",
		t.AppID, t.Command, t.IntervalMinutes)
	return err
}

func DeleteTask(db *sql.DB, id int64) error {
	_, err := db.Exec("DELETE FROM tasks WHERE id = ?", id)
	return err
}

func DeleteTasksForApp(db *sql.DB, appID string) error {
	_, err := db.Exec("DELETE FROM tasks WHERE app_id = ?", appID)
	return err
}

func GetTask(db *sql.DB, id int64) (*Task, error) {
	return scanTask(db.QueryRow("SELECT id, app_id, command, interval_minutes, last_run, last_status, last_output FROM tasks WHERE id = ?", id))
}

func RecordTaskRun(db *sql.DB, id int64, status, output string) error {
	if len(output) > 8192 {
		output = output[:8192] + "\n… (truncated)"
	}
	_, err := db.Exec("UPDATE tasks SET last_run = ?, last_status = ?, last_output = ? WHERE id = ?",
		time.Now(), status, output, id)
	return err
}

func scanTask(row interface{ Scan(...any) error }) (*Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.AppID, &t.Command, &t.IntervalMinutes, &t.LastRun, &t.LastStatus, &t.LastOutput)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func listTasks(db *sql.DB, query string, args ...any) ([]*Task, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func ListTasks(db *sql.DB, appID string) ([]*Task, error) {
	return listTasks(db, "SELECT id, app_id, command, interval_minutes, last_run, last_status, last_output FROM tasks WHERE app_id = ? ORDER BY id", appID)
}

func ListAllScheduledTasks(db *sql.DB) ([]*Task, error) {
	return listTasks(db, "SELECT id, app_id, command, interval_minutes, last_run, last_status, last_output FROM tasks WHERE interval_minutes > 0")
}
