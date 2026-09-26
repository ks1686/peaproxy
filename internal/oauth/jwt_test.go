package oauth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestParseJWTClaimsExtractsCodexAccount(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"email": "user@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct_123",
			"chatgpt_plan_type":  "plus",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := "hdr." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	claims, err := ParseJWTClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Email != "user@example.com" {
		t.Fatalf("email %q", claims.Email)
	}
	if claims.AccountID != "acct_123" {
		t.Fatalf("account %q", claims.AccountID)
	}
	if claims.PlanType != "plus" {
		t.Fatalf("plan %q", claims.PlanType)
	}
}

func TestParseJWTClaimsRejectsMalformed(t *testing.T) {
	if _, err := ParseJWTClaims("not-a-jwt"); err == nil {
		t.Fatal("expected error")
	}
}
