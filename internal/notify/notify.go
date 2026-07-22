// Package notify pushes platform events to a user-configured webhook.
// The payload carries both "content" (Discord) and "text" (Slack-compatible)
// keys so most chat webhooks work without extra configuration.
package notify

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"quasar/internal/db"
)

var client = &http.Client{Timeout: 10 * time.Second}

// Send posts a message to the configured webhook. Failures are logged, never
// fatal: notifications must not break deploys or monitoring.
func Send(database *sql.DB, msg string) {
	url := db.GetSetting(database, db.SettingNotifyURL)
	if url == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{
		"content": msg,
		"text":    msg,
	})
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("notify: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		log.Printf("notify: webhook returned %s", resp.Status)
	}
}
