package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookie   = "quasar_session"
	sessionLifetime = 7 * 24 * time.Hour
)

// Roles. A viewer can see everything the dashboard shows but change nothing,
// which also excludes the two read-only actions that hand over real power: a
// container shell and the encryption master key.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

func ValidRole(role string) bool { return role == RoleAdmin || role == RoleViewer }

// User is an account as shown on the settings page.
type User struct {
	ID          int64
	Username    string
	Role        string
	TOTPEnabled bool
	CreatedAt   time.Time
}

// ListUsers returns every account, oldest first.
func ListUsers(db *sql.DB) ([]*User, error) {
	rows, err := db.Query("SELECT id, username, role, totp_enabled, created_at FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.TOTPEnabled, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &u)
	}
	return out, rows.Err()
}

// CreateUser adds an account. Usernames are unique, enforced by the schema.
func CreateUser(db *sql.DB, username, password, role string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("username is required")
	}
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if !ValidRole(role) {
		return errors.New("role must be admin or viewer")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := db.Exec("INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)",
		username, string(hash), role); err != nil {
		return fmt.Errorf("could not create %q (the name may already be taken): %w", username, err)
	}
	return nil
}

// SetRole changes an account's role, refusing to remove the last admin.
// Locking every human out of a self-hosted dashboard is unrecoverable without
// SSH and sqlite3, so it is blocked rather than confirmed.
func SetRole(db *sql.DB, userID int64, role string) error {
	if !ValidRole(role) {
		return errors.New("role must be admin or viewer")
	}
	if role != RoleAdmin {
		if err := ensureNotLastAdmin(db, userID); err != nil {
			return err
		}
	}
	_, err := db.Exec("UPDATE users SET role = ? WHERE id = ?", role, userID)
	return err
}

// DeleteUser removes an account and its sessions, refusing to remove the last
// admin for the same reason as SetRole.
func DeleteUser(db *sql.DB, userID int64) error {
	if err := ensureNotLastAdmin(db, userID); err != nil {
		return err
	}
	if _, err := db.Exec("DELETE FROM users WHERE id = ?", userID); err != nil {
		return err
	}
	_, err := db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

// ResetPassword sets another account's password without knowing the old one,
// and signs that account out everywhere.
func ResetPassword(db *sql.DB, userID int64, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err := db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(hash), userID); err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM sessions WHERE user_id = ?", userID)
	return err
}

func ensureNotLastAdmin(db *sql.DB, userID int64) error {
	var role string
	if err := db.QueryRow("SELECT role FROM users WHERE id = ?", userID).Scan(&role); err != nil {
		return err
	}
	if role != RoleAdmin {
		return nil
	}
	var admins int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE role = ?", RoleAdmin).Scan(&admins); err != nil {
		return err
	}
	if admins <= 1 {
		return errors.New("this is the last admin account — promote another user first")
	}
	return nil
}

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
	if _, err := db.Exec("INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)",
		username, string(hash), RoleAdmin); err != nil {
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

// UserForSession returns the id, username and role tied to a valid session.
func UserForSession(db *sql.DB, token string) (int64, string, string, error) {
	var id int64
	var username, role string
	err := db.QueryRow(`SELECT u.id, u.username, u.role FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.pending_2fa = 0 AND s.expires_at > ?`, token, time.Now()).
		Scan(&id, &username, &role)
	return id, username, role, err
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
