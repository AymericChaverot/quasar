package db

import (
	"fmt"
	"testing"
)

func TestAuditRoundTripAndSearch(t *testing.T) {
	database := openTestDB(t)

	RecordAudit(database, AuditEntry{Actor: "alice", Action: "app.delete", Target: "Web", Detail: "web (image)", IP: "198.51.100.7"})
	RecordAudit(database, AuditEntry{Actor: "bob", Action: "terminal.open", Target: "Postgres", IP: "198.51.100.8"})
	RecordAudit(database, AuditEntry{Actor: ActorWebhook, Action: "app.deploy", Target: "Web"})

	all, err := ListAudit(database, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d entries, want 3", len(all))
	}
	// Newest first, so the most recent action is what you see on arrival.
	if all[0].Actor != ActorWebhook {
		t.Errorf("first entry actor = %q, want the newest (%q)", all[0].Actor, ActorWebhook)
	}
	if all[2].Actor != "alice" || all[2].Detail != "web (image)" {
		t.Errorf("oldest entry = %+v, want alice's app.delete", all[2])
	}
	if all[2].TS.IsZero() {
		t.Error("entries should be timestamped by the database")
	}

	// Search spans actor, action and target: those are the three things you
	// actually go looking for.
	for _, tc := range []struct {
		query string
		want  int
	}{
		{"alice", 1},
		{"terminal", 1},
		{"Web", 2},
		{"nothing-matches", 0},
	} {
		got, err := ListAudit(database, tc.query, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.want {
			t.Errorf("search %q returned %d entries, want %d", tc.query, len(got), tc.want)
		}
	}
}

func TestPruneAuditKeepsNewest(t *testing.T) {
	database := openTestDB(t)

	// Inserted in one transaction rather than through RecordAudit: 20k
	// individual commits take over a minute, and the commit boundary is not
	// what this test is about.
	over := 50
	total := MaxAuditEntries + over
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare("INSERT INTO audit_log (actor, action, target) VALUES (?, ?, ?)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < total; i++ {
		if _, err := stmt.Exec("alice", "app.deploy", fmt.Sprintf("app%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	PruneAudit(database)

	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != MaxAuditEntries {
		t.Errorf("kept %d entries, want the cap of %d", n, MaxAuditEntries)
	}

	newest, err := ListAudit(database, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("app%d", MaxAuditEntries+over-1); newest[0].Target != want {
		t.Errorf("newest surviving entry = %q, want %q", newest[0].Target, want)
	}
}

func TestPruneAuditOnEmptyTable(t *testing.T) {
	PruneAudit(openTestDB(t)) // fresh install
}
