package db

import "testing"

// Which credential a clone URL resolves to is the whole feature: pick the
// wrong one and a token is offered to a forge it was never issued for, which
// is both a failed deploy and a token disclosed to the wrong host.
func TestGitCredentialResolvesMostSpecificHost(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)

	for _, c := range []GitCredential{
		{Name: "fallback", Host: AnyHost, Secret: "tok-any"},
		{Name: "github", Host: "github.com", Secret: "tok-github"},
		{Name: "self-hosted", Host: "git.example.com:8443", Secret: "tok-port"},
		{Name: "bare", Host: "gitea.example.com", Secret: "tok-bare"},
	} {
		if err := SaveGitCredential(database, k, &c); err != nil {
			t.Fatalf("save %s: %v", c.Host, err)
		}
	}

	for _, tc := range []struct {
		name string
		host string
		want string
	}{
		{"the host with its own credential", "github.com", "tok-github"},
		{"host and port both named", "git.example.com:8443", "tok-port"},
		{"a bare-host credential covers a port it does not name", "gitea.example.com:443", "tok-bare"},
		{"a port-specific credential does not cover the bare host", "git.example.com", "tok-any"},
		{"anything else falls back", "bitbucket.org", "tok-any"},
	} {
		got := GitCredentialFor(database, k, tc.host)
		if got == nil {
			t.Errorf("%s: no credential for %s", tc.name, tc.host)
			continue
		}
		if got.Secret != tc.want {
			t.Errorf("%s: %s resolved to %q, want %q", tc.name, tc.host, got.Secret, tc.want)
		}
	}
}

func TestGitCredentialAbsentWithoutAFallback(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	if err := SaveGitCredential(database, k, &GitCredential{Host: "github.com", Secret: "tok"}); err != nil {
		t.Fatal(err)
	}
	if got := GitCredentialFor(database, k, "gitlab.com"); got != nil {
		t.Errorf("gitlab.com resolved to %q, want nothing — a public clone must not carry a token", got.Secret)
	}
}

// The token is the one thing the database must not give up on its own.
func TestGitCredentialSecretIsSealedAndNeverListed(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	const secret = "glpat-Sup3rSecretValue"
	if err := SaveGitCredential(database, k, &GitCredential{Name: "gl", Host: "gitlab.com", Secret: secret}); err != nil {
		t.Fatal(err)
	}

	var stored string
	if err := database.QueryRow("SELECT secret FROM git_credentials WHERE host = 'gitlab.com'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret {
		t.Error("the token is stored in plaintext")
	}
	if got := GitCredentialFor(database, k, "gitlab.com"); got == nil || got.Secret != secret {
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
	if err := SaveGitCredential(database, k, &GitCredential{Name: "old", Host: "github.com", Secret: "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveGitCredential(database, k, &GitCredential{Name: "new", Host: "github.com", Username: "bob"}); err != nil {
		t.Fatal(err)
	}

	got := GitCredentialFor(database, k, "github.com")
	if got == nil || got.Secret != "tok" {
		t.Fatal("the stored token was lost by a metadata-only save")
	}
	if got.Name != "new" || got.Username != "bob" {
		t.Errorf("name/username = %q/%q, want new/bob", got.Name, got.Username)
	}

	// ...but a host with nothing stored has to supply one.
	err := SaveGitCredential(database, k, &GitCredential{Name: "x", Host: "gitlab.com"})
	if err == nil {
		t.Error("saving a new host with no token should be refused")
	}
}

func TestSaveReplacesTheTokenForAHostAlreadyHeld(t *testing.T) {
	database, k := openTestDB(t), testKeyring(t)
	if err := SaveGitCredential(database, k, &GitCredential{Host: "github.com", Secret: "old"}); err != nil {
		t.Fatal(err)
	}
	MarkGitCredentialUsed(database, GitCredentialFor(database, k, "github.com").ID)

	if err := SaveGitCredential(database, k, &GitCredential{Host: "github.com", Secret: "rotated"}); err != nil {
		t.Fatal(err)
	}
	got := GitCredentialFor(database, k, "github.com")
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

func TestNormalizeGitHostAcceptsWhatOperatorsActuallyPaste(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"github.com", "github.com"},
		{"  GitHub.com  ", "github.com"},
		{"https://github.com/owner/repo.git", "github.com"},
		{"github.com/owner/repo", "github.com"},
		{"git@github.com", "github.com"},
		{"https://git.example.com:8443/g/r.git", "git.example.com:8443"},
		{"*", "*"},
		{"", ""},
	} {
		if got := NormalizeGitHost(tc.in); got != tc.want {
			t.Errorf("NormalizeGitHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGitHostOfOnlyClaimsHTTPS(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://github.com/owner/repo.git", "github.com"},
		{"https://GitHub.com/owner/repo.git", "github.com"},
		{"https://git.example.com:8443/g/r.git", "git.example.com:8443"},
		// Nothing else is a URL Quasar attaches a credential to, so nothing
		// else has a host worth reporting.
		{"git@github.com:owner/repo.git", ""},
		{"ssh://git@example.com/repo.git", ""},
		{"/srv/repos/local.git", ""},
	} {
		if got := GitHostOf(tc.in); got != tc.want {
			t.Errorf("GitHostOf(%q) = %q, want %q", tc.in, got, tc.want)
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
	got := GitCredentialFor(database, k, "anything.example.com")
	if got == nil || got.Secret != "ghp_legacyvalue123" {
		t.Fatal("the legacy token should now answer for every host")
	}
	if !got.IsFallback() {
		t.Errorf("migrated credential host = %q, want %q", got.Host, AnyHost)
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
	if err := SaveGitCredential(database, k, &GitCredential{Name: "mine", Host: AnyHost, Secret: "current"}); err != nil {
		t.Fatal(err)
	}
	SetSetting(database, SettingGitToken, "stale-legacy")

	if moved, err := MigrateGitToken(database, k); moved || err != nil {
		t.Errorf("migrate = %v, %v; want it to leave a configured fallback alone", moved, err)
	}
	if got := GitCredentialFor(database, k, "github.com"); got.Secret != "current" {
		t.Errorf("secret = %q, want the credential the operator configured", got.Secret)
	}
	if left := GetSetting(database, SettingGitToken); left != "" {
		t.Error("the superseded plaintext token should still be cleared")
	}
}
