package db

import (
	"errors"
	"testing"
)

// Which credential a clone URL resolves to is the whole feature: pick the
// wrong one and a token is offered to a repository it was never issued for,
// which is both a failed deploy and a token disclosed to the wrong place.
func TestGitCredentialResolvesTheNarrowestScope(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)

	for _, c := range []GitCredential{
		{Name: "fallback", Scope: AnyScope, Secret: "tok-any"},
		{Name: "github", Scope: "github.com", Secret: "tok-github"},
		{Name: "work org", Scope: "github.com/acme", Secret: "tok-acme"},
		{Name: "one repo", Scope: "github.com/acme/secret", Secret: "tok-repo"},
		{Name: "self-hosted", Scope: "git.example.com:8443", Secret: "tok-port"},
		{Name: "bare", Scope: "gitea.example.com", Secret: "tok-bare"},
	} {
		if err := SaveGitCredential(database, k, &c); err != nil {
			t.Fatalf("save %s: %v", c.Scope, err)
		}
	}

	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{"an owner-scoped token beats the forge-wide one",
			"https://github.com/acme/api.git", "tok-acme"},
		{"a repository-scoped token beats its owner's",
			"https://github.com/acme/secret.git", "tok-repo"},
		{"another owner on the same forge gets the forge-wide token",
			"https://github.com/me/blog.git", "tok-github"},
		// The reason scopes only break on a slash: a prefix comparison would
		// hand acme's token to a completely unrelated account.
		{"an owner scope never claims a longer name that starts the same way",
			"https://github.com/acmecorp/api.git", "tok-github"},
		{"host and port both named", "https://git.example.com:8443/t/r.git", "tok-port"},
		{"a scope with no port covers a forge served on one",
			"https://gitea.example.com:443/t/r.git", "tok-bare"},
		{"a scope that names a port does not cover the bare host",
			"https://git.example.com/t/r.git", "tok-any"},
		{"an unrelated forge falls back", "https://bitbucket.org/t/r.git", "tok-any"},
	} {
		got := GitCredentialFor(database, k, tc.url)
		if got == nil {
			t.Errorf("%s: no credential for %s", tc.name, tc.url)
			continue
		}
		if got.Secret != tc.want {
			t.Errorf("%s: %s resolved to %q, want %q", tc.name, tc.url, got.Secret, tc.want)
		}
	}
}

// Two accounts on the same forge is the case a host-only scope cannot express,
// and the reason owner scopes exist.
func TestTwoOwnersOnOneForgeKeepSeparateTokens(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	for _, c := range []GitCredential{
		{Name: "work", Scope: "github.com/acme", Secret: "tok-work"},
		{Name: "personal", Scope: "github.com/me", Secret: "tok-personal"},
	} {
		if err := SaveGitCredential(database, k, &c); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ url, want string }{
		{"https://github.com/acme/api.git", "tok-work"},
		{"https://github.com/me/blog.git", "tok-personal"},
	} {
		if got := GitCredentialFor(database, k, tc.url); got == nil || got.Secret != tc.want {
			t.Errorf("%s did not resolve to %q", tc.url, tc.want)
		}
	}
	// With no forge-wide credential, a third owner gets nothing rather than
	// being handed one of the two above.
	if got := GitCredentialFor(database, k, "https://github.com/other/x.git"); got != nil {
		t.Errorf("an uncovered owner resolved to %q", got.Secret)
	}
}

func TestGitCredentialAbsentWithoutAFallback(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	if err := SaveGitCredential(database, k, &GitCredential{Scope: "github.com", Secret: "tok"}); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{
		"https://gitlab.com/o/r.git",
		// Not URLs a credential is ever attached to.
		"git@github.com:o/r.git",
		"/srv/repos/local.git",
	} {
		if got := GitCredentialFor(database, k, url); got != nil {
			t.Errorf("%s resolved to %q, want nothing", url, got.Secret)
		}
	}
}

// The token is the one thing the database must not give up on its own.
func TestGitCredentialSecretIsSealedAndNeverListed(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	const secret = "glpat-Sup3rSecretValue"
	if err := SaveGitCredential(database, k, &GitCredential{Name: "gl", Scope: "gitlab.com", Secret: secret}); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := database.QueryRow("SELECT secret FROM git_credentials WHERE scope = 'gitlab.com'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret {
		t.Error("the token is stored in plaintext")
	}
	if got := GitCredentialFor(database, k, "https://gitlab.com/o/r.git"); got == nil || got.Secret != secret {
		t.Error("a sealed token must come back out intact for a deploy")
	}

	list, err := ListGitCredentials(database)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d credentials, want 1", len(list))
	}
	if list[0].Secret != "" {
		t.Error("listings must not carry the secret — the page showing them has no use for it")
	}
	if list[0].Hint != "glpat-…alue" {
		t.Errorf("hint = %q, want the masked reminder", list[0].Hint)
	}
}

