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

// Token is an API credential. The secret itself is never stored — only its
// hash — so a copy of the database does not hand over working credentials the
// way it would with plaintext tokens.
type Token struct {
	ID         int64
	Name       string
	Role       string
	Prefix     string // first characters of the secret, to tell tokens apart in the UI
	CreatedAt  time.Time
	LastUsedAt sql.NullTime
}

// CreateToken issues a token and returns the secret, which is the only time it
// is ever available.
func CreateToken(db *sql.DB, name, role string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("a token name is required")
	}
	if !ValidRole(role) {
		return "", errors.New("role must be admin or viewer")
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	secret := TokenPrefix + hex.EncodeToString(buf)

	if _, err := db.Exec(
		"INSERT INTO api_tokens (name, role, prefix, token_hash) VALUES (?, ?, ?, ?)",
		name, role, tokenDisplayPrefix(secret), hashToken(secret)); err != nil {
		return "", fmt.Errorf("could not create the token: %w", err)
	}
	return secret, nil
}

// AuthenticateToken resolves a presented secret to the token's name and the
// role it grants, and records that the token was used. An unknown or malformed
// secret yields an error, with no detail about which — the caller either has a
// valid token or does not.
//
// Lookup is by hash, so this is one indexed comparison rather than a scan.
func AuthenticateToken(db *sql.DB, secret string) (name, role string, err error) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return "", "", errors.New("invalid token")
	}
	var id int64
	if err := db.QueryRow("SELECT id, name, role FROM api_tokens WHERE token_hash = ?",
		hashToken(secret)).Scan(&id, &name, &role); err != nil {
		return "", "", errors.New("invalid token")
	}
	// Best-effort: a failed timestamp update must not reject a valid request.
	db.Exec("UPDATE api_tokens SET last_used_at = ? WHERE id = ?", time.Now(), id)
	return name, role, nil
}

func ListTokens(db *sql.DB) ([]*Token, error) {
	rows, err := db.Query("SELECT id, name, role, prefix, created_at, last_used_at FROM api_tokens ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.Prefix, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// DeleteToken revokes a token. Revocation is immediate: the next request
// carrying it finds no matching hash.
func DeleteToken(db *sql.DB, id int64) (string, error) {
	var name string
	if err := db.QueryRow("SELECT name FROM api_tokens WHERE id = ?", id).Scan(&name); err != nil {
		return "", err
	}
	_, err := db.Exec("DELETE FROM api_tokens WHERE id = ?", id)
	return name, err
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
