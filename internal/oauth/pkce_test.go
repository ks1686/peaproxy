package oauth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGeneratePKCEUsesS256AndURLSafeVerifier(t *testing.T) {
	pkce, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(pkce.Verifier) < 43 || len(pkce.Verifier) > 128 {
		t.Fatalf("verifier length %d", len(pkce.Verifier))
	}
	if strings.ContainsAny(pkce.Verifier, "+/=") {
		t.Fatalf("verifier should be base64url without padding: %q", pkce.Verifier)
	}
	if pkce.Challenge != ChallengeS256(pkce.Verifier) {
		t.Fatalf("challenge mismatch")
	}
	if _, err := base64.RawURLEncoding.DecodeString(pkce.Challenge); err != nil {
		t.Fatalf("challenge decode: %v", err)
	}
}

func TestChallengeS256IsDeterministic(t *testing.T) {
	got := ChallengeS256("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	// RFC 7636 appendix B
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
