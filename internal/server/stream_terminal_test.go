package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
)

// A stream that ends without any terminal event is what makes a client report
// "stream ended without finish_reason". PeaProxy used to record such a turn as
// an ordinary success, so the request log could not tell a provider that cut
// the stream off from one that finished properly. The class is recorded now.
//
// https://github.com/ks1686/peaproxy plan T1 / defect D1 diagnostics.
func streamTerminalServer(t *testing.T, upstream http.HandlerFunc) *Server {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	cfg := config.Config{
		SchemaVersion: 1,
		Providers: []config.Provider{{
			ID: "only", Adapter: "openai_compat", Tier: "paid", BaseURL: up.URL + "/v1",
		}},
	}
	gw, err := gateway.New(cfg, filepath.Join(t.TempDir(), "peaproxy.yaml"), adapters.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	gw.Refresh(context.Background())
	return New(Options{Gateway: gw, ClientRoot: filepath.Join(t.TempDir(), "home")})
}

func chatStreamOnce(t *testing.T, s *Server) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestStreamTerminalRecordedWhenUpstreamCuts(t *testing.T) {
	s := streamTerminalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half\"}}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Upstream closes with no finish_reason and no [DONE].
	})
	rr := chatStreamOnce(t, s)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	recent := s.gw.Usage.Recent()
	if len(recent) == 0 {
		t.Fatal("no usage event recorded")
	}
	if got := recent[0].StreamTerminal; got != TerminalNone {
		t.Fatalf("streamTerminal = %q, want %q", got, TerminalNone)
	}
}

func TestStreamTerminalRecordedWhenUpstreamFinishes(t *testing.T) {
	s := streamTerminalServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m"}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	chatStreamOnce(t, s)
	recent := s.gw.Usage.Recent()
	if len(recent) == 0 {
		t.Fatal("no usage event recorded")
	}
	if got := recent[0].StreamTerminal; got != TerminalDone {
		t.Fatalf("streamTerminal = %q, want %q", got, TerminalDone)
	}
}
