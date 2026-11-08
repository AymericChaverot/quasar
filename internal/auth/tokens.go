package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TokenPrefix marks a Quasar API token so a leaked one is recognisable in a
// log or a secret scanner.
const TokenPrefix = "qsr_"

// There are two kinds of token, in the same table and with the same secret
// format.
//
// A service token belongs to nobody: an admin issues it for a pipeline, with a
// role of its own, and it lasts until it is revoked.
//
// A personal token belongs to the account that created it, which is what the
// command-line client signs in with. It has no role of its own: it acts with
// its owner's role as it stands at each request, so demoting the owner demotes
// the token, and deleting the owner deletes it. It expires when its owner
// chose it would.

// Token is an API credential as the settings page lists it. The secret itself
// is never stored — only its hash — so a copy of the database does not hand
// over working credentials the way it would with plaintext tokens.
type Token struct {
	ID         int64
	Name       string
	Role       string // the service token's own role; empty for a personal token
	Prefix     string // first characters of the secret, to tell tokens apart in the UI
	CreatedAt  time.Time
	LastUsedAt sql.NullTime
	ExpiresAt  sql.NullTime
	UserID     int64  // the owner of a personal token; 0 for a service token
	Username   string // the owner's name, for a personal token
}

// Personal reports whether the token belongs to an account.
func (t *Token) Personal() bool { return t.UserID != 0 }

// Expired reports whether the token has outlived its expiry.
func (t *Token) Expired() bool { return t.ExpiresAt.Valid && !time.Now().Before(t.ExpiresAt.Time) }

// Identity is who a presented token acts as.
type Identity struct {
	TokenID   int64
	Token     string // the token's name
	UserID    int64  // 0 for a service token
	Username  string // empty for a service token
	Role      string
	ExpiresAt sql.NullTime
}

// Personal reports whether the request is made on an account's behalf.
func (id *Identity) Personal() bool { return id.UserID != 0 }

// ErrTokenExpired is the refusal of a personal token past its expiry. It is
// told apart from an unknown token because whoever presents it holds the
// secret already, and "expired" tells them what to do about it.
var ErrTokenExpired = errors.New("token expired")

// TokenLifetimes are the expiries a personal token can be created with, in
// the order the form offers them. Zero is never.
var TokenLifetimes = []struct {
	Value string
	Label string
	TTL   time.Duration
}{
	{"30d", "30 days", 30 * 24 * time.Hour},
	{"90d", "90 days", 90 * 24 * time.Hour},
	{"365d", "1 year", 365 * 24 * time.Hour},
	{"never", "never", 0},
}

// TokenLifetime reads an expiry choice from the form.
func TokenLifetime(choice string) (time.Duration, error) {
	for _, l := range TokenLifetimes {
		if l.Value == choice {
			return l.TTL, nil
		}
	}
	return 0, errors.New("pick when the token expires")
}

// newSecret draws a token secret: 32 random bytes behind the prefix.
func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return TokenPrefix + hex.EncodeToString(buf), nil
}

// CreateToken issues a service token and returns the secret, which is the
// only time it is ever available.
func CreateToken(db *sql.DB, name, role string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("a token name is required")
	}
	if !ValidRole(role) {
		return "", errors.New("role must be admin or viewer")
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	if _, err := db.Exec(
		"INSERT INTO api_tokens (name, role, prefix, token_hash) VALUES (?, ?, ?, ?)",
		name, role, tokenDisplayPrefix(secret), hashToken(secret)); err != nil {
		return "", fmt.Errorf("could not create the token: %w", err)
	}
	return secret, nil
}

// CreatePersonalToken issues a token for an account and returns the secret,
// which is the only time it is ever available. A ttl of zero never expires.
func CreatePersonalToken(db *sql.DB, userID int64, name string, ttl time.Duration) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("a token name is required")
	}
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	var expires sql.NullTime
	if ttl > 0 {
		expires = sql.NullTime{Time: time.Now().Add(ttl), Valid: true}
	}
	// The role column stays empty: a personal token reads its owner's role at
	// every request instead.
	if _, err := db.Exec(
		"INSERT INTO api_tokens (name, role, prefix, token_hash, user_id, expires_at) VALUES (?, '', ?, ?, ?, ?)",
		name, tokenDisplayPrefix(secret), hashToken(secret), userID, expires); err != nil {
		return "", fmt.Errorf("could not create the token: %w", err)
	}
	return secret, nil
}

