package notify

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"quasar/internal/db"
)

// recorder is a stand-in destination that can also be told to fail, so the
// isolation between channels can be tested.
type recorder struct {
	mu       sync.Mutex
	bodies   []string
	titles   []string
	failWith int // 0 means respond 200
	srv      *httptest.Server
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.bodies = append(rec.bodies, string(body))
		rec.titles = append(rec.titles, r.Header.Get("Title"))
		status := rec.failWith
		rec.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

func (r *recorder) fail(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failWith = status
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// The reason for having several channels at all: one dead destination must not
// take the others down with it.
func TestOneBrokenChannelDoesNotSilenceTheOthers(t *testing.T) {
	database := openTestDB(t)
	webhook, ntfy := newRecorder(t), newRecorder(t)
	db.SetSetting(database, db.SettingNotifyURL, webhook.srv.URL)
	db.SetSetting(database, db.SettingNtfyURL, ntfy.srv.URL)
	// And an SMTP host that refuses connections outright.
	db.SetSetting(database, db.SettingSMTPHost, "127.0.0.1")
	db.SetSetting(database, db.SettingSMTPPort, "1")
	db.SetSetting(database, db.SettingSMTPTo, "me@example.com")

	webhook.fail(http.StatusInternalServerError)

	Send(database, "the disk is nearly full")

	if ntfy.count() != 1 {
		t.Errorf("ntfy received %d messages, want 1 despite the other channels failing", ntfy.count())
	}
	if webhook.count() != 1 {
		t.Errorf("the webhook should still have been attempted, got %d", webhook.count())
	}
}

// A total delivery failure has to leave a trace an operator can find, since the
// symptom is silence.
func TestTotalFailureIsAudited(t *testing.T) {
	database := openTestDB(t)
	webhook := newRecorder(t)
	webhook.fail(http.StatusNotFound)
	db.SetSetting(database, db.SettingNotifyURL, webhook.srv.URL)

	Send(database, "an app crashed")

	entries, err := db.ListAudit(database, "notify.failed", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d audit entries, want 1", len(entries))
	}
	if !strings.Contains(entries[0].Detail, "webhook") {
		t.Errorf("audit detail %q should name the failing channel", entries[0].Detail)
	}
}

// A partial success is not worth an audit entry: the operator was reached.
func TestPartialSuccessIsNotAudited(t *testing.T) {
	database := openTestDB(t)
	webhook, ntfy := newRecorder(t), newRecorder(t)
	webhook.fail(http.StatusBadGateway)
	db.SetSetting(database, db.SettingNotifyURL, webhook.srv.URL)
	db.SetSetting(database, db.SettingNtfyURL, ntfy.srv.URL)

	Send(database, "an app crashed")

	entries, _ := db.ListAudit(database, "notify.failed", 10)
	if len(entries) != 0 {
		t.Errorf("got %d audit entries, want none when a channel succeeded", len(entries))
	}
}

func TestNoChannelsConfiguredIsSilent(t *testing.T) {
	database := openTestDB(t)
	Send(database, "nothing to see") // must not panic or record a failure
	if entries, _ := db.ListAudit(database, "", 10); len(entries) != 0 {
		t.Errorf("a platform with no channels configured recorded %d entries", len(entries))
	}
}

func TestWebhookPayloadSuitsDiscordAndSlack(t *testing.T) {
	database := openTestDB(t)
	webhook := newRecorder(t)
	db.SetSetting(database, db.SettingNotifyURL, webhook.srv.URL)

	Send(database, "hello")

	webhook.mu.Lock()
	body := webhook.bodies[0]
	webhook.mu.Unlock()
	// Discord reads "content", Slack-compatible endpoints read "text".
	for _, key := range []string{`"content":"hello"`, `"text":"hello"`} {
		if !strings.Contains(body, key) {
			t.Errorf("payload %q is missing %s", body, key)
		}
	}
}

func TestNtfySendsPlainTextWithATitle(t *testing.T) {
	database := openTestDB(t)
	ntfy := newRecorder(t)
	db.SetSetting(database, db.SettingNtfyURL, ntfy.srv.URL)

	Send(database, "the disk is nearly full")

	ntfy.mu.Lock()
	defer ntfy.mu.Unlock()
	if ntfy.bodies[0] != "the disk is nearly full" {
		t.Errorf("ntfy body = %q, want the bare message", ntfy.bodies[0])
	}
	if ntfy.titles[0] != "Quasar" {
		t.Errorf("ntfy Title header = %q, want Quasar", ntfy.titles[0])
	}
}

// Test must distinguish "nothing configured" from "configured but broken":
// reporting success on an unconfigured platform is how you end up trusting
// notifications that were never going anywhere.
func TestTestReportsUnconfigured(t *testing.T) {
	database := openTestDB(t)
	err := Test(database)
	if err == nil {
		t.Fatal("expected an error when no channel is configured")
	}
	if !strings.Contains(err.Error(), "no notification channel") {
		t.Errorf("error %q should say no channel is configured", err)
	}
}

func TestTestSucceedsWhenEveryChannelWorks(t *testing.T) {
	database := openTestDB(t)
	webhook, ntfy := newRecorder(t), newRecorder(t)
	db.SetSetting(database, db.SettingNotifyURL, webhook.srv.URL)
	db.SetSetting(database, db.SettingNtfyURL, ntfy.srv.URL)

	if err := Test(database); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if webhook.count() != 1 || ntfy.count() != 1 {
		t.Errorf("webhook=%d ntfy=%d, want one probe each", webhook.count(), ntfy.count())
	}
}

// Unlike Send, Test reports a single broken channel: the operator is asking
// whether delivery works, so a partial answer is a failed answer.
func TestTestReportsAnyBrokenChannel(t *testing.T) {
	database := openTestDB(t)
	webhook, ntfy := newRecorder(t), newRecorder(t)
	webhook.fail(http.StatusUnauthorized)
	db.SetSetting(database, db.SettingNotifyURL, webhook.srv.URL)
	db.SetSetting(database, db.SettingNtfyURL, ntfy.srv.URL)

	err := Test(database)
	if err == nil {
		t.Fatal("expected an error naming the broken channel")
	}
	if !strings.Contains(err.Error(), "webhook") {
		t.Errorf("error %q should name the webhook", err)
	}
}

func TestMailSubject(t *testing.T) {
	t.Run("short message passes through", func(t *testing.T) {
		if got := mailSubject("the disk is nearly full"); got != "the disk is nearly full" {
			t.Errorf("mailSubject() = %q", got)
		}
	})

	t.Run("long message is truncated", func(t *testing.T) {
		got := mailSubject(strings.Repeat("a very long alert message ", 40))
		if len(got) > 120 {
			t.Errorf("subject is %d chars, want at most 120", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Errorf("a truncated subject should be marked as such, got %q", got)
		}
	})

	// A message carrying a newline could otherwise inject its own headers into
	// the mail — the alert text includes app names and error strings.
	t.Run("newlines are stripped", func(t *testing.T) {
		got := mailSubject("an app crashed\r\nBcc: attacker@example.com")
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("subject %q still contains a line break", got)
		}
		if !strings.Contains(got, "Bcc: attacker@example.com") {
			t.Error("the text should be kept, only the line break neutralised")
		}
	})
}

func TestMailConfigUsable(t *testing.T) {
	tests := []struct {
		name string
		cfg  mailConfig
		want bool
	}{
		{"complete", mailConfig{host: "smtp.example.com", port: 587, to: "me@example.com"}, true},
		{"no host", mailConfig{port: 587, to: "me@example.com"}, false},
		{"no port", mailConfig{host: "smtp.example.com", to: "me@example.com"}, false},
		{"no recipient", mailConfig{host: "smtp.example.com", port: 587}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.usable(); got != tc.want {
				t.Errorf("usable() = %v, want %v", got, tc.want)
			}
		})
	}
}

// With no explicit From, the username stands in — a relay generally requires
// the envelope sender to be the authenticated account anyway.
func TestSenderFallsBackToUsername(t *testing.T) {
	if got := (mailConfig{username: "quasar@example.com"}).sender(); got != "quasar@example.com" {
		t.Errorf("sender() = %q, want the username", got)
	}
	if got := (mailConfig{username: "u", from: "from@example.com"}).sender(); got != "from@example.com" {
		t.Errorf("sender() = %q, want the explicit From", got)
	}
}
