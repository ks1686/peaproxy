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
	// Extra holds provider-specific fields (project_id, device_id, token_endpoint).
	// Values may be secrets (dca_token); never log.
	Extra map[string]string
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

// ExtraGet returns a metadata value.
func (t Token) ExtraGet(key string) string {
	if t.Extra == nil {
		return ""
	}
	return t.Extra[key]
}

// WithExtra copies t and sets one Extra key.
func (t Token) WithExtra(key, value string) Token {
	next := make(map[string]string, len(t.Extra)+1)
	for k, v := range t.Extra {
		next[k] = v
	}
	if value == "" {
		delete(next, key)
	} else {
		next[key] = value
	}
	t.Extra = next
	return t
}

// KeepExtra copies Extra (and empty-safe identity fields) from prev after refresh.
func (t Token) KeepExtra(prev Token) Token {
	if t.Email == "" {
		t.Email = prev.Email
	}
	if t.AccountID == "" {
		t.AccountID = prev.AccountID
	}
	if t.PlanType == "" {
		t.PlanType = prev.PlanType
	}
	if len(prev.Extra) == 0 {
		return t
	}
	next := make(map[string]string, len(prev.Extra)+len(t.Extra))
	for k, v := range prev.Extra {
		next[k] = v
	}
	for k, v := range t.Extra {
		next[k] = v
	}
	t.Extra = next
	return t
}
