package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/requestmeta"
)

func TestRequestCtxCarriesClientAnthropicBeta(t *testing.T) {
	raw := []byte(`{"model":"claude-opus-5-5","messages":[]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(raw)))
	req.Header.Set("anthropic-beta", "thinking-binding-controls-2026-08-01, structured-outputs-2025-11-13,bad name")

	meta, ok := requestmeta.FromContext(requestCtx(req, raw))
	if !ok {
		t.Fatal("request metadata missing")
	}
	if want := "thinking-binding-controls-2026-08-01,structured-outputs-2025-11-13"; meta.AnthropicBeta != want {
		t.Fatalf("AnthropicBeta = %q, want %q", meta.AnthropicBeta, want)
	}
}

func TestRequestCtxCarriesNativeClaudeCodeHeadersOnly(t *testing.T) {
	raw := []byte(`{"model":"claude-opus-5-5","messages":[]}`)
	native := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(raw)))
	native.Header.Set("User-Agent", "claude-cli/2.1.300 (external, cli)")
	native.Header.Set("X-App", "cli")
	meta, _ := requestmeta.FromContext(requestCtx(native, raw))
	if len(meta.ClaudeCodeHeaders) != 1 || meta.ClaudeCodeHeaders[0].Value != "claude-cli/2.1.300 (external, cli)" {
		t.Fatalf("native Claude Code headers = %#v", meta.ClaudeCodeHeaders)
	}

	other := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(raw)))
	other.Header.Set("User-Agent", "opencode/1.0")
	meta, _ = requestmeta.FromContext(requestCtx(other, raw))
	if meta.ClaudeCodeHeaders != nil {
		t.Fatalf("other client got Claude Code headers %#v", meta.ClaudeCodeHeaders)
	}
}
