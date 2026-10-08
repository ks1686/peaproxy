package anthropic_oauth

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

// #63: client tool names are sent under Claude Code's names and restored in the
// response, so an upstream 4xx shows names the user never wrote. That is
// deliberate -- it is what upstream validates -- but it makes a
// "400 tool name ..." hard to trace.

// aliasDebugLine is the line the adapter logs on a 4xx when PEAPROXY_DEBUG is
// set. It must name the account, the status and the alias pairs, and nothing
// else: no headers, no body, no tokens.
func TestAliasDebugLineIsBuiltWithoutSecrets(t *testing.T) {
	// The reverse map the adapter carries is upstream -> client; the log wants
	// client -> upstream, which is the direction a reader traces.
	restore := map[string]string{
		"ClaudeCode_Bash_01": "Bash",
		"ClaudeCode_Read_02": "Read",
	}
	line := aliasDebugLine("acct-1", 400, restore)
	if !strings.Contains(line, "acct-1") {
		t.Errorf("no account id: %q", line)
	}
	if !strings.Contains(line, "400") {
		t.Errorf("no status: %q", line)
	}
	for _, want := range []string{"Bash", "ClaudeCode_Bash_01", "Read", "ClaudeCode_Read_02"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q: %q", want, line)
		}
	}
	// Sorted, so two runs of the same request log the same line.
	if strings.Index(line, "Bash") > strings.Index(line, "Read") {
		t.Errorf("pairs are not sorted: %q", line)
	}
}

// Nothing to say is nothing to log.
func TestAliasDebugLineIsEmptyWithoutAliases(t *testing.T) {
	if got := aliasDebugLine("acct-1", 400, nil); got != "" {
		t.Errorf("an empty alias map should log nothing, got %q", got)
	}
	if got := aliasDebugLine("acct-1", 400, map[string]string{}); got != "" {
		t.Errorf("an empty alias map should log nothing, got %q", got)
	}
}

// The flag is off by default. A 4xx in normal operation must not write a line.
func TestAliasDebugLoggingIsOffWithoutTheFlag(t *testing.T) {
	t.Setenv("PEAPROXY_DEBUG", "")
	dir := t.TempDir()
	path := dir + "/log"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	restore := log.Writer()
	log.SetOutput(file)
	defer log.SetOutput(restore)

	noteAliasedToolNames("acct-1", 400, map[string]string{"ClaudeCode_Bash_01": "Bash"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(data)) != 0 {
		t.Errorf("logged without PEAPROXY_DEBUG: %q", data)
	}
}

func TestAliasDebugLoggingIsOnWithTheFlag(t *testing.T) {
	t.Setenv("PEAPROXY_DEBUG", "1")
	dir := t.TempDir()
	path := dir + "/log"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	restore := log.Writer()
	log.SetOutput(file)
	defer log.SetOutput(restore)

	noteAliasedToolNames("acct-1", 422, map[string]string{"ClaudeCode_Bash_01": "Bash"})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Bash") {
		t.Errorf("PEAPROXY_DEBUG did not produce the alias line: %q", data)
	}
}

// A 2xx is not a problem to trace.
func TestOnlyClientErrorsAreNoted(t *testing.T) {
	t.Setenv("PEAPROXY_DEBUG", "1")
	dir := t.TempDir()
	path := dir + "/log"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	restore := log.Writer()
	log.SetOutput(file)
	defer log.SetOutput(restore)

	noteAliasedToolNames("acct-1", 200, map[string]string{"ClaudeCode_Bash_01": "Bash"})
	noteAliasedToolNames("acct-1", 500, map[string]string{"ClaudeCode_Bash_01": "Bash"})
	data, _ := os.ReadFile(path)
	if len(bytes.TrimSpace(data)) != 0 {
		t.Errorf("a non-4xx was logged: %q", data)
	}
}

// The pairs must be sorted deterministically from a map, whose iteration order
// Go deliberately randomises.
func TestAliasPairsAreSortedDeterministically(t *testing.T) {
	restore := map[string]string{
		"cc_z": "Zeta", "cc_a": "Alpha", "cc_m": "Mid", "cc_b": "Beta",
	}
	want := aliasDebugLine("acct", 400, restore)
	for i := 0; i < 20; i++ {
		if got := aliasDebugLine("acct", 400, restore); got != want {
			t.Fatalf("line changed between runs:\n%q\n%q", want, got)
		}
	}
	pairs := []string{"Alpha", "Beta", "Mid", "Zeta"}
	pos := -1
	for _, p := range pairs {
		at := strings.Index(want, p)
		if at <= pos {
			t.Fatalf("pairs out of order in %q", want)
		}
		pos = at
	}
}
