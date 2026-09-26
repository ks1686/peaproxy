package oauth

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// JWTClaims is a subset of OpenAI/Codex ID-token claims. Signature is not verified;
// the token endpoint is the source of trust.
type JWTClaims struct {
	Email     string
	AccountID string
	PlanType  string
}

type jwtPayload struct {
	Email      string `json:"email"`
	OpenAIAuth struct {
		ChatgptAccountID string `json:"chatgpt_account_id"`
		ChatgptPlanType  string `json:"chatgpt_plan_type"`
	} `json:"https://api.openai.com/auth"`
}

// ParseJWTClaims decodes the payload of a compact JWT without verifying the signature.
func ParseJWTClaims(token string) (JWTClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return JWTClaims{}, fmt.Errorf("jwt: expected 3 parts, got %d", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(padRawURL(parts[1]))
		if err != nil {
			return JWTClaims{}, fmt.Errorf("jwt payload: %w", err)
		}
	}
	var p jwtPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return JWTClaims{}, err
	}
	return JWTClaims{
		Email:     p.Email,
		AccountID: p.OpenAIAuth.ChatgptAccountID,
		PlanType:  p.OpenAIAuth.ChatgptPlanType,
	}, nil
}

func padRawURL(s string) string {
	switch len(s) % 4 {
	case 2:
		return s + "=="
	case 3:
		return s + "="
	default:
		return s
	}
}
