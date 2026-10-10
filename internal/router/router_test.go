package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

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

func TestChatFailsoverOn401HTTPError(t *testing.T) {
	calls := 0
	r := &Router{
		Candidates: []Candidate{
			{AccountID: "a", Adapter: stubAdapter{id: "a", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				calls++
				return adapter.ChatResponse{}, adapter.HTTPError{Status: 401, Body: "nope"}
			}}},
			{AccountID: "b", Adapter: stubAdapter{id: "b", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				calls++
				return adapter.ChatResponse{Content: "ok"}, nil
			}}},
		},
	}
	resp, err := r.Chat(context.Background(), adapter.ChatRequest{Model: "m"})
	if err != nil || resp.Content != "ok" || calls != 2 {
		t.Fatalf("resp=%#v err=%v calls=%d", resp, err, calls)
	}
}

func TestChatFailsoverOnRateLimitErrorBody(t *testing.T) {
	calls := 0
	r := &Router{
		Candidates: []Candidate{
			{AccountID: "a", Adapter: stubAdapter{id: "a", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				calls++
				return adapter.ChatResponse{}, adapter.HTTPError{Status: 400, Body: `{"error":{"type":"rate_limit_error"}}`}
			}}},
			{AccountID: "b", Adapter: stubAdapter{id: "b", chat: func(context.Context, adapter.ChatRequest) (adapter.ChatResponse, error) {
				calls++
				return adapter.ChatResponse{Content: "ok"}, nil
			}}},
		},
	}
	resp, err := r.Chat(context.Background(), adapter.ChatRequest{Model: "m"})
	if err != nil || resp.Content != "ok" || calls != 2 {
		t.Fatalf("resp=%#v err=%v calls=%d", resp, err, calls)
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

func TestCooldownErrorRetryAfter(t *testing.T) {
	err := CooldownError{RetryAfter: 30 * time.Second, Err: errors.New("HTTP 429")}
	if RetryAfterSeconds(err) != 30 {
		t.Fatalf("Retry-After %d", RetryAfterSeconds(err))
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("unwrap inner")
	}
}

func TestUpstreamRetryAfterSurvivesWithoutCooldownWrap(t *testing.T) {
	upstream := adapter.HTTPError{Status: 429, RetryAfter: 7 * time.Second}
	if got := RetryAfterSeconds(fmt.Errorf("stream: %w", upstream)); got != 7 {
		t.Fatalf("Retry-After %d, want the upstream's 7", got)
	}
	if got := RetryAfterSeconds(adapter.HTTPError{Status: 429}); got != 0 {
		t.Fatalf("no upstream hint: Retry-After %d, want 0", got)
	}
}

func TestTransientIncludesEdgeTransport503(t *testing.T) {
	for _, body := range []string{
		"upstream connect error or disconnect/reset before headers. reset reason: connection timeout",
		"upstream connect error or disconnect/reset before headers. retried and the latest reset reason: remote connection failure, transport failure reason: delayed connect error: Connection refused",
	} {
		if !Transient(adapter.HTTPError{Status: 503, Body: body}) {
			t.Fatalf("edge transport 503 not transient: %q", body)
		}
	}
	if Transient(adapter.HTTPError{Status: 503, Body: `{"error":{"type":"overloaded_error","message":"Overloaded"}}`}) {
		t.Fatal("a provider overload 503 must stay a normal cooldown, not a transient retry")
	}
}

func TestTransientIncludesTransportFailures(t *testing.T) {
	for _, msg := range []string{
		`Post "https://example.test/v1/chat/completions": remote error: tls: bad record MAC`,
		`read tcp 127.0.0.1:1->127.0.0.1:2: connection reset by peer`,
		`http2: stream error: stream ID 1; INTERNAL_ERROR; received from peer`,
		`unexpected EOF`,
	} {
		if !Transient(errors.New(msg)) {
			t.Fatalf("not transient: %s", msg)
		}
	}
	if Transient(context.Canceled) {
		t.Fatal("a client cancel must not be retried")
	}
	if Transient(adapter.HTTPError{Status: 400, Body: "nope"}) {
		t.Fatal("a 400 is not a transport failure")
	}
}

func TestClientRetryAfterIsCappedForLongQuotaResets(t *testing.T) {
	long := CooldownError{RetryAfter: time.Hour, Err: errors.New("quota reached. Resets in 166h")}
	if got := RetryAfterSeconds(long); got != 60 {
		t.Fatalf("Retry-After %d, want 60 so clients that honor it uncapped do not stall for an hour", got)
	}
}
