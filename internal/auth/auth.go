package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookie   = "quasar_session"
	sessionLifetime = 7 * 24 * time.Hour
)

// EnsureAdmin creates the initial admin account on first boot. It only runs
// when the users table is empty, so the ADMIN_* env vars are ignored afterwards.
func EnsureAdmin(db *sql.DB, username, password string) error {
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if username == "" || password == "" {
		return errors.New("no user exists and ADMIN_USER / ADMIN_PASSWORD are not set")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, string(hash)); err != nil {
		return err
	}
	log.Printf("created initial admin user %q — you can now remove ADMIN_PASSWORD from .env", username)
	return nil
}

// Login checks credentials and returns a new session token. When the user
// has 2FA enabled, the session starts in a pending state and needs2FA is
// true: the token only grants access to the 2FA confirmation step.
func Login(db *sql.DB, username, password string) (token string, needs2FA bool, err error) {
	var id int64
	var hash string
	var totpEnabled bool
	err = db.QueryRow("SELECT id, password_hash, totp_enabled FROM users WHERE username = ?", username).
		Scan(&id, &hash, &totpEnabled)
	if err != nil {
		// Run bcrypt anyway so unknown users take as long as bad passwords.
		bcrypt.CompareHashAndPassword([]byte("$2a$10$0000000000000000000000000000000000000000000000000000"), []byte(password))
		return "", false, errors.New("invalid credentials")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", false, errors.New("invalid credentials")
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	token = hex.EncodeToString(buf)
	_, err = db.Exec("INSERT INTO sessions (token, user_id, pending_2fa, expires_at) VALUES (?, ?, ?, ?)",
		token, id, totpEnabled, time.Now().Add(sessionLifetime))
	if err != nil {
		return "", false, err
	}
	return token, totpEnabled, nil
}

func Logout(db *sql.DB, token string) {
	db.Exec("DELETE FROM sessions WHERE token = ?", token)
}

// UserForSession returns the id and username tied to a valid session token.
func UserForSession(db *sql.DB, token string) (int64, string, error) {
	var id int64
	var username string
	err := db.QueryRow(`SELECT u.id, u.username FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.pending_2fa = 0 AND s.expires_at > ?`, token, time.Now()).Scan(&id, &username)
	return id, username, err
}

// ChangePassword verifies the current password, stores the new hash and
// invalidates every other session of the user.
func ChangePassword(db *sql.DB, userID int64, keepToken, current, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("new password must be at least 8 characters")
	}
	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id = ?", userID).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		return errors.New("current password is incorrect")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(newHash), userID); err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM sessions WHERE user_id = ? AND token != ?", userID, keepToken)
	return err
}

// ClearAllSessions signs out every session on the platform.
func ClearAllSessions(db *sql.DB) error {
	_, err := db.Exec("DELETE FROM sessions")
	return err
}

// Valid reports whether the session token is fully authenticated (exists,
// not expired, and not waiting on a 2FA confirmation).
func Valid(db *sql.DB, token string) bool {
	if token == "" {
		return false
	}
	var expires time.Time
	var pending bool
	err := db.QueryRow("SELECT expires_at, pending_2fa FROM sessions WHERE token = ?", token).Scan(&expires, &pending)
	if err != nil {
		return false
	}
	if time.Now().After(expires) {
		db.Exec("DELETE FROM sessions WHERE token = ?", token)
		return false
	}
	return !pending
}

// SetCookie writes the session cookie. secure=false is only meant for local
// development over plain HTTP; production always runs behind Traefik TLS.
func SetCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionLifetime.Seconds()),
	})
}

func ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
