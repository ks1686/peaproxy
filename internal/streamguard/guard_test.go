package streamguard

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPreludeErrorBeforeCommit(t *testing.T) {
	var dst bytes.Buffer
	g := New(&dst, 1024, time.Minute)
	_, err := g.Write([]byte("data: {\"error\":{\"message\":\"nope\"}}\n\n"))
	if !errors.Is(err, ErrPrelude) {
		t.Fatalf("error = %v", err)
	}
	if g.Committed() || dst.Len() != 0 {
		t.Fatalf("committed=%v bytes=%q", g.Committed(), dst.String())
	}
}

func TestNoRetryAfterCommit(t *testing.T) {
	var dst bytes.Buffer
	g := New(&dst, 1024, time.Minute)
	chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	if _, err := g.Write([]byte(chunk)); err != nil {
		t.Fatal(err)
	}
	if !g.Committed() {
		t.Fatal("expected commitment")
	}
	if _, err := g.Write([]byte("data: {\"error\":{\"message\":\"late\"}}\n\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dst.String(), "late") {
		t.Fatalf("post-commit bytes were not forwarded: %s", dst.String())
	}
}

func TestSplitToolArgumentsPreserved(t *testing.T) {
	var dst bytes.Buffer
	g := New(&dst, 1024, time.Minute)
	parts := []string{
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"a\\\":\"}}]}}]}\n",
		"\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]}}]}\n\n",
	}
	for _, part := range parts {
		if _, err := g.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(dst.String(), `\"a\":`) || !strings.Contains(dst.String(), "1}") {
		t.Fatalf("split arguments were not preserved: %s", dst.String())
	}
}

func TestPreludeBounded(t *testing.T) {
	var dst bytes.Buffer
	g := New(&dst, 32, time.Minute)
	_, err := g.Write([]byte(strings.Repeat("x", 64)))
	if !errors.Is(err, ErrBounded) {
		t.Fatalf("error = %v", err)
	}
	if dst.Len() != 0 {
		t.Fatal("bounded prelude was forwarded")
	}
}

func TestKeepaliveResetsTimeout(t *testing.T) {
	var dst bytes.Buffer
	g := New(&dst, 1024, 20*time.Millisecond)
	g.deadline = time.Now().Add(-time.Millisecond)
	if _, err := g.Write([]byte(": ping\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Write([]byte("data: {\"choices\":[]}\n\n")); err != nil {
		t.Fatalf("keepalive did not extend the prelude: %v", err)
	}
}
