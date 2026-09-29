package secretstore

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestChunkHeaderIsReadableByOldReleases(t *testing.T) {
	// Given: the JSON shape of config.OAuthToken (which has only YAML tags).
	type oauthToken struct {
		AccessToken  string
		RefreshToken string
		ExpiresAt    string
		IDToken      string
		AccountID    string
		Email        string
		PlanType     string
		Extra        map[string]string
	}
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	v := `{"AccessToken":"` + strings.Repeat("x", chunkSize+1) + `","RefreshToken":"refresh"}`

	// When: a token too large for one keychain item is saved.
	if err := s.Set("acct", KindOAuth, v); err != nil {
		t.Fatal(err)
	}

	// Then: an old release decodes an empty token, while this release reads it all.
	var tok oauthToken
	if err := json.Unmarshal([]byte(kr.m[Service+"\x00acct/oauth"]), &tok); err != nil {
		t.Errorf("old release cannot decode the base item: %v", err)
	}
	if !reflect.DeepEqual(tok, oauthToken{}) {
		t.Error("chunk header populated OAuth token fields")
	}
	if got, err := s.Get("acct", KindOAuth); err != nil || got != v {
		t.Fatalf("round trip len=%d err=%v", len(got), err)
	}
}

func TestReadsCurrentGenerationHeaderForm(t *testing.T) {
	// Given: generation-scoped items written by the previous chunk writer.
	kr := newFault()
	s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
	kr.m[Service+"\x00acct/oauth"] = "peaproxy-chunks:deadbeef:2"
	kr.m[Service+"\x00acct/oauth#deadbeef.0"] = "ab"
	kr.m[Service+"\x00acct/oauth#deadbeef.1"] = "cd"

	// When
	got, err := s.Get("acct", KindOAuth)

	// Then
	if err != nil || got != "abcd" {
		t.Fatalf("current generation read %q %v", got, err)
	}
}

func TestRawHeaderFormsRoundTripThroughChunks(t *testing.T) {
	for _, v := range []string{
		"peaproxy-chunks:2",
		"peaproxy-chunks:deadbeef:2",
		`{"peaproxyChunks":"deadbeef:2"}`,
		`{"peaproxyChunks":`,
	} {
		t.Run(v, func(t *testing.T) {
			// Given: a small secret whose prefix is reserved for chunk metadata.
			kr := newFault()
			s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}

			// When
			if err := s.Set("acct", KindAPIKey, v); err != nil {
				t.Fatal(err)
			}

			// Then: it is escaped through a chunk, not interpreted as metadata.
			if n := chunkItems(kr, "acct/apikey"); n != 1 {
				t.Errorf("want one chunk for a header-like secret, got %d", n)
			}
			if got, err := s.Get("acct", KindAPIKey); err != nil || got != v {
				t.Fatalf("got %q err=%v", got, err)
			}
		})
	}
}

func TestMalformedJSONChunkHeaderIsUnreadable(t *testing.T) {
	for _, v := range []string{
		`{"peaproxyChunks":`,
		`{"peaproxyChunks":2}`,
		`{"peaproxyChunks":"deadbeef:0"}`,
		`{"peaproxyChunks":"deadbeef:x"}`,
		`{"peaproxyChunks":":2"}`,
	} {
		t.Run(v, func(t *testing.T) {
			// Given: corrupt metadata stored directly as a base item.
			kr := newFault()
			s := &Store{backend: BackendKeyring, dir: t.TempDir(), kr: kr}
			kr.m[Service+"\x00acct/oauth"] = v

			// When
			_, err := s.Get("acct", KindOAuth)

			// Then
			if !errors.Is(err, ErrUnreadable) {
				t.Fatalf("want ErrUnreadable, got %v", err)
			}
		})
	}
}
