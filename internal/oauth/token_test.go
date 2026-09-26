package oauth

import (
	"testing"
	"time"
)

func TestTokenNeedsRefreshBeforeExpiry(t *testing.T) {
	tok := Token{AccessToken: "a", ExpiresAt: time.Now().Add(2 * time.Minute)}
	if !tok.NeedsRefresh(5 * time.Minute) {
		t.Fatal("should refresh when expiry is inside skew")
	}
	tok.ExpiresAt = time.Now().Add(time.Hour)
	if tok.NeedsRefresh(5 * time.Minute) {
		t.Fatal("should not refresh a fresh token")
	}
}

func TestTokenNeedsRefreshWhenAccessEmpty(t *testing.T) {
	tok := Token{RefreshToken: "r"}
	if !tok.NeedsRefresh(time.Minute) {
		t.Fatal("empty access token needs refresh")
	}
}

func TestParseTokenResponse(t *testing.T) {
	raw := []byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"id_token":"hdr.eyJlbWFpbCI6ImEifQ.sig"}`)
	tok, err := ParseTokenResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at" || tok.RefreshToken != "rt" {
		t.Fatalf("%#v", tok)
	}
	if tok.ExpiresAt.Before(time.Now().Add(50 * time.Minute)) {
		t.Fatalf("expires %s", tok.ExpiresAt)
	}
}

func TestParseTokenResponseKeepsRefreshWhenOmitted(t *testing.T) {
	raw := []byte(`{"access_token":"new","expires_in":60}`)
	tok, err := ParseTokenResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	tok = tok.WithFallbackRefresh("old-rt")
	if tok.RefreshToken != "old-rt" {
		t.Fatalf("%#v", tok)
	}
}