// AuthenticateToken resolves a presented secret to who it acts as, and
// records that the token was used. An unknown or malformed secret yields an
// error with no detail about which — the caller either has a valid token or
// does not. An expired one yields ErrTokenExpired.
//
// Lookup is by hash, so this is one indexed comparison rather than a scan.
func AuthenticateToken(db *sql.DB, secret string) (*Identity, error) {
	invalid := errors.New("invalid token")
	if !strings.HasPrefix(secret, TokenPrefix) {
		return nil, invalid
	}
	var (
		id                 Identity
		ownRole            string
		userID             sql.NullInt64
		username, userRole sql.NullString
	)
	err := db.QueryRow(`SELECT t.id, t.name, t.role, t.user_id, t.expires_at, u.username, u.role
		FROM api_tokens t LEFT JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, hashToken(secret)).
		Scan(&id.TokenID, &id.Token, &ownRole, &userID, &id.ExpiresAt, &username, &userRole)
	if err != nil {
		return nil, invalid
	}
	if userID.Valid {
		// A token whose owner is gone is no token at all. The foreign key
		// deletes it with the account; this holds even if that ever failed.
		if !username.Valid {
			return nil, invalid
		}
		id.UserID, id.Username, id.Role = userID.Int64, username.String, userRole.String
	} else {
		id.Role = ownRole
	}
	if id.ExpiresAt.Valid && !time.Now().Before(id.ExpiresAt.Time) {
		return nil, ErrTokenExpired
	}
	// Best-effort: a failed timestamp update must not reject a valid request.
	_, _ = db.Exec("UPDATE api_tokens SET last_used_at = ? WHERE id = ?", time.Now(), id.TokenID)
	return &id, nil
}

const tokenSelect = `SELECT t.id, t.name, t.role, t.prefix, t.created_at, t.last_used_at, t.expires_at,
	COALESCE(t.user_id, 0), COALESCE(u.username, '')
	FROM api_tokens t LEFT JOIN users u ON u.id = t.user_id`

func listTokens(db *sql.DB, where string, args ...any) ([]*Token, error) {
	rows, err := db.Query(tokenSelect+" WHERE "+where+" ORDER BY t.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.Prefix, &t.CreatedAt, &t.LastUsedAt,
			&t.ExpiresAt, &t.UserID, &t.Username); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ListTokens lists the service tokens.
func ListTokens(db *sql.DB) ([]*Token, error) {
	return listTokens(db, "t.user_id IS NULL")
}

// ListPersonalTokens lists one account's personal tokens, or everyone's when
// userID is 0.
func ListPersonalTokens(db *sql.DB, userID int64) ([]*Token, error) {
	if userID == 0 {
		return listTokens(db, "t.user_id IS NOT NULL")
	}
	return listTokens(db, "t.user_id = ?", userID)
}

// DeleteToken revokes a service token. Revocation is immediate: the next
// request carrying it finds no matching hash.
func DeleteToken(db *sql.DB, id int64) (string, error) {
	var name string
	if err := db.QueryRow("SELECT name FROM api_tokens WHERE id = ? AND user_id IS NULL", id).Scan(&name); err != nil {
		return "", err
	}
	_, err := db.Exec("DELETE FROM api_tokens WHERE id = ?", id)
	return name, err
}

// DeletePersonalToken revokes a personal token and returns its name and its
// owner's. With ownerID set, only that account's token is found: a user
// revokes their own tokens, and only an admin (ownerID 0) anybody's.
func DeletePersonalToken(db *sql.DB, id, ownerID int64) (name, owner string, err error) {
	q := "SELECT t.name, u.username FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.id = ?"
	args := []any{id}
	if ownerID != 0 {
		q += " AND t.user_id = ?"
		args = append(args, ownerID)
	}
	if err := db.QueryRow(q, args...).Scan(&name, &owner); err != nil {
		return "", "", err
	}
	_, err = db.Exec("DELETE FROM api_tokens WHERE id = ?", id)
	return name, owner, err
}

// hashToken is a plain SHA-256: unlike a password, a token is 32 bytes of
// entropy this server generated, so there is nothing to brute-force and no
// reason to pay bcrypt's cost on every API request.
func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// tokenDisplayPrefix keeps enough of the secret to identify which token a row
// refers to, and far too little to use.
func tokenDisplayPrefix(secret string) string {
	end := len(TokenPrefix) + 6
	if end > len(secret) {
		end = len(secret)
	}
	return secret[:end]
}
