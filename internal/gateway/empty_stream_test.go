package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

// emptyStream answers 200 with a stream that carries no event at all: a ping
// and [DONE], the shape of an upstream that dropped the turn.
func emptyStream(hits *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		*hits++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(": ping\n\ndata: [DONE]\n\n"))
	}
}

type streamEntry struct {
	name string
	run  func(*Gateway, io.Writer) error
}

var streamEntries = []streamEntry{
	{"chat", func(g *Gateway, w io.Writer) error {
		_, err := g.ChatStream(context.Background(), []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`), w)
		return err
	}},
	{"claude", func(g *Gateway, w io.Writer) error {
		_, err := g.ClaudeChatStream(context.Background(), claudeBody("m"), w)
		return err
	}},
	{"responses", func(g *Gateway, w io.Writer) error {
		_, err := g.ResponsesStream(context.Background(), responsesBody("m"), w)
		return err
	}},
}

// A stream that ends without a single event is not an answer. It used to count
// as a success: the client got an empty 200 and the account was credited.
func TestAnEmptyStreamFailsOverToAnAccountThatAnswers(t *testing.T) {
	for _, e := range streamEntries {
		t.Run(e.name, func(t *testing.T) {
			var first, second int
			g := twoAccountGateway(t, emptyStream(&first), streamStatusCounter(0, "", &second))
			out := &strings.Builder{}
			if err := e.run(g, out); err != nil {
				t.Fatalf("empty stream from acct-a was not failed over: %v", err)
			}
			if first == 0 || second == 0 {
				t.Fatalf("attempts a=%d b=%d", first, second)
			}
			if !strings.Contains(out.String(), "ok") {
				t.Fatalf("client did not get the healthy account's answer: %q", out.String())
			}
		})
	}
}

func TestAnEmptyStreamFromEveryAccountIsAnError(t *testing.T) {
	for _, e := range streamEntries {
		t.Run(e.name, func(t *testing.T) {
			var first, second int
			g := twoAccountGateway(t, emptyStream(&first), emptyStream(&second))
			out := &strings.Builder{}
			if err := e.run(g, out); err == nil {
				t.Fatalf("every account sent an empty stream and the request succeeded; client got %q", out.String())
			}
			if out.Len() != 0 {
				t.Fatalf("bytes reached the client: %q", out.String())
			}
		})
	}
}

// nativeEmpty is a native Messages and Responses adapter that streams body
// verbatim; the native paths write straight into the guard, with no translator.
type nativeEmpty struct {
	rawStreamAdapter
	body string
	hits *int
}

func (a *nativeEmpty) ResponsesStream(_ context.Context, _ []byte, w io.Writer) error {
	*a.hits++
	_, _ = io.WriteString(w, a.body)
	return nil
}

func (a *nativeEmpty) Messages(context.Context, []byte) ([]byte, error) { return nil, nil }

func (a *nativeEmpty) MessagesStream(_ context.Context, _ []byte, w io.Writer) error {
	*a.hits++
	_, _ = io.WriteString(w, a.body)
	return nil
}

func TestAnEmptyNativeStreamFailsOver(t *testing.T) {
	const empty = ": ping\n\ndata: [DONE]\n\n"
	answers := map[string]string{
		"claude":    "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"x\",\"model\":\"m\",\"content\":[]}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		"responses": "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"m\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"model\":\"m\"}}\n\n",
	}
	for _, e := range streamEntries[1:] {
		t.Run(e.name, func(t *testing.T) {
			var first, second int
			reg := adapter.NewRegistry()
			reg.Register("empty", func(adapter.Options) (adapter.Adapter, error) {
				return &nativeEmpty{body: empty, hits: &first}, nil
			})
			reg.Register("answers", func(adapter.Options) (adapter.Adapter, error) {
				return &nativeEmpty{body: answers[e.name], hits: &second}, nil
			})
			cfg := config.Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317, Providers: []config.Provider{
				{ID: "acct-a", Adapter: "empty", Tier: "paid"}, {ID: "acct-b", Adapter: "answers", Tier: "paid"},
			}}
			gw, err := New(cfg, "", reg)
			if err != nil {
				t.Fatal(err)
			}
			gw.models = []catalog.Model{
				{ID: "m", AccountID: "acct-a", Tier: catalog.TierPaid, Routable: true, Exposed: true},
				{ID: "m", AccountID: "acct-b", Tier: catalog.TierPaid, Routable: true, Exposed: true},
			}
			out := &strings.Builder{}
			if err := e.run(gw, out); err != nil {
				t.Fatalf("empty native stream was not failed over: %v", err)
			}
			if first == 0 || second == 0 || !strings.Contains(out.String(), "ok") {
				t.Fatalf("attempts a=%d b=%d, client got %q", first, second, out.String())
			}
		})
	}
}
