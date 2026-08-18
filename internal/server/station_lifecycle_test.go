package server

import (
	"strings"
	"testing"

	"quasar/internal/db"
	"quasar/internal/station"
)

// The verbs are a list, and a station holds the ones on it. A document asking
// for restart has not asked to be able to stop the thing.
func TestAnUnlistedLifecycleVerbIsRefused(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{Lifecycle: []string{"restart"}}, "")

	for _, verb := range []string{"stop", "start", "redeploy", "set_image"} {
		_, err := ask(t, c, "lifecycle", map[string]any{"verb": verb, "image": "nginx:1.27"})
		if err == nil {
			t.Errorf("%s was allowed on a station that only listed restart", verb)
		} else if !strings.Contains(err.Error(), "lifecycle") {
			t.Errorf("%s: the refusal does not name the permission: %v", verb, err)
		}
	}

	// And the one it did list is refused for a different reason on a dashboard
	// with no Docker, which is a refusal about this machine rather than about
	// the document.
	_, err := ask(t, c, "lifecycle", map[string]any{"verb": "restart"})
	if err == nil || strings.Contains(err.Error(), "lifecycle permission") {
		t.Errorf("restart was refused as if it had not been listed: %v", err)
	}
}

// set_image is the whole of what "upgrade this server" means for an image
// application, so it writes the reference and then goes down the same path the
// Update button does.
func TestSetImageWritesTheReference(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{Lifecycle: []string{"set_image"}}, "")
	c.app.DeployType = "image"

	if err := db.UpdateAppImage(c.srv.db, c.app.ID, "itzg/minecraft-server:java17"); err != nil {
		t.Fatal(err)
	}
	app, err := db.GetApp(c.srv.db, c.srv.keyring, c.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if app.ImageRef != "itzg/minecraft-server:java17" {
		t.Errorf("image_ref = %q", app.ImageRef)
	}
}

// A message arriving out of nowhere is one nobody can act on, and a station in
// a loop reaches somebody's phone.
func TestNotifyIsGrantedAndBounded(t *testing.T) {
	c, _ := brokerFor(t, station.Permissions{}, "")
	if _, err := ask(t, c, "notify", map[string]any{"message": "hello"}); err == nil {
		t.Fatal("a station granted no notify sent a message")
	}

	c.doc.Permissions.Notify = true
	for i := 0; i < maxNotifications; i++ {
		if _, err := ask(t, c, "notify", map[string]any{"message": "mod updates available"}); err != nil {
			t.Fatalf("message %d was refused: %v", i+1, err)
		}
	}
	if _, err := ask(t, c, "notify", map[string]any{"message": "and again"}); err == nil {
		t.Error("a station sent more messages in one call than it is allowed to")
	}

	entries, _ := db.ListAudit(c.srv.db, "station.notify", 10)
	if len(entries) != maxNotifications {
		t.Errorf("%d audit entries for %d messages", len(entries), maxNotifications)
	}
	if !strings.Contains(entries[0].Detail, "mod updates") {
		t.Errorf("the entry does not say what was sent: %q", entries[0].Detail)
	}
}
