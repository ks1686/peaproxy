package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/usage"
)

// --since is the one thing everyone gets wrong reading an error log by hand, so
// the shapes people actually type have to work.
func TestParseSinceAcceptsWhatPeopleType(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"24h", now.Add(-24 * time.Hour)},
		{"7d", now.AddDate(0, 0, -7)},
		{"2d", now.AddDate(0, 0, -2)},
		{"12", now.Add(-12 * time.Hour)},
		{"", now.Add(-24 * time.Hour)},
		{"90m", now.Add(-90 * time.Minute)},
		{"2026-10-01", time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)},
		{"2026-10-01T09:30:00Z", time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := parseSince(c.in, now)
		if err != nil {
			t.Fatalf("parseSince(%q): %v", c.in, err)
		}
		if !got.Equal(c.want) {
			t.Fatalf("parseSince(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := parseSince("last tuesday", now); err == nil {
		t.Fatal("parseSince accepted a string it cannot mean")
	}
}

func seedErrorStore(t *testing.T, recs ...usage.Event) string {
	t.Helper()
	dir := t.TempDir()
	var lines []string
	for _, e := range recs {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	if len(lines) > 0 {
		p := usage.ErrorLogPath(dir)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "config.yaml")
}

// The bundle leads with counts so it can be read without scrolling every event,
// and it separates cancels from failures -- otherwise a machine whose agents
// disconnect constantly looks like a machine that is constantly broken.
func TestErrorsExportSeparatesCancelsFromFailures(t *testing.T) {
	now := time.Now()
	cfgPath := seedErrorStore(t,
		usage.Event{AccountID: "xai-oauth", Status: 401, Error: "invalid_grant", Class: usage.ClassAuthRevoked, Time: now.Add(-2 * time.Hour)},
		usage.Event{AccountID: "openai-oauth", Status: 429, Class: usage.ClassRateLimit, Time: now.Add(-3 * time.Hour)},
		usage.Event{AccountID: "opencode-zen", Status: 0, Error: "context canceled", Class: usage.ClassClientCancel, Time: now.Add(-time.Hour)},
		usage.Event{AccountID: "opencode-zen", Status: 0, Error: "context canceled", Class: usage.ClassClientCancel, Time: now.Add(-time.Hour)},
	)
	var out bytes.Buffer
	if err := ExecuteWithArgs([]string{"errors", "export", "--config", cfgPath, "--since", "24h"}, &out); err != nil {
		t.Fatal(err)
	}
	var b struct {
		Summary usage.ErrorSummary `json:"summary"`
		Events  []usage.Event      `json:"events"`
	}
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatalf("export is not valid JSON: %v\n%s", err, out.String())
	}
	if b.Summary.Failures != 2 || b.Summary.Cancels != 2 {
		t.Fatalf("summary = %+v, want 2 failures and 2 cancels", b.Summary)
	}
	if b.Summary.ByClass["auth_revoked"] != 1 || b.Summary.ByAcct["opencode-zen"] != 2 {
		t.Fatalf("counts wrong: %+v", b.Summary)
	}
	if len(b.Events) != 4 {
		t.Fatalf("exported %d events, want 4", len(b.Events))
	}
}

// The window is what makes a log readable; without it every bundle is the same
// bundle.
func TestErrorsExportFiltersByWindow(t *testing.T) {
	now := time.Now()
	cfgPath := seedErrorStore(t,
		usage.Event{AccountID: "old", Status: 500, Class: usage.ClassUpstreamError, Time: now.AddDate(0, 0, -3)},
		usage.Event{AccountID: "recent", Status: 500, Class: usage.ClassUpstreamError, Time: now.Add(-time.Hour)},
	)
	var out bytes.Buffer
	if err := ExecuteWithArgs([]string{"errors", "export", "--config", cfgPath, "--since", "24h"}, &out); err != nil {
		t.Fatal(err)
	}
	var b struct {
		Events []usage.Event `json:"events"`
	}
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Events) != 1 || b.Events[0].AccountID != "recent" {
		t.Fatalf("window did not filter: %+v", b.Events)
	}
}

// The store redacts on write, but a record edited by hand or written by an
// older build must not become the thing that leaks on the way out.
func TestErrorsExportRedactsAgainOnTheWayOut(t *testing.T) {
	dir := t.TempDir()
	p := usage.ErrorLogPath(dir)
	line, _ := json.Marshal(usage.Event{
		AccountID: "copilot-oauth", Status: 401,
		Error:   "bad token ghu_ExampleTokenNotARealCredential0",
		Preview: "secret prompt",
		Class:   usage.ClassAuthRevoked, Time: time.Now(),
	})
	if err := os.WriteFile(p, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := ExecuteWithArgs([]string{"errors", "export", "--config", filepath.Join(dir, "config.yaml"), "--since", "24h"}, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "ghu_ExampleTokenNotARealCredential0") {
		t.Fatalf("a GitHub token survived the export: %s", out.String())
	}
	if strings.Contains(out.String(), "secret prompt") {
		t.Fatalf("a prompt survived the export: %s", out.String())
	}
	if !strings.Contains(out.String(), "auth_revoked") {
		t.Fatalf("redaction destroyed the class: %s", out.String())
	}
}

func TestErrorsSummaryReportsCounts(t *testing.T) {
	now := time.Now()
	cfgPath := seedErrorStore(t,
		usage.Event{AccountID: "a", Status: 401, Class: usage.ClassAuthRevoked, Time: now},
		usage.Event{AccountID: "b", Status: 0, Error: "context canceled", Class: usage.ClassClientCancel, Time: now},
	)
	var out bytes.Buffer
	if err := ExecuteWithArgs([]string{"errors", "summary", "--config", cfgPath, "--since", "24h"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"failures: 1", "cancels:  1", "auth_revoked", "client_cancel"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q:\n%s", want, got)
		}
	}
}

// A machine with no failures should say so plainly, not print an empty bundle
// that reads like a truncated file.
func TestErrorsExportWithNoStore(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := ExecuteWithArgs([]string{"errors", "export", "--config", filepath.Join(dir, "config.yaml"), "--since", "24h"}, &out); err != nil {
		t.Fatal(err)
	}
	var b struct {
		Summary usage.ErrorSummary `json:"summary"`
	}
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatalf("empty export is not valid JSON: %v\n%s", err, out.String())
	}
	if b.Summary.Failures != 0 || b.Summary.Cancels != 0 {
		t.Fatalf("a missing store reported failures: %+v", b.Summary)
	}
}

func TestErrorsListPrintsOneLinePerFailure(t *testing.T) {
	cfgPath := seedErrorStore(t,
		usage.Event{AccountID: "xai-oauth", Status: 401, Error: "invalid_grant", Class: usage.ClassAuthRevoked, Time: time.Now()},
	)
	var out bytes.Buffer
	if err := ExecuteWithArgs([]string{"errors", "list", "--config", cfgPath, "--since", "24h"}, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "auth_revoked") || !strings.Contains(got, "xai-oauth") {
		t.Fatalf("list is missing the class or account: %s", got)
	}
}
