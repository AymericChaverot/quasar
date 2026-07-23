package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	k, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}

	enc, err := k.Encrypt("SECRET_TOKEN=hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "hunter2") {
		t.Errorf("ciphertext leaks plaintext: %q", enc)
	}
	if !strings.HasPrefix(enc, encPrefix) {
		t.Errorf("ciphertext missing %q prefix: %q", encPrefix, enc)
	}

	dec, err := k.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "SECRET_TOKEN=hunter2" {
		t.Errorf("Decrypt = %q, want original plaintext", dec)
	}
}

func TestEncryptEmptyStringStaysEmpty(t *testing.T) {
	k, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := k.Encrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if enc != "" {
		t.Errorf("Encrypt(\"\") = %q, want empty", enc)
	}
	dec, err := k.Decrypt("")
	if err != nil {
		t.Fatal(err)
	}
	if dec != "" {
		t.Errorf("Decrypt(\"\") = %q, want empty", dec)
	}
}

func TestDecryptPassesThroughUnprefixedPlaintext(t *testing.T) {
	k, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	dec, err := k.Decrypt("KEY=plainvalue")
	if err != nil {
		t.Fatal(err)
	}
	if dec != "KEY=plainvalue" {
		t.Errorf("Decrypt of legacy plaintext = %q, want unchanged", dec)
	}
}

func TestLoadOrCreateKeyPersistsAcrossLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")

	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := k1.Encrypt("hello")
	if err != nil {
		t.Fatal(err)
	}

	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := k2.Decrypt(enc)
	if err != nil {
		t.Fatalf("second load couldn't decrypt first load's ciphertext: %v", err)
	}
	if dec != "hello" {
		t.Errorf("Decrypt = %q, want hello", dec)
	}
}

func TestLoadOrCreateKeyFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("NTFS doesn't enforce POSIX permission bits; this matters on the Linux hosts Quasar deploys to")
	}
	path := filepath.Join(t.TempDir(), "master.key")
	if _, err := LoadOrCreateKey(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("master key file mode = %o, want no group/other permissions", perm)
	}
}

func TestDecryptWithWrongKeyFails(t *testing.T) {
	k1, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := k1.Encrypt("hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k2.Decrypt(enc); err == nil {
		t.Error("Decrypt with a different key succeeded, want error")
	}
}
