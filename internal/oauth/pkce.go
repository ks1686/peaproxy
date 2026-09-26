package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCE is an RFC 7636 S256 pair.
type PKCE struct {
	Verifier  string
	Challenge string
}

// GeneratePKCE returns a new verifier/challenge pair.
func GeneratePKCE() (PKCE, error) {
	raw := make([]byte, 96)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, fmt.Errorf("pkce: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	return PKCE{Verifier: verifier, Challenge: ChallengeS256(verifier)}, nil
}

// ChallengeS256 is the base64url SHA-256 of verifier (no padding).
func ChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomState returns a CSRF state parameter.
func RandomState() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
