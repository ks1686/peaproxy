package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A disabled account is never constructed by the gateway -- rebuild skips it --
// so constructing one during validation rejects a config the gateway starts
// without complaint. Validation that is stricter than startup is worse than no
// validation: it refuses configs that work.
func TestValidateDoesNotRejectAConfigWhoseOnlyBrokenAccountIsDisabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: working
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
  - id: parked
    adapter: openai_compat
    tier: paid
    disabled: true
    apiKeyEnv: EXAMPLE_KEY
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out); err != nil {
		t.Fatalf("validate refused a config the gateway would start: %v\n%s\n"+
			"the disabled account is never constructed at startup, so refusing it here is stricter "+
			"than the thing this command is supposed to predict", err, out)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("validate did not report ok:\n%s", out)
	}
}

// The same config with the account enabled must still fail -- otherwise the fix
// above would be "ignore account problems". Expressed with the broken account
// as the only one, because a second working account means the gateway starts
// regardless, and then failing would be the bug in the other direction.
func TestValidateStillRejectsTheSameAccountOnceItIsEnabled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: parked
    adapter: openai_compat
    tier: paid
    apiKeyEnv: EXAMPLE_KEY
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)
	if err == nil {
		t.Fatalf("validate approved a config whose only account has no endpoint:\n%s", out)
	}
	if !strings.Contains(err.Error(), "parked") {
		t.Fatalf("the failure does not name the offending account: %v", err)
	}
}

// The gateway keeps starting when one account cannot be built, as long as
// another works: rebuild collects the first error and returns it only when no
// account at all could be built. Failing on the first error made validate
// refuse a config serve runs happily, which is the same false assurance this
// check exists to remove -- pointed the other way.
func TestValidateToleratesOneBrokenAccountWhenAnotherStillWorks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: working
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
  - id: broken
    adapter: openai_compat
    tier: paid
    apiKeyEnv: EXAMPLE_KEY
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out); err != nil {
		t.Fatalf("validate refused a config the gateway starts: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("validate did not report ok:\n%s", out)
	}
	// The failure is not swallowed either: it is named, because an operator
	// who cannot see which account is broken will not fix it.
	if !strings.Contains(out.String(), "broken") {
		t.Fatalf("validate passed without naming the account it could not build:\n%s", out)
	}
}

// When no account can be built the gateway does not start, so validate must not
// report ok. This is the case the check was added for.
func TestValidateStillFailsWhenNoAccountCanBeBuilt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: first
    adapter: openai_compat
    tier: paid
    apiKeyEnv: EXAMPLE_KEY
  - id: second
    adapter: openai_compat
    tier: paid
    apiKeyEnv: EXAMPLE_KEY
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out); err == nil {
		t.Fatalf("validate approved a config the gateway cannot start:\n%s", out)
	}
}
