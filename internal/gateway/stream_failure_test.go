package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/config"
)

// errChatCut is an upstream chat stream that breaks in the middle of a turn.
var errChatCut = errors.New("upstream connection reset")

// cutChat is a chat account whose stream sends the start of one tool call,
// with part of its arguments, and then fails.
type cutChat struct {
	slowNative
}

func (a *cutChat) ChatStream(_ context.Context, _ adapter.ChatRequest, w io.Writer) error {
	a.calls.Add(1)
	half := `data: {"id":"c1","model":"m","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"write","arguments":"{\"path\":\"a.t"}}]}}]}` + "\n\n"
	if _, err := io.WriteString(w, half); err != nil {
		return err
	}
	return errChatCut
}

// A translated stream whose upstream fails mid-turn must not reach the client
// as a finished turn, or the client runs the truncated tool call.
func TestTranslatedStreamFailureNeverFinishesTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		call    streamCall
		body    string
		started string
		deny    []string
	}{
		{"messages", (*Gateway).ClaudeChatStream, `{"model":"m","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`,
			"event: message_start", []string{`"type":"tool_use"`, "event: message_stop"}},
		{"responses", (*Gateway).ResponsesStream, `{"model":"m","stream":true,"input":"hi"}`,
			"event: response.created", []string{"event: response.completed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given the only account serves chat and its stream fails after half a tool call.
			reg := adapter.NewRegistry()
			reg.Register("cut", func(adapter.Options) (adapter.Adapter, error) {
				return compatOnly{&cutChat{slowNative: slowNative{id: "cut"}}}, nil
			})
			gw, err := New(config.Config{Providers: []config.Provider{{ID: "cut", Adapter: "cut"}}}, "", reg)
			if err != nil {
				t.Fatal(err)
			}
			gw.Refresh(context.Background())
			var out strings.Builder

			// When a client streams the turn through translation.
			_, err = tc.call(gw, context.Background(), []byte(tc.body), &out)

			// Then the client saw the turn start but never finish.
			if !errors.Is(err, errChatCut) {
				t.Fatalf("err = %v, want the upstream failure", err)
			}
			if !strings.Contains(out.String(), tc.started) {
				t.Fatalf("translated stream never reached the client:\n%s", out.String())
			}
			for _, d := range tc.deny {
				if strings.Contains(out.String(), d) {
					t.Fatalf("failed upstream reached the client as a finished turn (%s):\n%s", d, out.String())
				}
			}
		})
	}
}
