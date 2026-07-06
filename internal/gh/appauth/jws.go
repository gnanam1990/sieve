// Package appauth mints GitHub App installation tokens: it signs a short-lived
// RS256 JWT with the app's private key, exchanges it for a per-installation
// access token, and caches that token (with single-flight refresh) until
// shortly before it expires. AppTokenSource adapts it to gh.TokenSource so the
// review pipeline consumes App auth exactly as it consumes a static token.
package appauth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// jwtHeader is the fixed RS256 JWT header, encoded once. GitHub App JWTs never
// vary the header, so pinning the exact bytes keeps the first segment stable.
var jwtHeader = []byte(`{"alg":"RS256","typ":"JWT"}`)

// appClaims are the registered claims GitHub requires on an App JWT: issued 60s
// in the past (clock-skew slack), expiring within GitHub's 10-minute ceiling,
// issued by the app id.
type appClaims struct {
	Iat int64 `json:"iat"`
	Exp int64 `json:"exp"`
	Iss int64 `json:"iss"`
}

// AppJWT builds and signs an App JWT for appID valid around now. iat is
// backdated 60s and exp is now+9m, comfortably inside GitHub's 10m limit.
func AppJWT(appID int64, key *rsa.PrivateKey, now time.Time) (string, error) {
	if key == nil {
		return "", fmt.Errorf("nil app private key")
	}
	claims := appClaims{
		Iat: now.Add(-60 * time.Second).Unix(),
		Exp: now.Add(9 * time.Minute).Unix(),
		Iss: appID,
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal app claims: %w", err)
	}
	return signRS256(jwtHeader, claimsJSON, key)
}

// signRS256 produces base64url(header).base64url(claims).base64url(sig), the
// compact JWS serialization, with an RSASSA-PKCS1-v1_5 SHA-256 signature.
// PKCS#1 v1.5 signatures are deterministic, so the output is reproducible for a
// given key and payload.
func signRS256(headerJSON, claimsJSON []byte, key *rsa.PrivateKey) (string, error) {
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign JWT: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}
