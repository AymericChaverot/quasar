package auth

import (
	"database/sql"
	"encoding/base64"
	"errors"

	"github.com/pquerna/otp/totp"
	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

// BeginTOTPSetup generates and stores a new TOTP secret (not yet enabled) and
// returns the secret plus a data-URI QR code for authenticator apps.
func BeginTOTPSetup(db *sql.DB, userID int64, issuer, account string) (secret, qrDataURI string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account})
	if err != nil {
		return "", "", err
	}
	if _, err := db.Exec("UPDATE users SET totp_secret = ?, totp_enabled = 0 WHERE id = ?", key.Secret(), userID); err != nil {
		return "", "", err
	}
	png, err := qrcode.Encode(key.URL(), qrcode.Medium, 200)
	if err != nil {
		return "", "", err
	}
	return key.Secret(), "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

// EnableTOTP activates 2FA after the user proves they can generate a code.
func EnableTOTP(db *sql.DB, userID int64, code string) error {
	var secret string
	if err := db.QueryRow("SELECT totp_secret FROM users WHERE id = ?", userID).Scan(&secret); err != nil {
		return err
	}
	if secret == "" || !totp.Validate(code, secret) {
		return errors.New("invalid verification code")
	}
	_, err := db.Exec("UPDATE users SET totp_enabled = 1 WHERE id = ?", userID)
	return err
}

// DisableTOTP turns 2FA off after re-checking the account password.
func DisableTOTP(db *sql.DB, userID int64, password string) error {
	var hash string
	if err := db.QueryRow("SELECT password_hash FROM users WHERE id = ?", userID).Scan(&hash); err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return errors.New("password is incorrect")
	}
	_, err := db.Exec("UPDATE users SET totp_enabled = 0, totp_secret = '' WHERE id = ?", userID)
	return err
}

func TOTPEnabled(db *sql.DB, userID int64) bool {
	var enabled bool
	db.QueryRow("SELECT totp_enabled FROM users WHERE id = ?", userID).Scan(&enabled)
	return enabled
}

// Confirm2FA validates a login-time TOTP code and promotes the pending
// session to a fully authenticated one.
func Confirm2FA(db *sql.DB, token, code string) error {
	var secret string
	err := db.QueryRow(`SELECT u.totp_secret FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token = ? AND s.pending_2fa = 1`, token).Scan(&secret)
	if err != nil {
		return errors.New("no pending login")
	}
	if !totp.Validate(code, secret) {
		return errors.New("invalid code")
	}
	_, err = db.Exec("UPDATE sessions SET pending_2fa = 0 WHERE token = ?", token)
	return err
}

// PendingSession reports whether the token is a valid session awaiting 2FA.
func PendingSession(db *sql.DB, token string) bool {
	var pending bool
	err := db.QueryRow("SELECT pending_2fa FROM sessions WHERE token = ? AND expires_at > CURRENT_TIMESTAMP", token).Scan(&pending)
	return err == nil && pending
}
