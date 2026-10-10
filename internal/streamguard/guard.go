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
	DefaultTimeout  = 30 * time.Second
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
	unbounded bool
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
	g.unbounded = false
	g.deadline = time.Now().Add(g.timeout)
}

// Unbounded drops the prelude deadline until the next Reset. Use it when no
// other account can take over, so a slow first event is not aborted.
func (g *Guard) Unbounded() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.unbounded = true
	g.deadline = time.Time{}
}

// Bound cancels ctx if no valid event arrives before the prelude timeout.
// A keepalive extends that deadline; the timer follows the new deadline
// instead of the original one-shot wait. The returned stop waits for the
// timer to exit, so it cannot fail the attempt that follows.
func (g *Guard) Bound(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	g.mu.Lock()
	unbounded := g.unbounded
	g.mu.Unlock()
	if unbounded {
		return ctx, cancel
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		defer func() {
			if !g.Committed() {
				cancel()
			}
		}()
		for {
			g.mu.Lock()
			deadline := g.deadline
			committed := g.committed
			g.mu.Unlock()
			if committed {
				return
			}
			wait := time.Until(deadline)
			if wait < 0 {
				wait = 0
			}
			timer := time.NewTimer(wait)
			select {
			case <-stop:
				timer.Stop()
				return
			case <-parent.Done():
				timer.Stop()
				return
			case <-timer.C:
				g.mu.Lock()
				committed = g.committed
				remain := time.Until(g.deadline)
				if !committed && !g.unbounded && remain <= 0 && g.failed == nil {
					g.failed = ErrTimeout
				}
				g.mu.Unlock()
				if committed || remain <= 0 {
					return
				}
			}
		}
	}()
	return ctx, func() {
		once.Do(func() { close(stop) })
		cancel()
		<-done
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
	var kept []byte
	i := 0
	for i < len(raw) {
		nl := bytes.IndexByte(raw[i:], '\n')
		if nl < 0 {
			kept = append(kept, raw[i:]...)
			break
		}
		end := i + nl + 1
		line := string(bytes.TrimRight(raw[i:i+nl], "\r"))
		switch classifyLine(line) {
		case lineKeepalive:
			keepalive = true
			if !g.unbounded {
				g.deadline = time.Now().Add(g.timeout)
			}
			// Comment pings (`: ping`) and empty `data:` lines are not events,
			// so they stay out of the prelude and cannot fill the byte cap.
			// A blank line after a real event is that event's terminator.
			// Dropping it glues Anthropic's `event: ping` onto `message_start`.
			if strings.TrimSpace(line) == "" && len(kept) > 0 {
				kept = append(kept, raw[i:end]...)
			}
			i = end
		case lineError:
			return keepalive, ErrPrelude
		case lineCommit:
			g.committed = true
			payload := append(append([]byte{}, kept...), raw[i:]...)
			_, err = g.dst.Write(payload)
			g.buf.Reset()
			return keepalive, err
		default:
			kept = append(kept, raw[i:end]...)
			i = end
		}
	}
	if keepalive {
		g.buf.Reset()
		if len(kept) > 0 {
			g.buf.Write(kept)
		}
	}
	return keepalive, nil
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
		"response.function_call_arguments.delta", "response.completed", "response.incomplete":
		return lineCommit
	case "error", "response.failed":
		return lineError
	default:
		return lineOther
	}
}
