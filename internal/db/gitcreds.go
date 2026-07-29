package db

import (
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"time"

	"quasar/internal/secrets"
)

// AnyScope is the scope of the credential used for repositories no other row
// covers. One token serves the common single-account install; the narrower
// scopes exist for everyone who outgrew that.
const AnyScope = "*"

// GitCredential is the token Quasar hands to git when it clones or updates a
// repository.
//
// The secret is sealed with the master key like an app's env content, so the
// database file on its own gives up nothing. Listings never carry it: the page
// that shows credentials has no business decrypting them, and Hint is what it
// shows instead.
type GitCredential struct {
	ID   int64
	Name string // what the operator calls it: "GitHub — work", "GitLab perso"
	// Scope is how much of a forge this token answers for: "github.com",
	// "github.com/acme", "github.com/acme/api", or AnyScope.
	Scope      string
	Username   string // basic-auth user; most forges ignore it, Bitbucket does not
	Hint       string // masked reminder, e.g. "ghp_…4f2a"
	Secret     string // plaintext; only ever set by the lookups that need it
	CreatedAt  time.Time
	LastUsedAt sql.NullTime
}

// IsFallback reports whether this credential covers everything left over.
func (c *GitCredential) IsFallback() bool { return c.Scope == AnyScope }

// Host is the forge the scope names, without the owner or repository part.
func (c *GitCredential) Host() string {
	host, _, _ := strings.Cut(c.Scope, "/")
	return host
}

// Owns reports whether the scope narrows to part of a forge rather than all
// of it, which is what the page marks as covering only some repositories.
func (c *GitCredential) Owns() string {
	_, path, _ := strings.Cut(c.Scope, "/")
	return path
}

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

