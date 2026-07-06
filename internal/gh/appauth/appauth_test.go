package appauth

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/sieve/internal/gh"
)

// testKey generates a throwaway RSA key at test time — no key material is ever
// committed to the repo (GitGuardian-safe).
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// TestAppJWTSegments pins the exact header and claims segment encodings and
// verifies the signature against the public key.
func TestAppJWTSegments(t *testing.T) {
	key := testKey(t)
	now := time.Unix(1_700_000_000, 0)
	tok, err := AppJWT(123456, key, now)
	if err != nil {
		t.Fatal(err)
	}
	segs := strings.Split(tok, ".")
	if len(segs) != 3 {
		t.Fatalf("want 3 JWT segments, got %d", len(segs))
	}
	wantHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	wantClaims := base64.RawURLEncoding.EncodeToString([]byte(`{"iat":1699999940,"exp":1700000540,"iss":123456}`))
	if segs[0] != wantHeader {
		t.Errorf("header segment:\n got %q\nwant %q", segs[0], wantHeader)
	}
	if segs[1] != wantClaims {
		t.Errorf("claims segment (iat=now-60, exp=now+540, iss=appid):\n got %q\nwant %q", segs[1], wantClaims)
	}
	// Verify the signature over header.claims with the public key.
	digest := sha256.Sum256([]byte(segs[0] + "." + segs[1]))
	sig, err := base64.RawURLEncoding.DecodeString(segs[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
}

func TestAppJWTNilKey(t *testing.T) {
	if _, err := AppJWT(1, nil, time.Now()); err == nil {
		t.Fatal("nil key must error")
	}
}

// tokenServer serves installation tokens, counting requests and returning
// expiresAt relative to a fixed base.
func tokenServer(t *testing.T, count *int32, expiresAt time.Time, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(count, 1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing bearer JWT: %q", r.Header.Get("Authorization"))
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"token":"ghs_installtoken","expires_at":%q}`, expiresAt.Format(time.RFC3339))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, srv *httptest.Server, now time.Time) *Client {
	c := New(654321, testKey(t))
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	c.Now = func() time.Time { return now }
	return c
}

// TestInstallationTokenCaches: a second call within the token's lifetime hits
// the cache, not the network.
func TestInstallationTokenCaches(t *testing.T) {
	var count int32
	now := time.Unix(1_700_000_000, 0)
	srv := tokenServer(t, &count, now.Add(time.Hour), 0)
	c := newTestClient(t, srv, now)

	tok, err := c.InstallationToken(context.Background(), 42)
	if err != nil || tok != "ghs_installtoken" {
		t.Fatalf("first token: %q err=%v", tok, err)
	}
	if _, err := c.InstallationToken(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("second call must be cached: want 1 request, got %d", got)
	}
	// A different installation is a separate cache entry → a second fetch.
	if _, err := c.InstallationToken(context.Background(), 99); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&count); got != 2 {
		t.Errorf("distinct installation must fetch: want 2, got %d", got)
	}
}

// TestInstallationTokenRefreshesNearExpiry: once inside refreshSkew of expiry
// the cache entry is stale and a new token is fetched.
func TestInstallationTokenRefreshesNearExpiry(t *testing.T) {
	var count int32
	base := time.Unix(1_700_000_000, 0)
	// Token expires 3 minutes out — already inside the 5-minute refresh skew.
	srv := tokenServer(t, &count, base.Add(3*time.Minute), 0)
	c := newTestClient(t, srv, base)

	if _, err := c.InstallationToken(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InstallationToken(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&count); got != 2 {
		t.Errorf("token inside refresh skew must be re-fetched: want 2, got %d", got)
	}
}

// TestSingleFlight: a burst of concurrent callers for one installation triggers
// exactly one network fetch. -race is the witness for the shared-state safety.
func TestSingleFlight(t *testing.T) {
	var count int32
	now := time.Unix(1_700_000_000, 0)
	srv := tokenServer(t, &count, now.Add(time.Hour), 100*time.Millisecond)
	c := newTestClient(t, srv, now)

	const n = 24
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, n)
	toks := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			toks[i], errs[i] = c.InstallationToken(context.Background(), 5)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&count); got != 1 {
		t.Errorf("single-flight failed: want 1 fetch for %d concurrent callers, got %d", n, got)
	}
	for i := 0; i < n; i++ {
		if errs[i] != nil || toks[i] != "ghs_installtoken" {
			t.Fatalf("caller %d got %q err=%v", i, toks[i], errs[i])
		}
	}
}

func TestFetchNon201(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"forbidden"}`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, time.Unix(1_700_000_000, 0))
	if _, err := c.InstallationToken(context.Background(), 1); err == nil {
		t.Fatal("non-201 must error")
	}
}

func TestFetchEmptyToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"token":"","expires_at":"2026-01-01T00:00:00Z"}`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, time.Unix(1_700_000_000, 0))
	if _, err := c.InstallationToken(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("empty token must error, got %v", err)
	}
}

func TestFetchMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{not json`)
	}))
	t.Cleanup(srv.Close)
	c := newTestClient(t, srv, time.Unix(1_700_000_000, 0))
	if _, err := c.InstallationToken(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Fatalf("malformed JSON must error, got %v", err)
	}
}

func TestLoadPrivateKeyNonRSA(t *testing.T) {
	// A structurally valid PKCS#8 block that is not RSA (Ed25519) must be rejected.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	p8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})
	if _, err := LoadPrivateKey(block); err == nil || !strings.Contains(err.Error(), "want RSA") {
		t.Fatalf("non-RSA key must be rejected, got %v", err)
	}
}

func TestLoadPrivateKey(t *testing.T) {
	key := testKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if k, err := LoadPrivateKey(pkcs1); err != nil || k.N.Cmp(key.N) != 0 {
		t.Fatalf("PKCS#1 load failed: err=%v", err)
	}
	p8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})
	if k, err := LoadPrivateKey(pkcs8); err != nil || k.N.Cmp(key.N) != 0 {
		t.Fatalf("PKCS#8 load failed: err=%v", err)
	}
	if _, err := LoadPrivateKey([]byte("not a pem")); err == nil {
		t.Fatal("garbage must error")
	}
	// A valid PEM block that isn't an RSA key must be rejected.
	junk := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("junk")})
	if _, err := LoadPrivateKey(junk); err == nil {
		t.Fatal("non-key PEM must error")
	}
}

// TestTokenSourceSatisfiesGH is a compile-time + behavioral check that the App
// token source drops into the pipeline wherever gh.TokenSource is expected.
func TestTokenSourceSatisfiesGH(t *testing.T) {
	var count int32
	now := time.Unix(1_700_000_000, 0)
	srv := tokenServer(t, &count, now.Add(time.Hour), 0)
	c := newTestClient(t, srv, now)

	var ts gh.TokenSource = c.TokenSource(11)
	tok, err := ts.Token(context.Background())
	if err != nil || tok != "ghs_installtoken" {
		t.Fatalf("TokenSource.Token: %q err=%v", tok, err)
	}
}
