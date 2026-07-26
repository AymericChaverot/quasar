package auth

import (
	"strings"
	"testing"
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

	name, role, err := AuthenticateToken(database, adminSecret)
	if err != nil {
		t.Fatal(err)
	}
	if name != "deployer" || role != RoleAdmin {
		t.Errorf("got %q/%q, want deployer/%s", name, role, RoleAdmin)
	}

	if _, role, err = AuthenticateToken(database, viewerSecret); err != nil || role != RoleViewer {
		t.Errorf("viewer token resolved to %q (err %v)", role, err)
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
		if _, _, err := AuthenticateToken(database, bad); err == nil {
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
	if _, _, err := AuthenticateToken(database, secret); err == nil {
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

	if _, _, err := AuthenticateToken(database, secret); err != nil {
		t.Fatal(err)
	}
	tokens, _ = ListTokens(database)
	if !tokens[0].LastUsedAt.Valid {
		t.Error("using a token should record when")
	}
}
