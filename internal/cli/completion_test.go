package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ks1686/peaproxy/internal/clients"
)

// #82: tab completions for the shells people actually use.
func TestCompletionScriptsForEveryShell(t *testing.T) {
	for _, tc := range []struct{ shell, marker string }{
		{"bash", "__start_peaproxy"},
		{"zsh", "#compdef peaproxy"},
		{"fish", "complete -c peaproxy"},
		{"powershell", "Register-ArgumentCompleter"},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			out := &bytes.Buffer{}
			if err := ExecuteWithArgs([]string{"completion", tc.shell}, out); err != nil {
				t.Fatal(err)
			}
			if out.Len() < 100 {
				t.Fatalf("%s script looks empty:\n%s", tc.shell, out)
			}
			if !strings.Contains(out.String(), tc.marker) {
				t.Fatalf("%s script has no %q:\n%s", tc.shell, tc.marker, out)
			}
		})
	}
}

// A typo should say so rather than emit nothing at all.
func TestCompletionRejectsUnknownShell(t *testing.T) {
	out := &bytes.Buffer{}
	err := ExecuteWithArgs([]string{"completion", "tcsh"}, out)
	if err == nil {
		t.Fatal("expected an error for an unsupported shell")
	}
	if !strings.Contains(err.Error(), "bash") {
		t.Fatalf("the error should list what is supported, got %v", err)
	}
}

// The script has to be installable, so the command itself has to be a real
// subcommand rather than something that only exists in the docs.
func TestCompletionIsListedInHelp(t *testing.T) {
	out := &bytes.Buffer{}
	if err := ExecuteWithArgs([]string{"--help"}, out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "completion") {
		t.Fatalf("completion is not in the help output:\n%s", out)
	}
}

func findCmd(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cmd := NewRoot()
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cmd.Commands() {
			if c.Name() == name || strings.HasPrefix(c.Name(), name+" ") {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("no subcommand %q under %q", name, cmd.Name())
		}
		cmd = next
	}
	return cmd
}

func complete(t *testing.T, cmd *cobra.Command, args []string, toComplete string) []string {
	t.Helper()
	if cmd.ValidArgsFunction == nil {
		t.Fatalf("%s has no completion", cmd.CommandPath())
	}
	got, directive := cmd.ValidArgsFunction(cmd, args, toComplete)
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("%s asked the shell to do file completion (%v), which it should not", cmd.CommandPath(), directive)
	}
	return got
}

// Every command that takes a client name should offer the presets.
// import is Pi-only, so it offers just pi rather than every client.
func TestImportCompletesPiOnly(t *testing.T) {
	cmd := findCmd(t, "clients", "import")
	got := complete(t, cmd, nil, "")
	if len(got) != 1 || got[0] != "pi" {
		t.Fatalf("clients import completes %v, want [pi]", got)
	}
}

func TestClientNameCompletion(t *testing.T) {
	want := map[string]bool{}
	for _, n := range clients.List() {
		want[n] = true
	}
	for _, path := range [][]string{
		{"clients", "show"},
		{"clients", "verify"},
		{"clients", "connect"},
		{"clients", "disconnect"},
	} {
		cmd := findCmd(t, path...)
		got := complete(t, cmd, nil, "")
		if len(got) == 0 {
			t.Fatalf("%s completes nothing", cmd.CommandPath())
		}
		for _, g := range got {
			if !want[g] {
				t.Errorf("%s offered %q, which is not a preset", cmd.CommandPath(), g)
			}
		}
		for _, n := range clients.List() {
			if !contains(got, n) && want[n] {
				t.Errorf("%s does not offer %q", cmd.CommandPath(), n)
			}
		}
	}
}

