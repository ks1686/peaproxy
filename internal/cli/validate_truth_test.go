package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `config validate` is the command a person runs before starting the gateway,
// to find out whether the config is any good. It answered "ok", exit 0, for a
// config the gateway then refused to start on -- which is worse than silence,
// because it is the one place a person would think the question had been
// answered.
func TestValidateDoesNotApproveAConfigTheGatewayWouldRefuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	// baseUrl, not baseURL. The loader ignores the key it does not know, so
	// the endpoint silently stays empty.
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseUrl: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)
	if err == nil {
		t.Fatalf("validate approved a config with no endpoint; it said:\n%s\n"+
			"the same config makes the gateway exit with \"baseURL is required\"", out)
	}
	if !strings.Contains(err.Error(), "baseURL is required") {
		t.Fatalf("validate failed for the wrong reason: %v\n%s\n"+
			"the wording should match what the gateway says, or the two are describing different problems", err, out)
	}
	if strings.Contains(out.String(), "\nok\n") {
		t.Fatalf("validate printed ok alongside the failure:\n%s", out)
	}
}

// The typo is the diagnosis; the missing endpoint is only its symptom. A user
// told "baseURL is required" has to guess which of the eleven ways they got it
// wrong applies, so the key they actually wrote has to be named too.
func TestValidateNamesTheKeyYouWroteBeforeNamingWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseUrl: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	_ = ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)
	got := out.String()
	if !strings.Contains(got, "baseUrl") || !strings.Contains(got, "baseURL") {
		t.Fatalf("the warning does not name the key that was written and the key it probably meant:\n%s", got)
	}
	if !strings.Contains(got, "did you mean") {
		t.Fatalf("the warning names the ignored key but not what to write instead:\n%s", got)
	}
	if !strings.Contains(got, "providers[0]") {
		t.Fatalf("the warning does not say which provider carries the typo:\n%s", got)
	}
}

// A config with an ignored key still works, so warning must not become
// refusing. Failing here would break a running deployment over a spelling,
// which is a far worse outcome than the typo itself.
func TestValidateWarnsAboutAnIgnoredKeyWithoutFailingOnIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
optimization:
  automatic: true
  someKnobNobodyReads: 3
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out); err != nil {
		t.Fatalf("validate failed on a config that only carries an ignored key: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "someKnobNobodyReads") {
		t.Fatalf("validate passed without mentioning the ignored key:\n%s", out)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("validate did not report the config as ok:\n%s", out)
	}
}

// Half the point is catching typos nobody notices. A warning that fires on
// every config trains people to ignore it, so the cases that are not typos
// must stay silent.
func TestValidateSaysNothingAboutAConfigWithNoIgnoredKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := []byte(`schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers:
  - id: acct
    adapter: openai_compat
    tier: paid
    baseURL: http://127.0.0.1:8479/v1
    apiKeyEnv: EXAMPLE_KEY
routes:
  my-fast-model: acct
  some.other/model-name: acct
optimization:
  automatic: true
  freeOnly: true
catalog:
  pin:
    - acct/gpt-5
  rename:
    gpt-5: GPT five
`)
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out); err != nil {
		t.Fatalf("validate failed: %v\n%s", err, out)
	}
	if got := out.String(); strings.Contains(got, "warning") || strings.Contains(got, "not read") {
		t.Fatalf("validate warned about a config carrying nothing ignored:\n%s\n"+
			"routes and rename keys are map entries, not field names, and are never unknown", got)
	}
}
