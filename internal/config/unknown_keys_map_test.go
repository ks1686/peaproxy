package config

import (
	"strings"
	"testing"
)

// A map has two kinds of key, and only one of them is a field name. Getting
// this wrong in either direction is a defect: reporting a model name as a
// misspelled setting trains people to ignore the warning, and missing a typo
// inside a price quote leaves a rate silently unset.
func TestStructValuedMapsAreCheckedButScalarValuedOnesAreNot(t *testing.T) {
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
routes:
  my-fast-model: acct
  some.other/name: acct
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
catalog:
  rename:
    gpt-5: GPT five
automaticRoutes:
  enabled: true
  prices:
    acct/gpt-5:
      input: 100.0
      output: 400.0
      cacheRead: 0.01
      cacheWrit: 1.25
`)
	unknown, err := UnknownKeys(src)
	if err != nil {
		t.Fatal(err)
	}

	var found []string
	for _, u := range unknown {
		found = append(found, u.Key)
	}
	joined := strings.Join(found, ",")

	// The typo inside a price quote is the one that matters: cacheWrite stays
	// unset, and every cache write is then billed as an ordinary input token.
	if !strings.Contains(joined, "cacheWrit") {
		t.Fatalf("a typo inside automaticRoutes.prices was not reported; found %v\n"+
			"a rate that silently stays unset prices cache writes wrongly", found)
	}
	// The path is what the warning prints, so it has to lead the reader to the
	// setting rather than just naming a key they have to go looking for.
	var paths []string
	for _, u := range unknown {
		paths = append(paths, u.Path+"."+u.Key)
	}
	if p := strings.Join(paths, ","); !strings.Contains(p, "automaticRoutes.prices") {
		t.Fatalf("the report does not say which setting carries the typo: %v", paths)
	}

	// Model names in scalar maps are not field names and must never be named.
	for _, key := range unknown {
		if key.Key == "my-fast-model" || key.Key == "some.other/name" || key.Key == "gpt-5" {
			t.Fatalf("a map key was reported as a misspelled setting: %+v\n"+
				"routes and catalog.rename map model names to model names; those are values, not fields", key)
		}
	}
	// And neither is the deployment name used as a price key.
	for _, key := range unknown {
		if strings.Contains(key.Key, "/") {
			t.Fatalf("a deployment name was reported as a misspelled setting: %+v", key)
		}
	}
}

// A price quote with every field spelled correctly must be silent, or the
// warning is noise.
func TestACorrectPriceQuoteIsSilent(t *testing.T) {
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
automaticRoutes:
  enabled: true
  prices:
    acct/gpt-5:
      input: 100.0
      output: 400.0
      cacheRead: 0.01
      cacheWrite: 1.25
      verified: true
`)
	unknown, err := UnknownKeys(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("a fully-spelled price quote was reported as carrying %d unknown keys: %+v", len(unknown), unknown)
	}
}
