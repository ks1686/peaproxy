package router

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

type stubAdapter struct {
	id   string
	chat func(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error)
}

func (s stubAdapter) ID() string { return s.id }
func (s stubAdapter) ListModels(context.Context) ([]catalog.Model, error) {
	return nil, nil
}
func (s stubAdapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	return s.chat(ctx, req)
}
func (s stubAdapter) ChatStream(context.Context, adapter.ChatRequest, io.Writer) error {
	return adapter.ErrNotImplemented
}
func (s stubAdapter) Validate(context.Context) error { return nil }
func (s stubAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Chat: true}
}

func TestChatFailsoverOnRetryableThenSucceeds(t *testing.T) {
	calls := 0
	r := &Router{
		Policy: PolicyFillFirst,
		Candidates: []Candidate{
			{AccountID: "a", Adapter: stubAdapter{id: "a", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				calls++
				return adapter.ChatResponse{}, RouteError{Status: 429, Retryable: true, Err: errors.New("quota")}
			}}},
			{AccountID: "b", Adapter: stubAdapter{id: "b", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				calls++
				return adapter.ChatResponse{Content: "ok"}, nil
			}}},
		},
	}
	resp, err := r.Chat(context.Background(), adapter.ChatRequest{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || calls != 2 {
		t.Fatalf("resp=%#v calls=%d", resp, calls)
	}
}

func TestChatNoAccountWhenAllCooldown(t *testing.T) {
	r := &Router{
		Candidates: []Candidate{
			{AccountID: "a", Cooldown: true, Adapter: stubAdapter{id: "a", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				t.Fatal("cooldown account should not be called")
				return adapter.ChatResponse{}, nil
			}}},
		},
	}
	_, err := r.Chat(context.Background(), adapter.ChatRequest{Model: "m"})
	if !errors.Is(err, ErrNoAccount) {
		t.Fatalf("got %v", err)
	}
}

func TestNonRetryableStopsFailover(t *testing.T) {
	r := &Router{
		Candidates: []Candidate{
			{AccountID: "a", Adapter: stubAdapter{id: "a", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				return adapter.ChatResponse{}, RouteError{Status: 400, Retryable: false, Err: errors.New("bad request")}
			}}},
			{AccountID: "b", Adapter: stubAdapter{id: "b", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				t.Fatal("should not failover on 400")
				return adapter.ChatResponse{}, nil
			}}},
		},
	}
	_, err := r.Chat(context.Background(), adapter.ChatRequest{Model: "m"})
	if err == nil || errors.Is(err, ErrNoAccount) {
		t.Fatalf("got %v", err)
	}
}
