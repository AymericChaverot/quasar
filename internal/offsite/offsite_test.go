package offsite

import (
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(endpoint string) Config {
	return Config{
		Endpoint:  endpoint,
		Region:    "eu-central-1",
		Bucket:    "my-backups",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
}

var fixedTime = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

func TestConfigured(t *testing.T) {
	full := testConfig("s3.example.com")
	if !full.Configured() {
		t.Error("a complete config should be usable")
	}
	for _, blank := range []func(*Config){
		func(c *Config) { c.Endpoint = "" },
		func(c *Config) { c.Bucket = "" },
		func(c *Config) { c.AccessKey = "" },
		func(c *Config) { c.SecretKey = "" },
	} {
		c := testConfig("s3.example.com")
		blank(&c)
		if c.Configured() {
			t.Errorf("an incomplete config was reported usable: %+v", c)
		}
	}
}

// The signing key is a chain of HMACs over date, region and service, so a
// signature captured from one request cannot be replayed against another day,
// region or service.
func TestSigningKeyIsScoped(t *testing.T) {
	base := signingKey("secret", "20260727", "eu-central-1")
	if len(base) != 32 {
		t.Fatalf("signing key is %d bytes, want 32", len(base))
	}
	for _, other := range [][]byte{
		signingKey("secret", "20260728", "eu-central-1"), // next day
		signingKey("secret", "20260727", "us-east-1"),    // other region
		signingKey("other", "20260727", "eu-central-1"),  // other secret
	} {
		if hex.EncodeToString(base) == hex.EncodeToString(other) {
			t.Error("the signing key does not vary with its scope")
		}
	}
}

// The Authorization header has to carry exactly the elements S3 parses, in the
// documented shape; a provider rejects anything else with SignatureDoesNotMatch.
func TestAuthorizationHeaderShape(t *testing.T) {
	req, err := buildRequest(testConfig("s3.example.com"), "quasar-20260727-120000.tar.gz",
		strings.Repeat("a", 64), 10, strings.NewReader("0123456789"), fixedTime)
	if err != nil {
		t.Fatal(err)
	}

	auth := req.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 ",
		"Credential=AKIAIOSFODNN7EXAMPLE/20260727/eu-central-1/s3/aws4_request",
		"SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date",
		"Signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Errorf("Authorization %q is missing %q", auth, want)
		}
	}
	if req.Header.Get("X-Amz-Date") != "20260727T120000Z" {
		t.Errorf("X-Amz-Date = %q", req.Header.Get("X-Amz-Date"))
	}
	// Every header named in SignedHeaders must actually be sent, or the server
	// recomputes a different canonical request.
	for _, h := range []string{"Content-Type", "X-Amz-Content-Sha256", "X-Amz-Date"} {
		if req.Header.Get(h) == "" {
			t.Errorf("signed header %s is not set on the request", h)
		}
	}
}

// Any change to the request must change the signature, which is what makes the
// signature a signature rather than a decoration.
func TestSignatureCoversTheRequest(t *testing.T) {
	sign := func(mutate func(*Config), key, payloadHash string, at time.Time) string {
		cfg := testConfig("s3.example.com")
		if mutate != nil {
			mutate(&cfg)
		}
		req, err := buildRequest(cfg, key, payloadHash, 3, strings.NewReader("abc"), at)
		if err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}

	base := sign(nil, "archive.tar.gz", strings.Repeat("a", 64), fixedTime)
	if base == sign(nil, "different.tar.gz", strings.Repeat("a", 64), fixedTime) {
		t.Error("the object key is not covered by the signature")
	}
	if base == sign(nil, "archive.tar.gz", strings.Repeat("b", 64), fixedTime) {
		t.Error("the payload hash is not covered by the signature")
	}
	if base == sign(nil, "archive.tar.gz", strings.Repeat("a", 64), fixedTime.Add(time.Hour)) {
		t.Error("the timestamp is not covered by the signature")
	}
	if base == sign(func(c *Config) { c.Bucket = "other" }, "archive.tar.gz", strings.Repeat("a", 64), fixedTime) {
		t.Error("the bucket is not covered by the signature")
	}
	if base == sign(func(c *Config) { c.SecretKey = "different" }, "archive.tar.gz", strings.Repeat("a", 64), fixedTime) {
		t.Error("the secret key does not affect the signature")
	}
	// Determinism: the same inputs must reproduce the same signature.
	if base != sign(nil, "archive.tar.gz", strings.Repeat("a", 64), fixedTime) {
		t.Error("signing is not deterministic")
	}
}

