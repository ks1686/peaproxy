package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

// optimization.persistentContext is documented as keeping stored artifacts on
// disk. It does not: the store is memory-only. A setting that parses, validates
// and does nothing is the failure class this whole check exists for, because
// the user believes they changed something.
//
// It is not an error by default. A config carrying it has been loading for
// months; refusing to start over it would be a breaking change to fix a
// documentation problem. It is named on stderr instead, and refused only when
// the operator has asked for exactly this class of guarantee.

const inertConfig = `schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers: []
optimization:
  persistentContext: true
`

func writeInertConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peaproxy.yaml")
	if err := os.WriteFile(path, []byte(inertConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateWarnsAboutAnInertSettingButStillPasses(t *testing.T) {
	path := writeInertConfigFile(t)
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"config", "validate", "--config", path}, out)
	if err != nil {
		t.Fatalf("validate refused a config over an inert setting: %v\n"+
			"that would break a config that has been loading for months", err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Fatalf("validate did not report ok:\n%s", out)
	}
}

func TestStrictConfigRefusesAnInertSetting(t *testing.T) {
	path := writeInertConfigFile(t)
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"config", "validate", "--config", path, "--strict-config"}, out)
	if err == nil {
		t.Fatalf("--strict-config approved an inert setting:\n%s\n"+
			"that mode exists to answer whether the config still does what it says", out)
	}
	if !strings.Contains(err.Error(), "not acted on") {
		t.Fatalf("wrong error for an inert setting: %v", err)
	}
}

func TestStrictConfigStillPassesWhenNoSettingIsInert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	src := `schemaVersion: 1
bind: 127.0.0.1
port: 8317
providers: []
optimization:
  persistentContext: false
`
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"config", "validate", "--config", path, "--strict-config"}, out); err != nil {
		t.Fatalf("--strict-config refused a config with no inert setting: %v\n%s", err, out)
	}
}

// The warning has to be visible and it goes to stderr, which is a different
// writer from the one ExecuteWithArgs takes for stdout. So this drives the
// reporting function directly and asserts on what a user would actually read.
func TestTheInertWarningNamesTheSettingAndTheGap(t *testing.T) {
	var w bytes.Buffer
	yes := true
	cfg := config.Default()
	cfg.Optimization.PersistentContext = &yes

	if err := reportInertSettings(&w, cfg, false); err != nil {
		t.Fatal(err)
	}
	text := w.String()
	for _, want := range []string{"persistentContext", "memory-only", "warning"} {
		if !strings.Contains(text, want) {
			t.Errorf("warning does not mention %q:\n%s", want, text)
		}
	}
}

func TestAnInertSettingIsSilentWhenStrictIsNotSetAndNoneAreInert(t *testing.T) {
	var w bytes.Buffer
	if err := reportInertSettings(&w, config.Default(), false); err != nil {
		t.Fatal(err)
	}
	if w.Len() != 0 {
		t.Fatalf("a clean config printed %q", w.String())
	}
}