// Model ids come from the config the user already has: pinned, renamed and
// hidden are exactly the ids they have been working with.
func TestModelIDCompletionFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	body := `schemaVersion: 1
bind: 127.0.0.1
port: 8317
hide:
  models:
    - hidden-model
catalog:
  pin:
    - pinned-model
  rename:
    renamed-model: Renamed
providers:
  - id: local
    adapter: openai_compat
    baseURL: http://127.0.0.1:1234/v1
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"pin", "rename", "hide"} {
		cmd := findCmd(t, "catalog", sub)
		setConfig(t, cmd, path)
		got := complete(t, cmd, nil, "")
		for _, want := range []string{"hidden-model", "pinned-model", "renamed-model"} {
			if !contains(got, want) {
				t.Errorf("catalog %s does not complete %q; got %v", sub, want, got)
			}
		}
	}
}

// The models a user has actually called are worth completing too, and they come
// from the usage file next to the config.
func TestModelIDCompletionFromUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	if err := os.WriteFile(path, []byte("schemaVersion: 1\nbind: 127.0.0.1\nport: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events := `{"events":[
		{"time":"2026-01-02T03:04:05Z","model":"claude-sonnet-5-5","path":"/v1/messages"},
		{"time":"2026-01-02T03:04:06Z","model":"gpt-5.6-sol","path":"/v1/responses"},
		{"time":"2026-01-02T03:04:07Z","model":"claude-sonnet-5-5","path":"/v1/messages"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := findCmd(t, "catalog", "pin")
	setConfig(t, cmd, path)
	got := complete(t, cmd, nil, "")
	for _, want := range []string{"claude-sonnet-5-5", "gpt-5.6-sol"} {
		if !contains(got, want) {
			t.Errorf("a model from the usage file was not completed: %q in %v", want, got)
		}
	}
	// Each id once, whatever the traffic.
	if n := count(got, "claude-sonnet-5-5"); n != 1 {
		t.Errorf("claude-sonnet-5-5 offered %d times, want 1: %v", n, got)
	}
}

// `catalog hide --kind provider` completes providers, not models.
func TestProviderIDCompletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	body := `schemaVersion: 1
bind: 127.0.0.1
port: 8317
hide:
  providers:
    - my-provider
providers:
  - id: local
    adapter: openai_compat
    baseURL: http://127.0.0.1:1234/v1
  - id: subscription-1
    adapter: anthropic_oauth
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := findCmd(t, "catalog", "hide")
	setConfig(t, cmd, path)
	// --kind provider is what asks for provider ids; without it hide completes
	// model ids, and this config has none.
	if err := cmd.Flags().Set("kind", "provider"); err != nil {
		t.Fatal(err)
	}
	got := complete(t, cmd, []string{}, "")
	if !contains(got, "my-provider") {
		t.Errorf("provider id not completed: %v", got)
	}
	if !contains(got, "local") || !contains(got, "subscription-1") {
		t.Errorf("configured provider ids should be completed: %v", got)
	}
}

// Completion runs on every TAB. It must never fail, never block, and never be
// the thing that puts an error in someone's terminal.
func TestCompletionNeverFails(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, path string }{
		{"missing config", filepath.Join(dir, "nope.yaml")},
		{"unreadable config", dir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := findCmd(t, "catalog", "pin")
			setConfig(t, cmd, tc.path)
			if got := complete(t, cmd, nil, ""); len(got) != 0 {
				t.Fatalf("expected no candidates, got %v", got)
			}
		})
	}
	t.Run("corrupt config", func(t *testing.T) {
		bad := filepath.Join(dir, "bad.yaml")
		if err := os.WriteFile(bad, []byte("this: [is: not: yaml"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := findCmd(t, "catalog", "pin")
		setConfig(t, cmd, bad)
		if got := complete(t, cmd, nil, ""); len(got) != 0 {
			t.Fatalf("expected no candidates from a corrupt config, got %v", got)
		}
	})
}

// A model id containing a slash or a space is still offered; the shell will
// quote it and the command will take it.
func TestCompletionOffersAwkwardModelIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peaproxy.yaml")
	body := `schemaVersion: 1
bind: 127.0.0.1
port: 8317
catalog:
  pin:
    - "vendor/model:1.0"
    - "has space"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := findCmd(t, "catalog", "pin")
	setConfig(t, cmd, path)
	got := complete(t, cmd, nil, "")
	for _, want := range []string{"vendor/model:1.0", "has space"} {
		if !contains(got, want) {
			t.Errorf("%q was dropped: %v", want, got)
		}
	}
}

// Deterministic order, so a shell that shows a list does not reshuffle it.
func TestCompletionOrderIsStable(t *testing.T) {
	cmd := findCmd(t, "catalog", "pin")
	first := append([]string(nil), complete(t, cmd, nil, "")...)
	for i := 0; i < 5; i++ {
		again := complete(t, cmd, nil, "")
		if strings.Join(again, ",") != strings.Join(first, ",") {
			t.Fatalf("completion order changed between runs:\n%v\n%v", first, again)
		}
	}
	sorted := append([]string(nil), first...)
	sort.Strings(sorted)
	if strings.Join(sorted, ",") != strings.Join(first, ",") && len(first) > 1 {
		t.Fatalf("completion is not sorted:\n%v", first)
	}
}

// accounts add takes a preset, not a configured id, so it completes presets.
func TestAccountPresetCompletion(t *testing.T) {
	cmd := findCmd(t, "accounts", "add")
	got := complete(t, cmd, nil, "")
	if len(got) == 0 {
		t.Fatal("accounts add completes nothing")
	}
	for _, g := range got {
		if strings.ContainsAny(g, " \t") {
			t.Errorf("preset %q should be a single word", g)
		}
	}
}

// setConfig points --config at path. It is a root persistent flag, so it has to
// be set on the root for the command's captured variable to see it.
func setConfig(t *testing.T, cmd *cobra.Command, path string) {
	t.Helper()
	if err := cmd.Root().PersistentFlags().Set("config", path); err != nil {
		t.Fatal(err)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func count(list []string, want string) int {
	n := 0
	for _, v := range list {
		if v == want {
			n++
		}
	}
	return n
}
