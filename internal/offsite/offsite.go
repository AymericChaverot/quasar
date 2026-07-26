// Package offsite copies backup archives to S3-compatible object storage.
//
// Backups written only to /opt/quasar/backups sit on the same disk as
// everything they protect: the VPS that dies takes them with it, and a disk
// that fills stops producing them. This is the copy that survives the host.
//
// The S3 request is signed here rather than through an SDK. A single PUT needs
// one well-specified signature, and the alternative pulled 200 modules into a
// platform whose whole shape is one static binary plus SQLite. Signing failures
// are not silent: they surface as a 4xx from the provider, get audited and
// notified, and the settings page has a test upload for confirming a new
// configuration against the real endpoint.
package offsite

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"quasar/internal/db"
	"quasar/internal/secrets"
)

var client = &http.Client{Timeout: 30 * time.Minute}

// Config is the destination, assembled from settings.
type Config struct {
	Endpoint  string // host or https://host, e.g. s3.eu-central-1.amazonaws.com
	Region    string
	Bucket    string
	Prefix    string // optional key prefix, e.g. "quasar/prod"
	AccessKey string
	SecretKey string
}

// Configured reports whether enough is set to attempt an upload.
func (c Config) Configured() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
}

// Load reads the destination from settings, decrypting the secret key.
func Load(database *sql.DB, k *secrets.Keyring) (Config, error) {
	secretKey, err := k.Decrypt(db.GetSetting(database, db.SettingOffsiteSecretKey))
	if err != nil {
		return Config{}, fmt.Errorf("decrypt offsite secret key: %w", err)
	}
	region := strings.TrimSpace(db.GetSetting(database, db.SettingOffsiteRegion))
	if region == "" {
		// Providers that ignore the region (R2, MinIO) still require a
		// syntactically valid one in the signature.
		region = "us-east-1"
	}
	return Config{
		Endpoint:  strings.TrimSpace(db.GetSetting(database, db.SettingOffsiteEndpoint)),
		Region:    region,
		Bucket:    strings.TrimSpace(db.GetSetting(database, db.SettingOffsiteBucket)),
		Prefix:    strings.Trim(strings.TrimSpace(db.GetSetting(database, db.SettingOffsitePrefix)), "/"),
		AccessKey: strings.TrimSpace(db.GetSetting(database, db.SettingOffsiteAccessKey)),
		SecretKey: secretKey,
	}, nil
}

// Upload copies a local file to the configured bucket under its base name.
func Upload(cfg Config, localPath string) error {
	if !cfg.Configured() {
		return fmt.Errorf("offsite storage is not configured")
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}

	// The payload hash is computed from the file rather than declared
	// UNSIGNED-PAYLOAD: a signed hash is accepted by every S3-compatible
	// provider, whereas the unsigned form is not.
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return fmt.Errorf("hash %s: %w", localPath, err)
	}
	payloadHash := hex.EncodeToString(sum.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}

	// filepath.Base, not path.Base: the local path uses the host's separator,
	// and on a Windows dev machine path.Base returns the whole path, which then
	// becomes the object key.
	key := filepath.Base(localPath)
	if cfg.Prefix != "" {
		key = cfg.Prefix + "/" + key
	}

	req, err := buildRequest(cfg, key, payloadHash, info.Size(), f, time.Now().UTC())
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// The provider's XML error body names the actual problem
		// (SignatureDoesNotMatch, NoSuchBucket, AccessDenied), which is the
		// difference between a five-minute fix and an afternoon.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// UploadProbe writes a tiny object to confirm the endpoint, credentials and
// bucket permissions all line up, without waiting for a real backup.
func UploadProbe(cfg Config) error {
	if !cfg.Configured() {
		return fmt.Errorf("offsite storage is not configured")
	}
	dir, err := os.MkdirTemp("", "quasar-offsite-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// Named like a backup so it lands beside them and is obvious to delete.
	probe := filepath.Join(dir, "quasar-offsite-test.txt")
	content := "Quasar offsite test written at " + time.Now().UTC().Format(time.RFC3339) + "\n"
	if err := os.WriteFile(probe, []byte(content), 0o600); err != nil {
		return err
	}
	return Upload(cfg, probe)
}

// buildRequest assembles a SigV4-signed PUT. Split out from Upload so the
// signature can be tested without a network.
func buildRequest(cfg Config, key, payloadHash string, size int64, body io.Reader, now time.Time) (*http.Request, error) {
	endpoint := cfg.Endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint %q: %w", cfg.Endpoint, err)
	}

	// Path-style addressing (host/bucket/key) rather than virtual-hosted
	// (bucket.host/key): every S3-compatible provider accepts it, including
	// MinIO and buckets whose names are not valid DNS labels.
	canonicalURI := "/" + cfg.Bucket + "/" + escapePath(key)
	base.Path = canonicalURI
	base.RawPath = canonicalURI

	req, err := http.NewRequest(http.MethodPut, base.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size

	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("Content-Type", "application/gzip")

	// Signed in this exact order: SigV4 requires canonical headers sorted by
	// lowercase name.
	const signedHeaders = "content-type;host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := strings.Join([]string{
		"content-type:application/gzip",
		"host:" + base.Host,
		"x-amz-content-sha256:" + payloadHash,
		"x-amz-date:" + amzDate,
	}, "\n") + "\n"

	canonicalRequest := strings.Join([]string{
		http.MethodPut,
		canonicalURI,
		"", // no query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, cfg.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex(canonicalRequest),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(signingKey(cfg.SecretKey, dateStamp, cfg.Region), stringToSign))
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cfg.AccessKey, scope, signedHeaders, signature))

	return req, nil
}

// signingKey derives the request-scoped key: the secret is chained through the
// date, region and service so a leaked signature is useless elsewhere.
func signingKey(secretKey, dateStamp, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// escapePath percent-encodes a key the way S3 canonicalisation expects: every
// segment escaped, but the separators left alone.
func escapePath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
