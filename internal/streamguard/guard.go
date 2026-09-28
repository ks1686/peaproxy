// Package streamguard buffers a stream until the first valid lifecycle event
// so a prelude error can still fail over. Bytes after that event are forwarded
// immediately and are never replayed by the caller.
package streamguard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMaxBytes = 64 << 10
	DefaultTimeout  = 5 * time.Second
)

var (
	ErrBounded = errors.New("stream prelude exceeded its buffer")
	ErrTimeout = errors.New("stream prelude timed out before a valid event")
	ErrPrelude = errors.New("stream prelude failed before a valid event")
)

// Guard sits in front of the client writer.
type Guard struct {
	dst      io.Writer
	maxBytes int
	timeout  time.Duration

	mu        sync.Mutex
	buf       bytes.Buffer
	committed bool
	failed    error
	deadline  time.Time
}

func New(dst io.Writer, maxBytes int, timeout time.Duration) *Guard {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Guard{dst: dst, maxBytes: maxBytes, timeout: timeout, deadline: time.Now().Add(timeout)}
}

// Committed reports whether a valid event has been forwarded to the client.
func (g *Guard) Committed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.committed
}

// Err returns a prelude failure that has not been forwarded.
func (g *Guard) Err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed {
		return nil
	}
	return g.failed
}

// Reset drops an uncommitted prelude so the next account can start clean.
func (g *Guard) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed {
		return
	}
	g.buf.Reset()
	g.failed = nil
	g.deadline = time.Now().Add(g.timeout)
}

// Bound cancels ctx if no valid event arrives before the prelude timeout.
func (g *Guard) Bound(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	timer := time.AfterFunc(g.timeout, func() {
		if !g.Committed() {
			g.mu.Lock()
			if g.failed == nil {
				g.failed = ErrTimeout
			}
			g.mu.Unlock()
			cancel()
		}
	})
	return ctx, func() {
		timer.Stop()
		cancel()
	}
}

func (g *Guard) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed {
		return g.dst.Write(p)
	}
	if g.failed != nil {
		return 0, g.failed
	}
	expired := !g.deadline.IsZero() && time.Now().After(g.deadline)
	if g.buf.Len()+len(p) > g.maxBytes {
		g.failed = ErrBounded
		return 0, g.failed
	}
	g.buf.Write(p)
	keepalive, err := g.scanLocked()
	if err != nil {
		g.failed = err
		return 0, err
	}
	if expired && !g.committed && !keepalive {
		g.failed = ErrTimeout
		return 0, g.failed
	}
	return len(p), nil
}

// Finish forwards a completed valid prelude that never saw a trailing newline.
func (g *Guard) Finish() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed || g.buf.Len() == 0 {
		return g.failed
	}
	g.buf.WriteByte('\n')
	if _, err := g.scanLocked(); err != nil {
		g.failed = err
		return err
	}
	if !g.committed {
		g.failed = ErrPrelude
		return g.failed
	}
	return nil
}

func (g *Guard) scanLocked() (keepalive bool, err error) {
	raw := g.buf.Bytes()
	for {
		i := bytes.IndexByte(raw, '\n')
		if i < 0 {
			return keepalive, nil
		}
		line := string(bytes.TrimRight(raw[:i], "\r"))
		raw = raw[i+1:]
		switch classifyLine(line) {
		case lineKeepalive:
			keepalive = true
			g.deadline = time.Now().Add(g.timeout)
		case lineError:
			return keepalive, ErrPrelude
		case lineCommit:
			g.committed = true
			_, err = g.dst.Write(g.buf.Bytes())
			g.buf.Reset()
			return keepalive, err
		}
	}
}

type lineKind uint8

const (
	lineOther lineKind = iota
	lineKeepalive
	lineError
	lineCommit
)

func classifyLine(line string) lineKind {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, ":") {
		return lineKeepalive
	}
	if name, ok := strings.CutPrefix(line, "event:"); ok {
		return classifyEvent(strings.TrimSpace(name))
	}
	payload, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return lineOther
	}
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" {
		return lineKeepalive
	}
	var body map[string]json.RawMessage
	if json.Unmarshal([]byte(payload), &body) != nil {
		return lineOther
	}
	if _, ok := body["error"]; ok {
		if _, hasChoices := body["choices"]; !hasChoices {
			return lineError
		}
	}
	if _, ok := body["choices"]; ok {
		return lineCommit
	}
	if raw, ok := body["type"]; ok {
		var kind string
		if json.Unmarshal(raw, &kind) == nil {
			return classifyEvent(kind)
		}
	}
	return lineOther
}

func classifyEvent(name string) lineKind {
	switch name {
	case "message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop",
		"response.created", "response.in_progress", "response.output_item.added", "response.output_text.delta",
		"response.function_call_arguments.delta", "response.completed":
		return lineCommit
	case "error":
		return lineError
	default:
		return lineOther
	}
}
