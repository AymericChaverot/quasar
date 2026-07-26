package auth

import (
	"database/sql"
	"path/filepath"
	"testing"

	"quasar/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func userByName(t *testing.T, database *sql.DB, name string) *User {
	t.Helper()
	users, err := ListUsers(database)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.Username == name {
			return u
		}
	}
	t.Fatalf("user %q not found", name)
	return nil
}

// The bootstrap account has to be an admin, or a fresh install would have
// nobody able to configure it.
func TestEnsureAdminCreatesAnAdmin(t *testing.T) {
	database := openTestDB(t)
	if err := EnsureAdmin(database, "root", "password123"); err != nil {
		t.Fatal(err)
	}
	if got := userByName(t, database, "root").Role; got != RoleAdmin {
		t.Errorf("bootstrap user role = %q, want %q", got, RoleAdmin)
	}
}

func TestCreateUserValidation(t *testing.T) {
	database := openTestDB(t)
	if err := EnsureAdmin(database, "root", "password123"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, username, password, role string
		wantErr                        bool
	}{
		{name: "viewer", username: "alice", password: "password123", role: RoleViewer},
		{name: "admin", username: "bob", password: "password123", role: RoleAdmin},
		{name: "blank username", username: "  ", password: "password123", role: RoleViewer, wantErr: true},
		{name: "short password", username: "carol", password: "short", role: RoleViewer, wantErr: true},
		{name: "unknown role", username: "dave", password: "password123", role: "superuser", wantErr: true},
		{name: "duplicate name", username: "root", password: "password123", role: RoleViewer, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CreateUser(database, tc.username, tc.password, tc.role)
			if tc.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// Locking every human out of a self-hosted dashboard needs SSH and sqlite3 to
// undo, so the last admin is protected rather than merely warned about.
func TestLastAdminCannotBeDemotedOrDeleted(t *testing.T) {
	database := openTestDB(t)
	if err := EnsureAdmin(database, "root", "password123"); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(database, "alice", "password123", RoleViewer); err != nil {
		t.Fatal(err)
	}
	root := userByName(t, database, "root")

	if err := SetRole(database, root.ID, RoleViewer); err == nil {
		t.Error("demoting the only admin should be refused")
	}
	if err := DeleteUser(database, root.ID); err == nil {
		t.Error("deleting the only admin should be refused")
	}
	if got := userByName(t, database, "root").Role; got != RoleAdmin {
		t.Errorf("root role is now %q — the refusal did not hold", got)
	}

	// With a second admin in place, both become allowed.
	if err := SetRole(database, userByName(t, database, "alice").ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := SetRole(database, root.ID, RoleViewer); err != nil {
		t.Errorf("demoting one of two admins should be allowed: %v", err)
	}

	// And now alice is the last admin, so she is the protected one.
	if err := DeleteUser(database, userByName(t, database, "alice").ID); err == nil {
		t.Error("deleting the now-only admin should be refused")
	}
	// A viewer is never protected.
	if err := DeleteUser(database, root.ID); err != nil {
		t.Errorf("deleting a viewer should be allowed: %v", err)
	}
}

// A role change has to reach the session lookup, which is what requireAdmin
// consults on every request.
func TestSessionReportsCurrentRole(t *testing.T) {
	database := openTestDB(t)
	if err := EnsureAdmin(database, "root", "password123"); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(database, "alice", "password123", RoleViewer); err != nil {
		t.Fatal(err)
	}

	token, needs2FA, err := Login(database, "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if needs2FA {
		t.Fatal("no second factor was configured")
	}
	_, username, role, err := UserForSession(database, token)
	if err != nil {
		t.Fatal(err)
	}
	if username != "alice" || role != RoleViewer {
		t.Fatalf("session reports %q/%q, want alice/%s", username, role, RoleViewer)
	}

	if err := SetRole(database, userByName(t, database, "alice").ID, RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, _, role, err = UserForSession(database, token); err != nil {
		t.Fatal(err)
	}
	if role != RoleAdmin {
		t.Errorf("after promotion the session still reports %q", role)
	}
}

// Resetting someone's password must end their sessions, or a compromised
// account stays usable after the admin has locked it out.
func TestResetPasswordEndsSessions(t *testing.T) {
	database := openTestDB(t)
	if err := EnsureAdmin(database, "root", "password123"); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(database, "alice", "password123", RoleViewer); err != nil {
		t.Fatal(err)
	}
	token, _, err := Login(database, "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(database, token) {
		t.Fatal("the fresh session should be valid")
	}

	if err := ResetPassword(database, userByName(t, database, "alice").ID, "newpassword1"); err != nil {
		t.Fatal(err)
	}
	if Valid(database, token) {
		t.Error("the old session survived a password reset")
	}
	if _, _, err := Login(database, "alice", "newpassword1"); err != nil {
		t.Errorf("the new password should work: %v", err)
	}
	if _, _, err := Login(database, "alice", "password123"); err == nil {
		t.Error("the old password should be rejected")
	}
}

// Deleting an account must not leave a working session behind.
func TestDeleteUserEndsSessions(t *testing.T) {
	database := openTestDB(t)
	if err := EnsureAdmin(database, "root", "password123"); err != nil {
		t.Fatal(err)
	}
	if err := CreateUser(database, "alice", "password123", RoleViewer); err != nil {
		t.Fatal(err)
	}
	token, _, err := Login(database, "alice", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser(database, userByName(t, database, "alice").ID); err != nil {
		t.Fatal(err)
	}
	if Valid(database, token) {
		t.Error("a deleted user's session is still valid")
	}
}
