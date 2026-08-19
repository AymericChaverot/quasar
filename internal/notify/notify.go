// Package notify pushes platform events to the channels the operator
// configured: a chat webhook, ntfy, and email over SMTP. Every channel is
// attempted, so one broken destination does not silence the others.
package notify

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"quasar/internal/db"
)

var client = &http.Client{Timeout: 10 * time.Second}

// Send delivers a message over every configured channel. Failures are logged,
// never fatal: notifications must not break deploys or monitoring.
//
// A failure on one channel is also recorded in the audit trail, because the
// previous behaviour — a log line nobody reads — meant a webhook that had been
// broken for weeks looked exactly like a platform with nothing to report.
func Send(database *sql.DB, msg string) {
	var failures []string
	sent := 0

	if url := db.GetSetting(database, db.SettingNotifyURL); url != "" {
		if err := sendWebhook(url, msg); err != nil {
			failures = append(failures, "webhook: "+err.Error())
		} else {
			sent++
		}
	}
	if url := db.GetSetting(database, db.SettingNtfyURL); url != "" {
		if err := sendNtfy(url, msg); err != nil {
			failures = append(failures, "ntfy: "+err.Error())
		} else {
			sent++
		}
	}
	if cfg := smtpSettings(database); cfg.usable() {
		if err := sendMail(cfg, msg); err != nil {
			failures = append(failures, "email: "+err.Error())
		} else {
			sent++
		}
	}

	for _, f := range failures {
		log.Printf("notify: %s", f)
	}
	// Only recorded when nothing got through: if one channel worked, the
	// operator already knows, and the audit trail is not a place to accumulate
	// one entry per transient hiccup.
	if sent == 0 && len(failures) > 0 {
		if err := db.RecordAudit(database, db.AuditEntry{
			Actor:  db.ActorSystem,
			Action: "notify.failed",
			Detail: strings.Join(failures, "; "),
		}); err != nil {
			log.Printf("audit: recording the failed notification: %v", err)
		}
	}
}

// sendWebhook posts to a Discord or Slack-compatible endpoint. The payload
// carries both "content" (Discord) and "text" (Slack) so most chat webhooks
// work without extra configuration.
func sendWebhook(url, msg string) error {
	body, _ := json.Marshal(map[string]string{"content": msg, "text": msg})
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("returned %s", resp.Status)
	}
	return nil
}

// sendNtfy publishes to an ntfy topic URL, which takes the message as a plain
// text body.
func sendNtfy(url, msg string) error {
	req, err := http.NewRequest("POST", url, strings.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Title", "Quasar")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("returned %s", resp.Status)
	}
	return nil
}

// mailConfig is the SMTP destination assembled from settings.
type mailConfig struct {
	host     string
	port     int
	username string
	password string
	from     string
	to       string
}

func (c mailConfig) usable() bool {
	return c.host != "" && c.port > 0 && c.to != ""
}

func (c mailConfig) sender() string {
	if c.from != "" {
		return c.from
	}
	return c.username
}

func smtpSettings(database *sql.DB) mailConfig {
	port, _ := strconv.Atoi(db.GetSetting(database, db.SettingSMTPPort))
	return mailConfig{
		host:     strings.TrimSpace(db.GetSetting(database, db.SettingSMTPHost)),
		port:     port,
		username: db.GetSetting(database, db.SettingSMTPUser),
		password: db.GetSetting(database, db.SettingSMTPPassword),
		from:     strings.TrimSpace(db.GetSetting(database, db.SettingSMTPFrom)),
		to:       strings.TrimSpace(db.GetSetting(database, db.SettingSMTPTo)),
	}
}

// mailSubject turns an alert into a header-safe subject line: truncated,
// because these messages are a sentence of prose that can run long, and
// stripped of line breaks, which would otherwise let a message containing a
// newline inject its own headers. The body always carries the whole text.
func mailSubject(msg string) string {
	const maxLen = 120
	if len(msg) > maxLen {
		msg = msg[:maxLen-3] + "..."
	}
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(msg)
}

func sendMail(c mailConfig, msg string) error {
	addr := fmt.Sprintf("%s:%d", c.host, c.port)

	// Authentication is optional: a relay on the same host, or a local mail
	// submission agent, commonly takes mail without credentials.
	var auth smtp.Auth
	if c.username != "" {
		auth = smtp.PlainAuth("", c.username, c.password, c.host)
	}

	body := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		c.sender(), c.to, mailSubject(msg), msg)

	return smtp.SendMail(addr, auth, c.sender(), strings.Split(c.to, ","), []byte(body))
}

// Test delivers a probe message and reports what each configured channel did,
// so a misconfiguration is found now rather than during an incident.
func Test(database *sql.DB) error {
	const msg = "Quasar: test notification — if you are reading this, notifications work."
	var failures []string
	configured := 0

	if url := db.GetSetting(database, db.SettingNotifyURL); url != "" {
		configured++
		if err := sendWebhook(url, msg); err != nil {
			failures = append(failures, "webhook: "+err.Error())
		}
	}
	if url := db.GetSetting(database, db.SettingNtfyURL); url != "" {
		configured++
		if err := sendNtfy(url, msg); err != nil {
			failures = append(failures, "ntfy: "+err.Error())
		}
	}
	if cfg := smtpSettings(database); cfg.usable() {
		configured++
		if err := sendMail(cfg, msg); err != nil {
			failures = append(failures, "email: "+err.Error())
		}
	}

	if configured == 0 {
		return fmt.Errorf("no notification channel is configured")
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}
