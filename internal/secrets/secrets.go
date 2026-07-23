// Package secrets encrypts values Quasar stores at rest — app env vars and
// compose definitions — with a key generated on first boot and kept out of
// backup archives (see backup.Run, which never touches the key's path).
//
// This protects the realistic leak vector for a self-hosted single-VPS
// platform: a backup archive or the SQLite file copied out on its own. It
// does not protect against a fully compromised host, which can already read
// every secret straight out of the running containers.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const keySize = 32 // AES-256

// encPrefix marks a stored value as encrypted with this scheme, so plaintext
// left over from installs created before this feature existed can still be
// read (and gets re-saved encrypted the next time it's written).
const encPrefix = "enc:v1:"

// Keyring seals and opens stored values with the platform's master key.
type Keyring struct {
	gcm cipher.AEAD
}

// LoadOrCreateKey reads the master key from path, generating and persisting
// a new random one on first run. Callers must keep this path out of backup
// archives — the whole point is that a leaked backup or database file alone
// can't be decrypted.
func LoadOrCreateKey(path string) (*Keyring, error) {
	key, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		key = make([]byte, keySize)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("generate master key: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create master key dir: %w", err)
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, fmt.Errorf("write master key: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("read master key: %w", err)
	case len(key) != keySize:
		return nil, fmt.Errorf("master key at %s is %d bytes, want %d — delete it to regenerate (existing encrypted values become unreadable)", path, len(key), keySize)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keyring{gcm: gcm}, nil
}

// Encrypt seals plaintext for storage. Empty input round-trips as empty, so
// unset env vars don't grow a ciphertext prefix.
func (k *Keyring) Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, k.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := k.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// IsEncrypted reports whether a stored value already carries this package's
// encryption marker. It exists for one-time migrations of pre-existing
// plaintext data, which must check before encrypting so they don't wrap an
// already-encrypted value a second time.
func IsEncrypted(stored string) bool {
	return strings.HasPrefix(stored, encPrefix)
}

// Decrypt reverses Encrypt. A value without the enc:v1: prefix is returned
// unchanged — it's plaintext from before this feature existed, or already
// empty — so old databases keep working until each row is next written.
func (k *Keyring) Decrypt(stored string) (string, error) {
	rest, ok := strings.CutPrefix(stored, encPrefix)
	if !ok {
		return stored, nil
	}
	sealed, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return "", fmt.Errorf("decode stored value: %w", err)
	}
	ns := k.gcm.NonceSize()
	if len(sealed) < ns {
		return "", errors.New("stored value too short")
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	plain, err := k.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt stored value: %w", err)
	}
	return string(plain), nil
}