// SaveGitCredential creates or updates the credential for a scope.
//
// An empty secret keeps whatever is already stored, so renaming a credential
// or correcting its username does not mean pasting the token again — and does
// not tempt anyone into keeping a copy of it somewhere to be able to.
func SaveGitCredential(database *sql.DB, k *secrets.Keyring, c *GitCredential) error {
	scope := NormalizeGitScope(c.Scope)
	if scope == "" {
		return sql.ErrNoRows
	}
	if c.Secret == "" {
		res, err := database.Exec(
			"UPDATE git_credentials SET name = ?, username = ? WHERE scope = ?",
			c.Name, c.Username, scope)
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
	_, err = database.Exec(`INSERT INTO git_credentials (name, scope, username, secret, hint)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(scope) DO UPDATE SET
			name = excluded.name, username = excluded.username,
			secret = excluded.secret, hint = excluded.hint, last_used_at = NULL`,
		c.Name, scope, c.Username, sealed, MaskToken(c.Secret))
	return err
}

// ErrGitScopeTaken is returned when a credential is moved onto a scope another
// one already holds. Scopes are unique because two tokens for the same target
// would make which one a deploy gets a matter of row order.
var ErrGitScopeTaken = errors.New("another credential already covers that scope")

// UpdateGitCredentialMeta changes what a credential is called, what it covers
// and what it authenticates as, leaving the stored token alone.
//
// The token deliberately has no edit path: it cannot be read back, so an edit
// form could only ever offer to replace it — which is what saving over a scope
// already does. Everything around it is a label that was typed once and may
// well have been typed wrong, and re-entering a token to fix a name is exactly
// the friction that leads to tokens being kept in a file somewhere.
func UpdateGitCredentialMeta(database *sql.DB, id int64, name, scope, username string) error {
	scope = NormalizeGitScope(scope)
	if scope == "" {
		return sql.ErrNoRows
	}
	// Checked rather than left to the unique index, so the page can say which
	// credential is in the way instead of reporting a constraint.
	var other int64
	switch err := database.QueryRow(
		"SELECT id FROM git_credentials WHERE scope = ? AND id <> ?", scope, id).Scan(&other); {
	case err == nil:
		return ErrGitScopeTaken
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	res, err := database.Exec(
		"UPDATE git_credentials SET name = ?, scope = ?, username = ? WHERE id = ?",
		name, scope, username, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func DeleteGitCredential(database *sql.DB, id int64) error {
	_, err := database.Exec("DELETE FROM git_credentials WHERE id = ?", id)
	return err
}

// ListGitCredentials returns every stored credential without its secret, in
// the order the lookup considers them: narrowest first, fallback last.
func ListGitCredentials(database *sql.DB) ([]*GitCredential, error) {
	rows, err := database.Query(`SELECT id, name, scope, username, hint, created_at, last_used_at
		FROM git_credentials
		ORDER BY scope = '*', (length(scope) - length(replace(scope, '/', ''))) DESC, scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*GitCredential
	for rows.Next() {
		var c GitCredential
		if err := rows.Scan(&c.ID, &c.Name, &c.Scope, &c.Username, &c.Hint, &c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// GitCredentialFor resolves the credential a clone of rawURL should use, or
// nil when nothing covers it. The secret is decrypted.
//
// The most specific scope wins, so a token for one organisation is never
// overruled by the forge-wide one it sits under, and neither is overruled by
// the fallback. Everything is compared in one pass rather than by trying
// candidate keys in order, because "most specific" is a property of the rows
// that exist, not of a fixed list of shapes.
func GitCredentialFor(database *sql.DB, k *secrets.Keyring, rawURL string) *GitCredential {
	target := GitTargetOf(rawURL)
	if target == "" {
		return nil
	}
	rows, err := database.Query(`SELECT id, name, scope, username, hint, secret, created_at, last_used_at
		FROM git_credentials`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var best *GitCredential
	var bestSealed string
	bestScore := 0
	for rows.Next() {
		var c GitCredential
		var sealed string
		if err := rows.Scan(&c.ID, &c.Name, &c.Scope, &c.Username, &c.Hint, &sealed,
			&c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil
		}
		if score := gitScopeMatch(c.Scope, target); score > bestScore {
			best, bestSealed, bestScore = &c, sealed, score
		}
	}
	if best == nil {
		return nil
	}
	plain, err := k.Decrypt(bestSealed)
	if err != nil {
		// A credential sealed with a key this install no longer has is worse
		// than useless: handing git a ciphertext produces an authentication
		// failure that looks like a revoked token.
		return nil
	}
	best.Secret = plain
	return best
}

// GitCredentialByID loads one credential with its secret.
func GitCredentialByID(database *sql.DB, k *secrets.Keyring, id int64) *GitCredential {
	var c GitCredential
	var sealed string
	err := database.QueryRow(`SELECT id, name, scope, username, hint, secret, created_at, last_used_at
		FROM git_credentials WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Scope, &c.Username, &c.Hint, &sealed, &c.CreatedAt, &c.LastUsedAt)
	if err != nil {
		return nil
	}
	if c.Secret, err = k.Decrypt(sealed); err != nil {
		return nil
	}
	return &c
}

// MarkGitCredentialUsed records that a deploy actually authenticated with this
// credential, which is the only way to tell a token that is doing work from
// one left behind by an app that has since been deleted.
func MarkGitCredentialUsed(database *sql.DB, id int64) {
	database.Exec("UPDATE git_credentials SET last_used_at = ? WHERE id = ?", time.Now(), id)
}

// gitScopeMatch reports how well scope covers target, higher being more
// specific; 0 means it does not cover it at all.
//
// A scope matches when it is a prefix of the target on a segment boundary, so
// "github.com/acme" covers acme's repositories and never "acmecorp"'s.
func gitScopeMatch(scope, target string) int {
	if scope == AnyScope {
		return 1 // covers everything, and is beaten by anything naming a host
	}
	scopeHost, scopePath, _ := strings.Cut(scope, "/")
	targetHost, targetPath, _ := strings.Cut(target, "/")

	// A scope naming a port must match it, but one that names none still
	// covers a forge served on a non-default port.
	exactHost := scopeHost == targetHost
	if !exactHost && scopeHost != hostWithoutPort(targetHost) {
		return 0
	}
	if scopePath != "" && !segmentPrefix(scopePath, targetPath) {
		return 0
	}
	// Every named path segment outranks the host alone; naming the port breaks
	// the tie between two scopes that are otherwise equally specific.
	score := 2 + 2*countSegments(scopePath)
	if exactHost {
		score++
	}
	return score
}

// segmentPrefix reports whether prefix covers path without cutting a segment
// in half: "acme" covers "acme/api" but not "acmecorp/api".
func segmentPrefix(prefix, path string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func countSegments(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, "/") + 1
}

func hostWithoutPort(host string) string {
	if before, _, found := strings.Cut(host, ":"); found {
		return before
	}
	return host
}

// NormalizeGitScope reduces what an operator typed into the scope field to the
// form a URL is matched against.
//
// A whole clone URL is accepted because pasting one is the obvious thing to
// do, and the trailing "/*" people write to mean "everything under here" is
// simply dropped — a scope already covers everything beneath it.
func NormalizeGitScope(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" || raw == AnyScope {
		return raw
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			raw = u.Host + u.Path
		}
	}
	raw = strings.TrimPrefix(raw, "git@")
	raw = strings.Trim(raw, "/")
	raw = strings.TrimSuffix(raw, "/*")
	raw = strings.TrimSuffix(raw, ".git")
	return strings.Trim(raw, "/")
}

// GitTargetOf reduces a clone URL to what scopes are matched against —
// "github.com/acme/api" — or "" when the URL is not one Quasar attaches a
// credential to.
func GitTargetOf(rawURL string) string {
	if !strings.HasPrefix(rawURL, "https://") {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return NormalizeGitScope(u.Host + u.Path)
}

// GitHostOf returns the host a clone URL authenticates against, or "" when the
// URL is not one Quasar injects credentials into.
func GitHostOf(rawURL string) string {
	host, _, _ := strings.Cut(GitTargetOf(rawURL), "/")
	return host
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
// It lands on AnyScope because that is exactly what the old setting did: it
// was injected into every https clone, whatever forge it pointed at. Anything
// narrower would be a guess, and a wrong guess breaks a deploy that worked
// yesterday.
func MigrateGitToken(database *sql.DB, k *secrets.Keyring) (bool, error) {
	token := GetSetting(database, SettingGitToken)
	if token == "" {
		return false, nil
	}
	var exists int
	if err := database.QueryRow("SELECT COUNT(*) FROM git_credentials WHERE scope = ?", AnyScope).
		Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		err := SaveGitCredential(database, k, &GitCredential{
			Name:   "Imported token",
			Scope:  AnyScope,
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
