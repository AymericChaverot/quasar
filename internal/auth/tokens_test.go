package auth

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// The secret must not be recoverable from the database: that is the point of
// storing a hash, and what makes a leaked backup less damaging.
func TestTokenSecretIsNotStored(t *testing.T) {
	database := openTestDB(t)
	secret, err := CreateToken(database, "github-actions", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, TokenPrefix) {
		t.Errorf("secret %q should start with %q so a leak is recognisable", secret, TokenPrefix)
	}

	var stored, prefix string
	if err := database.QueryRow("SELECT token_hash, prefix FROM api_tokens WHERE name = 'github-actions'").
		Scan(&stored, &prefix); err != nil {
		t.Fatal(err)
	}
	if stored == secret {
		t.Fatal("the secret itself is in the database")
	}
	if strings.Contains(stored, strings.TrimPrefix(secret, TokenPrefix)) {
		t.Fatal("the stored hash contains the secret")
	}
	// The display prefix must be too short to be useful.
	if len(prefix) >= len(secret) {
		t.Errorf("prefix %q is as long as the secret", prefix)
	}
	if !strings.HasPrefix(secret, prefix) {
		t.Errorf("prefix %q should be the start of the secret", prefix)
	}
}

func TestAuthenticateToken(t *testing.T) {
	database := openTestDB(t)
	adminSecret, err := CreateToken(database, "deployer", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	viewerSecret, err := CreateToken(database, "monitoring", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}

	id, err := AuthenticateToken(database, adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	if id.Token != "deployer" || id.Role != RoleAdmin || id.Personal() {
		t.Errorf("got %+v, want the service token deployer as %s", id, RoleAdmin)
	}

	if id, err = AuthenticateToken(database, viewerSecret); err != nil || id.Role != RoleViewer {
		t.Errorf("viewer token resolved to %+v (err %v)", id, err)
	}

	// Every rejection path, so none of them accidentally authenticates.
	for _, bad := range []string{
		"",
		"nonsense",
		TokenPrefix + "0000000000000000000000000000000000000000000000000000000000000000",
		strings.TrimPrefix(adminSecret, TokenPrefix), // right secret, missing prefix
		adminSecret + "x",
		strings.ToUpper(adminSecret),
	} {
		if _, err := AuthenticateToken(database, bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// Two tokens issued back to back must differ; uniqueness comes from the random
// secret, not from anything the caller supplies.
func TestTokensAreUnique(t *testing.T) {
	database := openTestDB(t)
	first, err := CreateToken(database, "one", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateToken(database, "two", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two tokens came out identical")
	}
}

func TestCreateTokenValidation(t *testing.T) {
	database := openTestDB(t)
	for _, tc := range []struct{ name, role string }{
		{"", RoleAdmin},
		{"   ", RoleAdmin},
		{"ci", "superuser"},
		{"ci", ""},
	} {
		if _, err := CreateToken(database, tc.name, tc.role); err == nil {
			t.Errorf("CreateToken(%q, %q) should have failed", tc.name, tc.role)
		}
	}
}

// Revocation has to bite immediately — a token is usually revoked because it
// leaked.
func TestDeleteTokenRevokesImmediately(t *testing.T) {
	database := openTestDB(t)
	secret, err := CreateToken(database, "leaked", RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := ListTokens(database)
	if err != nil || len(tokens) != 1 {
		t.Fatalf("listed %d tokens (err %v), want 1", len(tokens), err)
	}

	name, err := DeleteToken(database, tokens[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if name != "leaked" {
		t.Errorf("DeleteToken returned %q, want the name for the audit trail", name)
	}
	if _, err := AuthenticateToken(database, secret); err == nil {
		t.Error("a revoked token still authenticates")
	}
}

// Last-used is what tells an operator which tokens are dead weight.
func TestLastUsedIsRecorded(t *testing.T) {
	database := openTestDB(t)
	secret, err := CreateToken(database, "ci", RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	tokens, _ := ListTokens(database)
	if tokens[0].LastUsedAt.Valid {
		t.Error("a fresh token should have no last-used timestamp")
	}

	if _, err := AuthenticateToken(database, secret); err != nil {
		t.Fatal(err)
	}
	tokens, _ = ListTokens(database)
	if !tokens[0].LastUsedAt.Valid {
		t.Error("using a token should record when")
	}
}

// newUser creates an account and returns its id.
func newUser(t *testing.T, database *sql.DB, name, role string) int64 {
	t.Helper()
	if err := CreateUser(database, name, "password123", role); err != nil {
		t.Fatal(err)
	}
	return userByName(t, database, name).ID
}

// A personal token acts as its owner, with the role the owner has at the
// moment of the request rather than the one they had when it was created.
func TestPersonalTokenActsAsItsOwner(t *testing.T) {
	database := openTestDB(t)
	alice := newUser(t, database, "alice", RoleAdmin)
	secret, err := CreatePersonalToken(database, alice, "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := AuthenticateToken(database, secret)
	if err != nil {
		t.Fatal(err)
	}
	if !id.Personal() || id.Username != "alice" || id.Token != "laptop" || id.Role != RoleAdmin || id.ExpiresAt.Valid {
		t.Fatalf("got %+v, want alice's laptop token as admin, never expiring", id)
	}

	// Demoting alice demotes the token. Another admin has to exist first,
	// since the last one cannot be demoted.
	newUser(t, database, "bob", RoleAdmin)
	if err := SetRole(database, alice, RoleViewer); err != nil {
		t.Fatal(err)
	}
	if id, _ = AuthenticateToken(database, secret); id.Role != RoleViewer {
		t.Errorf("after demotion the token acts as %q, want %q", id.Role, RoleViewer)
	}
}

func TestExpiredPersonalTokenIsRefused(t *testing.T) {
	database := openTestDB(t)
	alice := newUser(t, database, "alice", RoleAdmin)
	secret, err := CreatePersonalToken(database, alice, "old", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AuthenticateToken(database, secret); err != nil {
		t.Fatalf("an unexpired token was refused: %v", err)
	}
	if _, err := database.Exec("UPDATE api_tokens SET expires_at = ?", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthenticateToken(database, secret); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("an expired token gave %v, want ErrTokenExpired", err)
	}
}

func TestDeletingTheOwnerDeletesTheirTokens(t *testing.T) {
	database := openTestDB(t)
	newUser(t, database, "admin", RoleAdmin)
	alice := newUser(t, database, "alice", RoleViewer)
	secret, err := CreatePersonalToken(database, alice, "laptop", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteUser(database, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthenticateToken(database, secret); err == nil {
		t.Error("the token of a deleted account still authenticates")
	}
	if left, _ := ListPersonalTokens(database, 0); len(left) != 0 {
		t.Errorf("%d personal tokens outlived their owner", len(left))
	}
}

// Service and personal tokens share a table; each list and each revocation
// has to keep to its own kind, and a user to their own tokens.
func TestTokenKindsStayApart(t *testing.T) {
	database := openTestDB(t)
	alice := newUser(t, database, "alice", RoleAdmin)
	bob := newUser(t, database, "bob", RoleViewer)
	if _, err := CreateToken(database, "ci", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePersonalToken(database, alice, "laptop", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := CreatePersonalToken(database, bob, "desktop", 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	service, _ := ListTokens(database)
	if len(service) != 1 || service[0].Name != "ci" || service[0].Personal() {
		t.Fatalf("service tokens: %+v", service)
	}
	all, _ := ListPersonalTokens(database, 0)
	if len(all) != 2 {
		t.Fatalf("listed %d personal tokens, want 2", len(all))
	}
	mine, _ := ListPersonalTokens(database, bob)
	if len(mine) != 1 || mine[0].Name != "desktop" || mine[0].Username != "bob" || !mine[0].ExpiresAt.Valid {
		t.Fatalf("bob's tokens: %+v", mine)
	}

	laptop := all[0].ID
	if _, err := DeleteToken(database, laptop); err == nil {
		t.Error("DeleteToken revoked a personal token")
	}
	if _, _, err := DeletePersonalToken(database, laptop, bob); err == nil {
		t.Error("bob revoked alice's token")
	}
	if _, _, err := DeletePersonalToken(database, service[0].ID, 0); err == nil {
		t.Error("DeletePersonalToken revoked a service token")
	}
	name, owner, err := DeletePersonalToken(database, laptop, alice)
	if err != nil || name != "laptop" || owner != "alice" {
		t.Errorf("alice revoking her own token: %q %q %v", name, owner, err)
	}
	if _, _, err := DeletePersonalToken(database, mine[0].ID, 0); err != nil {
		t.Errorf("an admin revoking bob's token: %v", err)
	}
}

func TestTokenLifetime(t *testing.T) {
	if ttl, err := TokenLifetime("90d"); err != nil || ttl != 90*24*time.Hour {
		t.Errorf("90d gave %v %v", ttl, err)
	}
	if ttl, err := TokenLifetime("never"); err != nil || ttl != 0 {
		t.Errorf("never gave %v %v", ttl, err)
	}
	if _, err := TokenLifetime(""); err == nil {
		t.Error("an empty choice was accepted")
	}
}
