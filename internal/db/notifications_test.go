package db

import (
	"testing"
	"time"
)

// An alert is everybody's, the end of a job only its owner's, and what is
// read is per person, up to what they were shown.
func TestNotificationsPerPerson(t *testing.T) {
	database := openTestDB(t)
	add := func(user int64, kind, title string) {
		t.Helper()
		if err := AddNotification(database, Notification{UserID: user, Kind: kind, Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	add(0, "err", "Deploy failed")
	add(1, "ok", "Backup finished")
	add(2, "ok", "Cleanup finished")

	one, err := Notifications(database, 1, NotificationFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 2 || one[0].Title != "Backup finished" || one[1].Title != "Deploy failed" {
		t.Fatalf("user 1 sees %+v", one)
	}
	if c := CountNotifications(database, 1); c != (NotificationCounts{All: 2, Alerts: 1, Mine: 1, Unread: 2}) {
		t.Fatalf("counts %+v", c)
	}
	mine, _ := Notifications(database, 1, NotificationFilter{Origin: "mine"})
	alerts, _ := Notifications(database, 1, NotificationFilter{Origin: "alerts"})
	errs, _ := Notifications(database, 1, NotificationFilter{Kinds: []string{"err", "warn"}})
	if len(mine) != 1 || len(alerts) != 1 || len(errs) != 1 || errs[0].Kind != "err" {
		t.Fatalf("mine %v, alerts %v, errors %v", mine, alerts, errs)
	}

	// Opened with the newest being the backup: a later alert stays unread.
	if err := MarkNotificationsSeen(database, 1, one[0].ID); err != nil {
		t.Fatal(err)
	}
	add(0, "warn", "Disk over 85%")
	if n := UnreadNotifications(database, 1); n != 1 {
		t.Fatalf("unread after seeing %d", n)
	}
	unread, _ := Notifications(database, 1, NotificationFilter{Unread: true})
	if len(unread) != 1 || unread[0].Title != "Disk over 85%" || !unread[0].Unread {
		t.Fatalf("unread %+v", unread)
	}
	// The mark never goes back.
	_ = MarkNotificationsSeen(database, 1, 1)
	if n := UnreadNotifications(database, 1); n != 1 {
		t.Fatalf("unread after an older mark %d", n)
	}
	// User 2 has read nothing.
	if n := UnreadNotifications(database, 2); n != 3 {
		t.Fatalf("user 2 unread %d", n)
	}

	if err := DeleteUserNotifications(database, 1); err != nil {
		t.Fatal(err)
	}
	if c := CountNotifications(database, 1); c.Mine != 0 || c.Unread != 2 {
		t.Fatalf("after removing user 1 %+v", c)
	}
}

// What is older than the retention goes when the next one is recorded.
func TestNotificationsRetention(t *testing.T) {
	database := openTestDB(t)
	old := time.Now().Add(-NotificationRetention - time.Hour)
	if err := AddNotification(database, Notification{Kind: "info", Title: "old", At: old}); err != nil {
		t.Fatal(err)
	}
	if err := AddNotification(database, Notification{Kind: "info", Title: "new"}); err != nil {
		t.Fatal(err)
	}
	got, _ := Notifications(database, 1, NotificationFilter{})
	if len(got) != 1 || got[0].Title != "new" {
		t.Fatalf("kept %+v", got)
	}
}