// Renaming must not require pasting the token again, or operators keep a copy
// of it somewhere to be able to.
func TestSaveWithoutSecretKeepsTheStoredToken(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	if err := SaveGitCredential(database, k, &GitCredential{Name: "old", Scope: "github.com", Secret: "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGitCredential(database, k, &GitCredential{Name: "new", Scope: "github.com", Username: "bob"}); err != nil {
		t.Fatal(err)
	}

	got := GitCredentialFor(database, k, "https://github.com/o/r.git")
	if got == nil || got.Secret != "tok" {
		t.Fatal("the stored token was lost by a metadata-only save")
	}
	if got.Name != "new" || got.Username != "bob" {
		t.Errorf("name/username = %q/%q, want new/bob", got.Name, got.Username)
	}

	// ...but a host with nothing stored has to supply one.
	err := SaveGitCredential(database, k, &GitCredential{Name: "x", Scope: "gitlab.com"})
	if err == nil {
		t.Error("saving a new host with no token should be refused")
	}
}

func TestSaveReplacesTheTokenForAHostAlreadyHeld(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	if err := SaveGitCredential(database, k, &GitCredential{Scope: "github.com", Secret: "old"}); err != nil {
		t.Fatal(err)
	}
	MarkGitCredentialUsed(database, GitCredentialFor(database, k, "https://github.com/o/r.git").ID)

	if err := SaveGitCredential(database, k, &GitCredential{Scope: "github.com", Secret: "rotated"}); err != nil {
		t.Fatal(err)
	}
	got := GitCredentialFor(database, k, "https://github.com/o/r.git")
	if got.Secret != "rotated" {
		t.Errorf("secret = %q, want the rotated one", got.Secret)
	}
	// A rotation resets the usage record: "last used" answering for the
	// previous token would be a lie about the one now stored.
	if got.LastUsedAt.Valid {
		t.Error("rotating a token should clear its last-used date")
	}
	list, _ := ListGitCredentials(database)
	if len(list) != 1 {
		t.Errorf("got %d rows for one host, want 1", len(list))
	}
}

func TestEditingACredentialMovesItWithoutTouchingTheToken(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	if err := SaveGitCredential(database, k, &GitCredential{Name: "typo", Scope: "github.com", Secret: "tok"}); err != nil {
		t.Fatal(err)
	}
	list, _ := ListGitCredentials(database)
	id := list[0].ID

	// A scope typed too wide, narrowed after the fact. What the token can do
	// has not changed; only which repositories it is offered to.
	if err := UpdateGitCredentialMeta(database, id, "GitHub — work", "  GitHub.com/Acme  ", "bob"); err != nil {
		t.Fatal(err)
	}
	if got := GitCredentialFor(database, k, "https://github.com/other/repo.git"); got != nil {
		t.Error("the credential still answers for a repository its new scope excludes")
	}
	got := GitCredentialFor(database, k, "https://github.com/acme/api.git")
	if got == nil {
		t.Fatal("the credential does not answer for its new scope")
	}
	if got.Secret != "tok" {
		t.Errorf("secret = %q, want the one stored before the edit", got.Secret)
	}
	if got.Name != "GitHub — work" || got.Username != "bob" || got.Scope != "github.com/acme" {
		t.Errorf("got %q / %q / %q, want the edited values normalised", got.Name, got.Username, got.Scope)
	}
}

func TestEditingOntoAHeldScopeIsRefused(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	for _, scope := range []string{"github.com", "gitlab.com"} {
		if err := SaveGitCredential(database, k, &GitCredential{Scope: scope, Secret: "tok-" + scope}); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := ListGitCredentials(database)

	// Two rows for one scope would make which token a deploy gets a matter of
	// row order, so the collision is reported rather than resolved.
	err := UpdateGitCredentialMeta(database, list[0].ID, "", list[1].Scope, "")
	if !errors.Is(err, ErrGitScopeTaken) {
		t.Fatalf("err = %v, want ErrGitScopeTaken", err)
	}
	// Staying on its own scope is not a collision with itself.
	if err := UpdateGitCredentialMeta(database, list[0].ID, "renamed", list[0].Scope, ""); err != nil {
		t.Errorf("renaming in place: %v", err)
	}
	if err := UpdateGitCredentialMeta(database, 9999, "x", "codeberg.org", ""); err == nil {
		t.Error("editing a credential that does not exist should fail")
	}
}

func TestNormalizeGitScopeAcceptsWhatOperatorsActuallyPaste(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"github.com", "github.com"},
		{"  GitHub.com  ", "github.com"},
		{"github.com/acme", "github.com/acme"},
		// "everything under here" is what a scope already means, so the star
		// people write out of habit is simply dropped.
		{"github.com/acme/*", "github.com/acme"},
		{"github.com/acme/", "github.com/acme"},
		// Pasting a repository URL asks for that repository, which is now a
		// scope in its own right rather than a mistake to be widened.
		{"https://github.com/owner/repo.git", "github.com/owner/repo"},
		{"git@github.com", "github.com"},
		{"https://git.example.com:8443/g/r.git", "git.example.com:8443/g/r"},
		{"*", "*"},
		{"", ""},
	} {
		if got := NormalizeGitScope(tc.in); got != tc.want {
			t.Errorf("NormalizeGitScope(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGitTargetOfOnlyClaimsHTTPS(t *testing.T) {
	for _, tc := range []struct{ in, target, host string }{
		{"https://github.com/owner/repo.git", "github.com/owner/repo", "github.com"},
		{"https://GitHub.com/Owner/Repo.git", "github.com/owner/repo", "github.com"},
		{"https://git.example.com:8443/g/r.git", "git.example.com:8443/g/r", "git.example.com:8443"},
		// Nothing else is a URL Quasar attaches a credential to, so nothing
		// else has a target worth reporting.
		{"git@github.com:owner/repo.git", "", ""},
		{"ssh://git@example.com/repo.git", "", ""},
		{"/srv/repos/local.git", "", ""},
	} {
		if got := GitTargetOf(tc.in); got != tc.target {
			t.Errorf("GitTargetOf(%q) = %q, want %q", tc.in, got, tc.target)
		}
		if got := GitHostOf(tc.in); got != tc.host {
			t.Errorf("GitHostOf(%q) = %q, want %q", tc.in, got, tc.host)
		}
	}
}

func TestMaskTokenKeepsTheTypeAndNothingUseful(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ghp_abcdefghijklmnop1234", "ghp_…1234"},
		{"glpat-AbCdEfGhIjKl", "glpat-…IjKl"},
		{"github_pat_11ABCDEFG0abcdefg", "github_…defg"},
		{"nodelimiterjusttext", "…text"},
		{"short", "…"},
	} {
		if got := MaskToken(tc.in); got != tc.want {
			t.Errorf("MaskToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An upgrade must not break a deploy that worked yesterday: the old token was
// applied to every https clone, so it can only land on the any-host row.
func TestMigrateGitTokenLandsOnTheFallbackAndClearsThePlaintext(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	SetSetting(database, SettingGitToken, "ghp_legacyvalue123")

	moved, err := MigrateGitToken(database, k)
	if err != nil || !moved {
		t.Fatalf("migrate = %v, %v; want it to move the token", moved, err)
	}
	got := GitCredentialFor(database, k, "https://anything.example.com/o/r.git")
	if got == nil || got.Secret != "ghp_legacyvalue123" {
		t.Fatal("the legacy token should now answer for every host")
	}
	if !got.IsFallback() {
		t.Errorf("migrated credential host = %q, want %q", got.Scope, AnyScope)
	}
	if left := GetSetting(database, SettingGitToken); left != "" {
		t.Errorf("the plaintext setting still holds %q", left)
	}

	// Running again finds nothing to do, and a second boot must not resurrect
	// a credential the operator has since deleted.
	if moved, err := MigrateGitToken(database, k); moved || err != nil {
		t.Errorf("second migration = %v, %v; want it to be a no-op", moved, err)
	}
}

func TestMigrateGitTokenLeavesAnExistingFallbackAlone(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	if err := SaveGitCredential(database, k, &GitCredential{Name: "mine", Scope: AnyScope, Secret: "current"}); err != nil {
		t.Fatal(err)
	}
	SetSetting(database, SettingGitToken, "stale-legacy")

	if moved, err := MigrateGitToken(database, k); moved || err != nil {
		t.Errorf("migrate = %v, %v; want it to leave a configured fallback alone", moved, err)
	}
	if got := GitCredentialFor(database, k, "https://github.com/o/r.git"); got.Secret != "current" {
		t.Errorf("secret = %q, want the credential the operator configured", got.Secret)
	}
	if left := GetSetting(database, SettingGitToken); left != "" {
		t.Error("the superseded plaintext token should still be cleared")
	}
}
