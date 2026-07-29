package db

import (
	"database/sql"
	"net/url"
	"strings"
	"time"

	"quasar/internal/secrets"
)

// AnyHost is the host value of the credential used for forges no other row
// names. One token covers the common single-account install; the per-host rows
// exist for everyone who outgrew that.
const AnyHost = "*"

// GitCredential is the token Quasar hands to git when it clones or updates
// from one forge host.
//
// The secret is sealed with the master key like an app's env content, so the
// database file on its own gives up nothing. Listings never carry it: the page
// that shows credentials has no business decrypting them, and Hint is what it
// shows instead.
type GitCredential struct {
	ID         int64
	Name       string // what the operator calls it: "GitHub perso", "GitLab work"
	Host       string // "github.com", "git.example.com:8443", or AnyHost
	Username   string // basic-auth user; most forges ignore it, Bitbucket does not
	Hint       string // masked reminder, e.g. "ghp_…4f2a"
	Secret     string // plaintext; only ever set by the lookups that need it
	CreatedAt  time.Time
	LastUsedAt sql.NullTime
}

// IsFallback reports whether this credential is the any-host one.
func (c *GitCredential) IsFallback() bool { return c.Host == AnyHost }

// Account is how the credential authenticates, for display.
func (c *GitCredential) Account() string {
	if c.Username == "" {
		return DefaultGitUsername
	}
	return c.Username
}

// DefaultGitUsername is what goes in front of the token when the operator
// names no user. Every forge Quasar has been pointed at treats the token as
// the password and ignores the user — except Bitbucket, which is why the
// field is editable at all.
const DefaultGitUsername = "oauth2"

// SaveGitCredential creates or updates the credential for a host.
//
// An empty secret keeps whatever is already stored, so renaming a credential
// or correcting its username does not mean pasting the token again — and does
// not tempt anyone into keeping a copy of it somewhere to be able to.
func SaveGitCredential(database *sql.DB, k *secrets.Keyring, c *GitCredential) error {
	host := NormalizeGitHost(c.Host)
	if host == "" {
		return sql.ErrNoRows
	}
	if c.Secret == "" {
		res, err := database.Exec(
			"UPDATE git_credentials SET name = ?, username = ? WHERE host = ?",
			c.Name, c.Username, host)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return sql.ErrNoRows // nothing stored yet, so a token is required
		}
		return nil
	}
	sealed, err := k.Encrypt(c.Secret)
	if err != nil {
		return err
	}
	// The row is replaced rather than updated so a re-added host starts over
	// with a fresh created_at and no stale last-used date.
	_, err = database.Exec(`INSERT INTO git_credentials (name, host, username, secret, hint)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(host) DO UPDATE SET
			name = excluded.name, username = excluded.username,
			secret = excluded.secret, hint = excluded.hint, last_used_at = NULL`,
		c.Name, host, c.Username, sealed, MaskToken(c.Secret))
	return err
}

func DeleteGitCredential(database *sql.DB, id int64) error {
	_, err := database.Exec("DELETE FROM git_credentials WHERE id = ?", id)
	return err
}

