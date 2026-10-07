package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PeaProxy reports config keys it cannot read, and by default warns: a config
// carrying one is a working config today, and refusing would break a running
// deployment over a spelling. That leaves a gap -- nothing can ask "does this
// config still have a typo in it" in a way a CI job can use.
//
// --strict-config closes it for anyone who has cleaned their config, without
// taking the choice away from anyone else.

const typoConfig = `schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    capabilities:
      tooIs: false
`

const cleanConfig = `schemaVersion: 1
bind: 127.0.0.1
port: 8317
requestEngine:
  maxAttempts: 3
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
`

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peaproxy.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStrictConfigRefusesAConfigCarryingAKeyItCannotRead(t *testing.T) {
	path := writeCfg(t, typoConfig)
	out := &bytes.Buffer{}

	err := ExecuteWithArgs([]string{"config", "validate", "--config", path, "--strict-config"}, out)
	if err == nil {
		t.Fatalf("strict mode accepted a config with an unreadable key:\n%s", out)
	}
	if !strings.Contains(err.Error(), "strict") {
		t.Fatalf("the refusal does not say it is strict mode: %v", err)
	}
	// The key is named. A refusal that does not say what is wrong is a dead
	// end, and this is the command meant to prevent exactly that.
	if !strings.Contains(out.String(), "capabilities.tooIs") {
		t.Fatalf("the offending key was not named:\n%s", out)
	}
}

func TestStrictConfigAcceptsAConfigWithNoUnreadableKeys(t *testing.T) {
	path := writeCfg(t, cleanConfig)
	out := &bytes.Buffer{}

	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path, "--strict-config"}, out); err != nil {
		t.Fatalf("strict mode refused a clean config: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("strict mode passed but printed no verdict:\n%s", out)
	}
}

// The default must not change. This is the whole reason strict mode is a flag:
// a config that has been serving for months, carrying a typo nobody noticed,
// must keep booting.
func TestTheDefaultStillOnlyWarnsAboutAnUnreadableKey(t *testing.T) {
	path := writeCfg(t, typoConfig)
	out := &bytes.Buffer{}

	_ = ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)
	if !strings.Contains(out.String(), "warning:") {
		t.Fatalf("the default no longer warns about an unreadable key:\n%s", out)
	}
}

// A key with no close match is named without a guess. Following a suggestion
// the author did not mean leaves the config just as broken, and now with the
// spelling corrected, which is worse than a typo because it looks right.
func TestAnUnreadableKeyIsNamedWithoutAGuessWhenTwoFieldsAreEquallyClose(t *testing.T) {
	path := writeCfg(t, `schemaVersion: 1
bind: 127.0.0.1
port: 8317
requestEngine:
  maxRounds: 3
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
`)
	out := &bytes.Buffer{}
	_ = ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)

	if !strings.Contains(out.String(), "maxRounds") {
		t.Fatalf("the key was not named:\n%s", out)
	}
	if strings.Contains(out.String(), "did you mean") {
		t.Fatalf("a guess was offered for a key that matches two fields equally:\n%s", out)
	}
}

// The docs promise the unreadable key is named before the failure it caused.
// That was false for the typos whose symptom is a validation failure:
// config.Load runs cfg.Validate() on the way out, so `adaptor` produced
// "provider missing adapter" and returned before the unknown-key check ran at
// all. The author was told what broke and never what to change.
//
// The two never share a buffer -- the warning goes to stderr, the symptom comes
// back as the error -- so what this pins is the half that was actually broken:
// the warning was not emitted at all.
func TestTheUnreadableKeyIsNamedEvenWhenItAlsoBreaksValidation(t *testing.T) {
	path := writeCfg(t, `schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adaptor: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
`)
	out := &bytes.Buffer{}

	err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)
	if err == nil {
		t.Fatal("a provider with no adapter was accepted")
	}
	if !strings.Contains(err.Error(), "missing adapter") {
		t.Fatalf("expected the symptom as the error: %v", err)
	}
	if !strings.Contains(out.String(), "adaptor") {
		t.Fatalf("the typo was never named, so the author only learns what broke and not "+
			"what to change:\n%s\nerror: %v", out, err)
	}
}

// Strict mode's message reaches `config validate`, which starts nothing.
// "refusing to start" is a false statement in CI logs.
func TestStrictModeDoesNotClaimToBeStartingSomething(t *testing.T) {
	path := writeCfg(t, typoConfig)
	out := &bytes.Buffer{}

	err := ExecuteWithArgs([]string{"config", "validate", "--config", path, "--strict-config"}, out)
	if err == nil {
		t.Fatal("strict mode accepted a config with an unreadable key")
	}
	if strings.Contains(err.Error(), "refusing to start") {
		t.Fatalf("config validate does not start anything, so the message is false: %v", err)
	}
	if !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("the refusal does not say strict mode is why: %v", err)
	}
}

// serve runs the same check, and the ordering matters there for the same
// reason: config.Load validates on the way out, so a typo whose symptom is a
// validation failure would report only the symptom.
//
// Strict mode is what makes this observable without starting a proxy: serve
// refuses before it binds, so the test can assert the refusal without a
// listener ever appearing.
func TestServeRefusesATypoUnderStrictModeWithoutBinding(t *testing.T) {
	path := writeCfg(t, `schemaVersion: 1
bind: 127.0.0.1
port: 8399
providers:
  - id: acct
    adaptor: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
`)
	out := &bytes.Buffer{}

	err := ExecuteWithArgs([]string{"serve", "--config", path, "--strict-config"}, out)
	if err == nil {
		t.Fatalf("serve started with an unreadable key under strict mode:\n%s", out)
	}
	if !strings.Contains(err.Error(), "strict mode") {
		t.Fatalf("the refusal does not say strict mode is why: %v", err)
	}
	if !strings.Contains(out.String(), "adaptor") {
		t.Fatalf("serve named the typo nowhere, so the author only sees the symptom:\n%s", err)
	}
}
