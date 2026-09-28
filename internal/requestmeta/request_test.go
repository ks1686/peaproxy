package requestmeta

import (
	"context"
	"testing"
	"time"
)

// TestWithRequestKeepsMetadataSeparateFromContextDeadline catches a metadata
// regression that overwrites a harness-provided cancellation deadline.
func TestWithRequestKeepsMetadataSeparateFromContextDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute).Round(0)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)

	want := Request{ID: "request-1", SessionID: "session-1", Model: "coding", Wire: WireResponses}
	gotCtx := WithRequest(ctx, want)
	got, ok := FromContext(gotCtx)
	if !ok {
		t.Fatal("request metadata missing")
	}
	if got != want {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
	gotDeadline, ok := gotCtx.Deadline()
	if !ok || !gotDeadline.Equal(deadline) {
		t.Fatalf("deadline = %v (set=%v), want %v", gotDeadline, ok, deadline)
	}
}

// TestWithRequestCopiesValue catches callers mutating request metadata after it
// has crossed the server-to-gateway boundary.
func TestWithRequestCopiesValue(t *testing.T) {
	req := Request{ID: "request-1", Model: "one", Requirements: Requirements{Tools: true}}
	ctx := WithRequest(context.Background(), req)
	req.Model = "two"
	req.Requirements.Tools = false

	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("request metadata missing")
	}
	if got.Model != "one" || !got.Requirements.Tools {
		t.Fatalf("metadata mutated through caller value: %#v", got)
	}
}
