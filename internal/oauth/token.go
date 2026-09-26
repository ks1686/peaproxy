package oauth

import (
	"encoding/json"
	"time"
)

// Token is a subscription OAuth credential set. Never log these fields.
type Token struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ExpiresAt    time.Time
	AccountID    string
	Email        string
	PlanType     string
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// ParseTokenResponse reads a standard OAuth token JSON body.
func ParseTokenResponse(raw []byte) (Token, error) {
	var resp tokenResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Token{}, err
	}
	tok := Token{
		AccessToken:  resp.AccessToken,
		RefreshToken: resp.RefreshToken,
		IDToken:      resp.IDToken,
	}
	if resp.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	if tok.IDToken != "" {
		if claims, err := ParseJWTClaims(tok.IDToken); err == nil {
			if tok.Email == "" {
				tok.Email = claims.Email
			}
			if tok.AccountID == "" {
				tok.AccountID = claims.AccountID
			}
			if tok.PlanType == "" {
				tok.PlanType = claims.PlanType
			}
		}
	}
	return tok, nil
}

// WithFallbackRefresh keeps the previous refresh token when the server omits a new one.
func (t Token) WithFallbackRefresh(prev string) Token {
	if t.RefreshToken == "" {
		t.RefreshToken = prev
	}
	return t
}

// NeedsRefresh reports whether an access token is missing or near expiry.
func (t Token) NeedsRefresh(skew time.Duration) bool {
	if t.AccessToken == "" {
		return t.RefreshToken != ""
	}
	if t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(skew).After(t.ExpiresAt)
}

// Valid reports whether an access token is present.
func (t Token) Valid() bool {
	return t.AccessToken != ""
}
