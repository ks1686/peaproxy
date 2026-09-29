package requestmeta

import (
	"strings"
	"testing"
)

func TestNormalizeAnthropicBeta(t *testing.T) {
	cases := []struct {
		name, header, want string
	}{
		{"empty", "", ""},
		{"trims and dedups", " thinking-binding-controls-2026-08-01 ,structured-outputs-2025-11-13,thinking-binding-controls-2026-08-01", "thinking-binding-controls-2026-08-01,structured-outputs-2025-11-13"},
		{"drops malformed names", "ok-2025-01-01,bad name,evil\r\nx: y,,semi;colon", "ok-2025-01-01"},
		{"drops oversized names", strings.Repeat("a", 65) + ",short-1", "short-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeAnthropicBeta(tc.header); got != tc.want {
				t.Fatalf("NormalizeAnthropicBeta(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestNormalizeAnthropicBetaCapsCount(t *testing.T) {
	names := make([]string, 40)
	for i := range names {
		names[i] = "beta-" + strings.Repeat("x", i+1)
	}
	got := strings.Split(NormalizeAnthropicBeta(strings.Join(names, ",")), ",")
	if len(got) != maxAnthropicBetas {
		t.Fatalf("kept %d betas, want %d", len(got), maxAnthropicBetas)
	}
}

func TestDropAnthropicBeta(t *testing.T) {
	got := DropAnthropicBeta("oauth-2025-04-20,context-1m-2025-08-07,keep-1,oauth-x", "oauth-", "context-1m-")
	if got != "keep-1" {
		t.Fatalf("DropAnthropicBeta = %q, want %q", got, "keep-1")
	}
	if got := DropAnthropicBeta("", "oauth-"); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestMergeAnthropicBeta(t *testing.T) {
	own := "claude-code-20250219,oauth-2025-04-20"
	cases := []struct {
		name, client, want string
	}{
		{"no client betas", "", own},
		{"appends new ones in client order", "thinking-binding-controls-2026-08-01,structured-outputs-2025-11-13", own + ",thinking-binding-controls-2026-08-01,structured-outputs-2025-11-13"},
		{"skips ones already sent", "oauth-2025-04-20,thinking-binding-controls-2026-08-01", own + ",thinking-binding-controls-2026-08-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeAnthropicBeta(own, tc.client); got != tc.want {
				t.Fatalf("MergeAnthropicBeta = %q, want %q", got, tc.want)
			}
		})
	}
	if got := MergeAnthropicBeta("", "a-1"); got != "a-1" {
		t.Fatalf("empty own = %q", got)
	}
}