// Path-style addressing, so buckets whose names are not valid DNS labels and
// providers like MinIO both work.
func TestPathStyleAddressing(t *testing.T) {
	req, err := buildRequest(testConfig("s3.example.com"), "backups/archive.tar.gz",
		strings.Repeat("a", 64), 3, strings.NewReader("abc"), fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.Path; got != "/my-backups/backups/archive.tar.gz" {
		t.Errorf("path = %q, want the bucket in the path", got)
	}
	if req.URL.Host != "s3.example.com" {
		t.Errorf("host = %q, want the bare endpoint", req.URL.Host)
	}
}

// A bare host must be treated as https, never plain http: the request carries a
// credential and a copy of every secret on the platform.
func TestEndpointDefaultsToHTTPS(t *testing.T) {
	req, err := buildRequest(testConfig("s3.example.com"), "k", strings.Repeat("a", 64), 1, strings.NewReader("x"), fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Scheme != "https" {
		t.Errorf("scheme = %q, want https", req.URL.Scheme)
	}

	// An explicit scheme is respected, for a MinIO on the local network.
	req, err = buildRequest(testConfig("http://minio.local:9000"), "k", strings.Repeat("a", 64), 1, strings.NewReader("x"), fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Scheme != "http" || req.URL.Host != "minio.local:9000" {
		t.Errorf("got %s://%s, want http://minio.local:9000", req.URL.Scheme, req.URL.Host)
	}
}

func TestEscapePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"quasar-20260727.tar.gz", "quasar-20260727.tar.gz"},
		{"prefix/archive.tar.gz", "prefix/archive.tar.gz"}, // separators preserved
		{"with space.tar.gz", "with%20space.tar.gz"},
	}
	for _, tc := range tests {
		if got := escapePath(tc.in); got != tc.want {
			t.Errorf("escapePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// End to end against a stub that stands in for the provider: the file arrives
// intact, under the right key, with the hash the signature promised.
func TestUploadSendsTheFile(t *testing.T) {
	const content = "not really a tarball, but it is bytes\n"

	var gotPath, gotAuth, gotHash, gotBody string
	var gotLength int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.Path, string(body)
		gotAuth = r.Header.Get("Authorization")
		gotHash = r.Header.Get("X-Amz-Content-Sha256")
		gotLength = r.ContentLength
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	local := filepath.Join(dir, "quasar-20260727-120000.tar.gz")
	if err := os.WriteFile(local, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := testConfig(srv.URL)
	cfg.Prefix = "prod"
	if err := Upload(cfg, local); err != nil {
		t.Fatal(err)
	}

	if gotPath != "/my-backups/prod/quasar-20260727-120000.tar.gz" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody != content {
		t.Errorf("body = %q, want the file's contents", gotBody)
	}
	if gotLength != int64(len(content)) {
		t.Errorf("Content-Length = %d, want %d", gotLength, len(content))
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q", gotAuth)
	}
	// The declared hash has to match the bytes actually sent, or the provider
	// rejects the upload.
	if want := sha256Hex(content); gotHash != want {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %q", gotHash, want)
	}
}

// The provider's error body names the real problem — SignatureDoesNotMatch,
// NoSuchBucket, AccessDenied — and losing it turns a five-minute fix into an
// afternoon.
func TestUploadSurfacesTheProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`<Error><Code>SignatureDoesNotMatch</Code></Error>`))
	}))
	defer srv.Close()

	local := filepath.Join(t.TempDir(), "quasar-x.tar.gz")
	os.WriteFile(local, []byte("x"), 0o600)

	err := Upload(testConfig(srv.URL), local)
	if err == nil {
		t.Fatal("expected an error on a 403")
	}
	if !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Errorf("error %q should carry the provider's message", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error %q should carry the status", err)
	}
}

func TestUploadRefusesWhenUnconfigured(t *testing.T) {
	local := filepath.Join(t.TempDir(), "quasar-x.tar.gz")
	os.WriteFile(local, []byte("x"), 0o600)
	if err := Upload(Config{}, local); err == nil {
		t.Error("expected an error when nothing is configured")
	}
}

func TestUploadProbeWritesAnObject(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := UploadProbe(testConfig(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "quasar-offsite-test.txt") {
		t.Errorf("probe went to %q", gotPath)
	}
}
