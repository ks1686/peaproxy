package streamguard

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestBoundCommittedLifetime(t *testing.T) {
	g := New(io.Discard, 1024, time.Millisecond)
	if _, err := g.Write([]byte("data: {\"choices\":[]}\n\n")); err != nil {
		t.Fatal(err)
	}
	ctx, stop := g.Bound(context.Background())
	defer stop()
	time.Sleep(20 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
}
