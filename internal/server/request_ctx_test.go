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
