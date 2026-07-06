package appauth

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// refreshSkew is how long before a token's real expiry we consider it stale and
// refresh — GitHub installation tokens live ~1h, so this is generous headroom.
const refreshSkew = 5 * time.Minute

// Client mints and caches GitHub App installation access tokens.
type Client struct {
	AppID   int64
	BaseURL string
	HTTP    *http.Client
	Now     func() time.Time

	key *rsa.PrivateKey

	mu       sync.Mutex
	cache    map[int64]cachedToken
	inflight map[int64]*tokenCall
}

type cachedToken struct {
	token   string
	renewAt time.Time // real expiry minus refreshSkew
	// errRevalid is set when the cached entry is a negative cache: the token is
	// empty, renewAt is the deadline until which subsequent callers fall back
	// fast instead of stampeding the token endpoint after a failure. See
	// TestInstallationTokenErrorNegativeCache.
	errRevalid time.Time
}

// tokenCall is one in-flight fetch other callers for the same installation wait
// on, so a burst of webhooks never stampedes the token endpoint.
type tokenCall struct {
	wg    sync.WaitGroup
	token string
	err   error
}

// negativeCacheFor is how long a failed fetch collapses concurrent retries —
// the spec's named anti-pattern is "all retry at once" after a fetch error. A
// short hold means a transient 5xx is retried after 5s rather than by N callers
// in the same millisecond.
const negativeCacheFor = 5 * time.Second

// New returns a Client that signs App JWTs with key and mints tokens against
// api.github.com. BaseURL/HTTP/Now default to production values when zero.
func New(appID int64, key *rsa.PrivateKey) *Client {
	return &Client{
		AppID:    appID,
		BaseURL:  "https://api.github.com",
		HTTP:     &http.Client{Timeout: 30 * time.Second},
		Now:      time.Now,
		key:      key,
		cache:    map[int64]cachedToken{},
		inflight: map[int64]*tokenCall{},
	}
}

// InstallationToken returns a valid access token for installationID, minting a
// fresh one only when the cache is empty or within refreshSkew of expiry.
// Concurrent callers for the same installation share a single fetch.
func (c *Client) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	c.mu.Lock()
	if ct, ok := c.cache[installationID]; ok {
		now := c.Now()
		if ct.errRevalid.IsZero() {
			// positive cache: valid until renewAt
			if now.Before(ct.renewAt) {
				c.mu.Unlock()
				return ct.token, nil
			}
		} else {
			// negative cache: fail fast until errRevalid, then a single caller
			// re-attempts (the rest collapse onto the new in-flight call)
			if now.Before(ct.errRevalid) {
				c.mu.Unlock()
				return "", fmt.Errorf("installation %d: token fetch temporarily unavailable (negative cache)", installationID)
			}
		}
	}
	if call, ok := c.inflight[installationID]; ok {
		c.mu.Unlock()
		call.wg.Wait()
		return call.token, call.err
	}
	call := &tokenCall{}
	call.wg.Add(1)
	c.inflight[installationID] = call
	c.mu.Unlock()

	token, renewAt, err := c.fetch(ctx, installationID)

	c.mu.Lock()
	delete(c.inflight, installationID)
	if err == nil {
		c.cache[installationID] = cachedToken{token: token, renewAt: renewAt}
	} else {
		// Negative cache so a burst of waiters that all fail at once do NOT each
		// re-attempt the fetch on their own next call (the named stampede).
		c.cache[installationID] = cachedToken{errRevalid: c.Now().Add(negativeCacheFor)}
	}
	c.mu.Unlock()

	call.token, call.err = token, err
	call.wg.Done()
	return token, err
}

// fetch exchanges a fresh App JWT for an installation token.
func (c *Client) fetch(ctx context.Context, installationID int64) (string, time.Time, error) {
	jwt, err := AppJWT(c.AppID, c.key, c.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", c.BaseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("installation token request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusCreated {
		return "", time.Time{}, fmt.Errorf("installation %d token: unexpected status %d", installationID, resp.StatusCode)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", time.Time{}, fmt.Errorf("decode installation token: %w", err)
	}
	if out.Token == "" {
		return "", time.Time{}, fmt.Errorf("installation %d: empty token in response", installationID)
	}
	// Treat a missing/garbage expires_at as a hard failure rather than a
	// cache-miss-forever: a zero ExpiresAt yields renewAt = 0001-01-04, which
	// every future Now().Before(renewAt) check fails, so the cache would never
	// engage and every call would re-fetch — exactly the stampede single-flight
	// exists to prevent. GitHub's contract requires expires_at; treat its
	// absence as a protocol violation that must surface an error.
	if out.ExpiresAt.IsZero() || !out.ExpiresAt.After(c.Now()) {
		return "", time.Time{}, fmt.Errorf("installation %d: token response missing expires_at or already expired", installationID)
	}
	return out.Token, out.ExpiresAt.Add(-refreshSkew), nil
}

// TokenSource binds a Client to one installation, satisfying gh.TokenSource so
// the review pipeline receives App auth exactly as it receives a static token.
type TokenSource struct {
	client         *Client
	installationID int64
}

// TokenSource returns a gh.TokenSource for one installation.
func (c *Client) TokenSource(installationID int64) TokenSource {
	return TokenSource{client: c, installationID: installationID}
}

// Token resolves (and caches) the installation access token at request time.
// A zero-value TokenSource (built without Client.TokenSource) returns a
// graceful error rather than nil-dereferencing the unexported client.
func (t TokenSource) Token(ctx context.Context) (string, error) {
	if t.client == nil {
		return "", fmt.Errorf("appauth: TokenSource not initialized; construct via Client.TokenSource")
	}
	return t.client.InstallationToken(ctx, t.installationID)
}

// LoadPrivateKey parses a PEM-encoded RSA private key (PKCS#1 — GitHub's
// default — or PKCS#8).
func LoadPrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in app private key")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse app private key (tried PKCS#1 and PKCS#8): %w", err)
	}
	k, ok := keyAny.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("app private key is %T, want RSA", keyAny)
	}
	return k, nil
}
