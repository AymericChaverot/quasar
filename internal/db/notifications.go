package db

import (
	"database/sql"
	"strings"
	"time"
)

// The notification history behind the header's bell: every alert the
// platform raised — the very ones it sends to the webhook, ntfy and email,
// kept here too, so one that went to no channel is not lost — and how the long
// jobs each person started ended. An alert is everybody's; the end of a job is
// only the person's who started it.
//
// What has been read is one mark per person: the newest notification they had
// when they last opened the bell. Everything after it is unread.

// NotificationRetention is how long the history is kept.
const NotificationRetention = 30 * 24 * time.Hour

// Notification is one entry of the history.
type Notification struct {
	ID int64
	At time.Time
	// UserID is the person it is for, or 0 for an alert, which is for
	// everybody.
	UserID int64
	// Kind is ok, info, warn or err, as a toast's.
	Kind, Title, Text, Detail string
	// Link is the page it is about, if there is one.
	Link string
	// Unread is whether it came after the person last opened the bell.
	Unread bool
}

// AddNotification records n, and forgets what is past the retention.
func AddNotification(db *sql.DB, n Notification) error {
	if n.At.IsZero() {
		n.At = time.Now()
	}
	if _, err := db.Exec(`INSERT INTO notifications (ts, user_id, kind, title, text, detail, link)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		n.At.Unix(), n.UserID, n.Kind, n.Title, n.Text, n.Detail, n.Link); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM notifications WHERE ts < ?`, n.At.Add(-NotificationRetention).Unix())
	return err
}

// NotificationFilter narrows the history one person sees.
type NotificationFilter struct {
	// Origin is "alerts", "mine" or empty for both.
	Origin string
	// Kinds keeps only these kinds, when it is not empty.
	Kinds []string
	// Unread keeps only what came after the person last opened the bell.
	Unread bool
	// Limit is how many to return at most, newest first; 0 is no limit.
	Limit int
}

// Notifications is what user may see of the history, newest first.
func Notifications(db *sql.DB, user int64, f NotificationFilter) ([]Notification, error) {
	seen := notificationsSeen(db, user)
	where := []string{"(user_id = 0 OR user_id = ?)"}
	args := []any{user}
	switch f.Origin {
	case "alerts":
		where = append(where, "user_id = 0")
	case "mine":
		where = append(where, "user_id != 0")
	}
	if len(f.Kinds) > 0 {
		where = append(where, "kind IN (?"+strings.Repeat(", ?", len(f.Kinds)-1)+")")
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if f.Unread {
		where = append(where, "id > ?")
		args = append(args, seen)
	}
	q := `SELECT id, ts, user_id, kind, title, text, detail, link FROM notifications
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY id DESC`
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var ts int64
		if err := rows.Scan(&n.ID, &ts, &n.UserID, &n.Kind, &n.Title, &n.Text, &n.Detail, &n.Link); err != nil {
			return nil, err
		}
		n.At = time.Unix(ts, 0)
		n.Unread = n.ID > seen
		out = append(out, n)
	}
	return out, rows.Err()
}

// NotificationCounts is how many notifications user has: all of them, the
// alerts, their own, and those unread.
type NotificationCounts struct {
	All, Alerts, Mine, Unread int
}

// CountNotifications counts what user may see of the history.
func CountNotifications(db *sql.DB, user int64) NotificationCounts {
	var c NotificationCounts
	_ = db.QueryRow(`SELECT COUNT(*),
			COALESCE(SUM(user_id = 0), 0),
			COALESCE(SUM(user_id != 0), 0),
			COALESCE(SUM(id > ?), 0)
		FROM notifications WHERE user_id = 0 OR user_id = ?`,
		notificationsSeen(db, user), user).Scan(&c.All, &c.Alerts, &c.Mine, &c.Unread)
	return c
}

// UnreadNotifications is the number on the bell.
func UnreadNotifications(db *sql.DB, user int64) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE (user_id = 0 OR user_id = ?) AND id > ?`,
		user, notificationsSeen(db, user)).Scan(&n)
	return n
}

// MarkNotificationsSeen marks as read what user has up to the notification
// upTo: the newest one they were shown, so one that arrives while the bell is
// being opened stays unread rather than being marked without being seen. The
// mark never goes back.
func MarkNotificationsSeen(db *sql.DB, user, upTo int64) error {
	_, err := db.Exec(`INSERT INTO notifications_seen (user_id, last_id) VALUES (?, ?)
		ON CONFLICT(user_id) DO UPDATE SET last_id = MAX(last_id, excluded.last_id)`, user, upTo)
	return err
}

// DeleteUserNotifications forgets a removed account's own notifications and
// what it had read.
func DeleteUserNotifications(db *sql.DB, user int64) error {
	if _, err := db.Exec(`DELETE FROM notifications WHERE user_id = ?`, user); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM notifications_seen WHERE user_id = ?`, user)
	return err
}

func notificationsSeen(db *sql.DB, user int64) int64 {
	var id int64
	_ = db.QueryRow(`SELECT last_id FROM notifications_seen WHERE user_id = ?`, user).Scan(&id)
	return id
}
