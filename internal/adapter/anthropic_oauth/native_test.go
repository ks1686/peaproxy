package anthropic_oauth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

const (
	nativeUA      = "claude-cli/2.1.300 (external, cli)"
	nativeSession = "22222222-2222-4222-8222-222222222222"
)

// nativeBody is the shape a real Claude Code build sends: its own billing
// block and identity line in system, and a JSON-string metadata.user_id.
var nativeBody = `{"model":"claude-sonnet-5","max_tokens":32000,` +
	`"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.300.abc; cc_entrypoint=cli; cch=1a2b3;"},` +
	`{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":{"type":"ephemeral"}}],` +
	`"metadata":{"user_id":"{\"device_id\":\"` + strings.Repeat("a", 64) + `\",\"account_uuid\":\"\",\"session_id\":\"` + nativeSession + `\"}"},` +
	`"messages":[{"role":"user","content":"hi"}]}`

// nativeHeader is the full identity header set a real Claude Code build sends.
func nativeHeader() http.Header {
	h := http.Header{}
	h.Set("User-Agent", nativeUA)
	h.Set("X-App", "cli")
	h.Set("X-Stainless-Package-Version", "0.120.0")
	h.Set("X-Stainless-Runtime", "node")
	h.Set("X-Stainless-Runtime-Version", "v26.9.0")
	h.Set("X-Stainless-Lang", "js")
	h.Set("X-Stainless-Os", "MacOS")
	h.Set("X-Stainless-Arch", "arm64")
	h.Set("X-Stainless-Timeout", "600")
	h.Set("X-Claude-Code-Session-Id", nativeSession)
	return h
}

func nativeCtx() context.Context {
	h := nativeHeader()
	return requestmeta.WithRequest(context.Background(), requestmeta.Request{
		Wire:              requestmeta.WireMessages,
		AnthropicBeta:     "claude-code-20250219,interleaved-thinking-2025-05-14,effort-2025-11-24,context-1m-2025-08-07",
		ClaudeCodeHeaders: requestmeta.ClaudeCodeHeaders(h),
	})
}

func TestMessagesForwardsNativeClaudeCodeIdentity(t *testing.T) {
	got, body, beta := captureMessagesCtx(t, nativeCtx(), "claude-sonnet-5", nativeBody)
	if ua := got.Header.Get("User-Agent"); ua != nativeUA {
		t.Fatalf("User-Agent %q, want the client's own %q", ua, nativeUA)
	}
	if v := got.Header.Get("X-Stainless-Package-Version"); v != "0.120.0" {
		t.Fatalf("X-Stainless-Package-Version %q, want the client's 0.120.0", v)
	}
	if v := got.Header.Get("X-Stainless-Runtime-Version"); v != "v26.9.0" {
		t.Fatalf("X-Stainless-Runtime-Version %q", v)
	}
	if v := got.Header.Get("X-Stainless-Os"); v != "MacOS" {
		t.Fatalf("X-Stainless-Os %q, want the client's MacOS", v)
	}
	if v := got.Header.Get("X-Claude-Code-Session-Id"); v != nativeSession {
		t.Fatalf("X-Claude-Code-Session-Id %q", v)
	}
	if got.Header.Get("Authorization") != "Bearer oauth-at" {
		t.Fatalf("Authorization %q", got.Header.Get("Authorization"))
	}
	const wantBeta = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,effort-2025-11-24"
	if beta != wantBeta {
		t.Fatalf("anthropic-beta %q, want the client's list plus the OAuth beta %q", beta, wantBeta)
	}
	sys := decodeSystemBlocks(t, body)
	if len(sys) != 2 || !strings.Contains(sys[0].Text, "cc_version=2.1.300.abc;") {
		t.Fatalf("native system must reach Anthropic as the client sent it, got %#v", sys)
	}
	if !bytes.Contains(body, []byte(nativeSession)) || bytes.Contains(body, []byte("cc_version=2.1.280")) {
		t.Fatalf("native metadata must be kept and no 2.1.280 cloak added: %s", body)
	}
}

func TestMessagesKeepsFixedProfileWhenOnlyHeadersClaimClaudeCode(t *testing.T) {
	// A harness that copies Claude Code's User-Agent but sends its own body
	// has no billing block; it must not get its headers forwarded.
	raw := `{"model":"claude-sonnet-5","system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[{"role":"user","content":"hi"}]}`
	got, _, beta := captureMessagesCtx(t, nativeCtx(), "claude-sonnet-5", raw)
	if ua := got.Header.Get("User-Agent"); ua != MessagesUserAgent {
		t.Fatalf("User-Agent %q, want fixed %q", ua, MessagesUserAgent)
	}
	if v := got.Header.Get("X-Stainless-Package-Version"); v != StainlessPackageVersion {
		t.Fatalf("X-Stainless-Package-Version %q", v)
	}
	if !strings.HasPrefix(beta, oauthBetasPrefix) {
		t.Fatalf("anthropic-beta %q, want the fixed 2.1.280 list", beta)
	}
}

func TestMessagesStreamForwardsNativeClaudeCodeIdentity(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "oauth-at", ExpiresAt: time.Now().Add(time.Hour)}
	var out bytes.Buffer
	if err := a.MessagesStream(nativeCtx(), []byte(nativeBody), &out); err != nil {
		t.Fatal(err)
	}
	if gotUA != nativeUA {
		t.Fatalf("stream User-Agent %q, want %q", gotUA, nativeUA)
	}
}

func TestMessagesNativeKeepsLongClaudeCodeBetaList(t *testing.T) {
	// A future Claude Code build may send more betas than today's 11; every
	// one must reach Anthropic, since the 2.1.280 list no longer backs it up.
	names := []string{"claude-code-20250219", "context-1m-2025-08-07"}
	for i := 0; i < 18; i++ {
		names = append(names, fmt.Sprintf("future-beta-%02d-2027-01-01", i))
	}
	h := nativeHeader()
	ctx := requestmeta.WithRequest(context.Background(), requestmeta.Request{
		AnthropicBeta:     requestmeta.NormalizeAnthropicBeta(strings.Join(names, ",")),
		ClaudeCodeHeaders: requestmeta.ClaudeCodeHeaders(h),
	})
	_, _, beta := captureMessagesCtx(t, ctx, "claude-sonnet-5", nativeBody)
	want := "claude-code-20250219,oauth-2025-04-20," + strings.Join(names[2:], ",")
	if beta != want {
		t.Fatalf("anthropic-beta\n got %s\nwant %s", beta, want)
	}
}