// ListGitCredentials returns every stored credential without its secret,
// fallback last so the list reads most-specific first like the lookup does.
func ListGitCredentials(database *sql.DB) ([]*GitCredential, error) {
	rows, err := database.Query(`SELECT id, name, host, username, hint, created_at, last_used_at
		FROM git_credentials ORDER BY host = '*', host`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GitCredential
	for rows.Next() {
		var c GitCredential
		if err := rows.Scan(&c.ID, &c.Name, &c.Host, &c.Username, &c.Hint, &c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// GitCredentialFor resolves the credential a clone from host should use, or
// nil when nothing covers it. The secret is decrypted.
func GitCredentialFor(database *sql.DB, k *secrets.Keyring, host string) *GitCredential {
	for _, candidate := range hostCandidates(host) {
		if c := gitCredentialByHost(database, k, candidate); c != nil {
			return c
		}
	}
	return nil
}

// GitCredentialByID loads one credential with its secret, for the connection
// test — which has to authenticate exactly as a deploy would.
func GitCredentialByID(database *sql.DB, k *secrets.Keyring, id int64) *GitCredential {
	return scanGitCredential(database.QueryRow(
		`SELECT id, name, host, username, hint, secret, created_at, last_used_at
		 FROM git_credentials WHERE id = ?`, id), k)
}

func gitCredentialByHost(database *sql.DB, k *secrets.Keyring, host string) *GitCredential {
	return scanGitCredential(database.QueryRow(
		`SELECT id, name, host, username, hint, secret, created_at, last_used_at
		 FROM git_credentials WHERE host = ?`, host), k)
}

func scanGitCredential(row *sql.Row, k *secrets.Keyring) *GitCredential {
	var c GitCredential
	var sealed string
	if err := row.Scan(&c.ID, &c.Name, &c.Host, &c.Username, &c.Hint, &sealed,
		&c.CreatedAt, &c.LastUsedAt); err != nil {
		return nil
	}
	plain, err := k.Decrypt(sealed)
	if err != nil {
		// A credential sealed with a key this install no longer has is worse
		// than useless: handing git a ciphertext produces an authentication
		// failure that looks like a revoked token.
		return nil
	}
	c.Secret = plain
	return &c
}

// MarkGitCredentialUsed records that a deploy actually authenticated with this
// credential, which is the only way to tell a token that is doing work from
// one left behind by an app that has since been deleted.
func MarkGitCredentialUsed(database *sql.DB, id int64) {
	database.Exec("UPDATE git_credentials SET last_used_at = ? WHERE id = ?", time.Now(), id)
}

// hostCandidates lists the credential hosts that could serve a URL host, most
// specific first: the host as written (which may carry a port), then the bare
// hostname, then the any-host fallback.
func hostCandidates(host string) []string {
	host = NormalizeGitHost(host)
	if host == "" {
		return nil
	}
	out := []string{host}
	if bare, _, found := strings.Cut(host, ":"); found && bare != "" {
		out = append(out, bare)
	}
	return append(out, AnyHost)
}

// NormalizeGitHost reduces what an operator typed into the host field to the
// form a URL is matched against. A whole clone URL is accepted because pasting
// one is the obvious mistake, and rejecting it would teach nothing.
func NormalizeGitHost(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == AnyHost {
		return raw
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return u.Host
		}
	}
	// "github.com/owner/repo" and a trailing slash both mean the host.
	raw = strings.TrimPrefix(raw, "git@")
	if before, _, found := strings.Cut(raw, "/"); found {
		raw = before
	}
	return raw
}

// GitHostOf returns the host a clone URL authenticates against, or "" when the
// URL is not one Quasar injects credentials into.
func GitHostOf(rawURL string) string {
	if !strings.HasPrefix(rawURL, "https://") {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// MaskToken is the reminder shown in place of a stored token: enough to tell
// two of them apart and to recognise a rotation, never enough to use.
//
// The type prefix forges put on their tokens (ghp_, glpat-, github_pat_) is
// kept because it says more about which credential this is than any slice of
// the random part would.
func MaskToken(secret string) string {
	if len(secret) < 8 {
		return "…"
	}
	prefix := ""
	if i := strings.IndexAny(secret, "_-"); i > 0 && i <= 12 {
		prefix = secret[:i+1]
	}
	return prefix + "…" + secret[len(secret)-4:]
}

// MigrateGitToken moves the single platform-wide token of earlier versions
// into the credentials table.
//
// It lands on AnyHost because that is exactly what the old setting did: it was
// injected into every https clone, whatever forge it pointed at. Anything
// narrower would be a guess, and a wrong guess breaks a deploy that worked
// yesterday.
func MigrateGitToken(database *sql.DB, k *secrets.Keyring) (bool, error) {
	token := GetSetting(database, SettingGitToken)
	if token == "" {
		return false, nil
	}
	var exists int
	if err := database.QueryRow("SELECT COUNT(*) FROM git_credentials WHERE host = ?", AnyHost).
		Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		err := SaveGitCredential(database, k, &GitCredential{
			Name:   "Imported token",
			Host:   AnyHost,
			Secret: token,
		})
		if err != nil {
			return false, err
		}
	}
	// Cleared only once the credential is safely stored, so an interrupted
	// migration is retried rather than losing the token. It was held in
	// plaintext here; the credentials table seals it.
	SetSetting(database, SettingGitToken, "")
	return exists == 0, nil
}
