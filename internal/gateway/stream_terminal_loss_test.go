package gateway

import (
	"strings"
	"testing"
)

// A stream that is rewritten for a route is split into writes wherever the
// network happens to split it. Nothing may be lost at the end: the rewriter
// holds back a partial token in case the model name continues in the next
// write, and a held token that is never drained is silently discarded.
func TestRouteRewriterLosesNothingAtEverySplitPoint(t *testing.T) {
	upstream := strings.Join([]string{
		`data: {"id":"1","model":"upstream-model","choices":[{"delta":{"content":"Hel"}}]}`,
		"",
		`data: {"id":"2","model":"upstream-model","choices":[{"delta":{"content":"lo"}}]}`,
		"",
		`data: {"id":"3","model":"upstream-model","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		"",
		"data: [DONE]",
		"",
		"",
	}, "\n")

	want := strings.ReplaceAll(upstream, `"upstream-model"`, `"pea/auto"`)

	for split := 0; split <= len(upstream); split++ {
		var out strings.Builder
		w := newRouteRewriter(&out, "upstream-model", "pea/auto")
		_, _ = w.Write([]byte(upstream[:split]))
		_, _ = w.Write([]byte(upstream[split:]))

		if out.String() != want {
			t.Fatalf("split at %d: client saw %d bytes, want %d\n got: %q\nwant: %q",
				split, len(out.String()), len(want), out.String(), want)
		}
	}
}

// The same guarantee when the model name itself is split across writes, which
// is what the hold buffer exists for.
func TestRouteRewriterSurvivesAModelNameSplitAcrossWrites(t *testing.T) {
	whole := `data: {"id":"x","model":"upstream-model"}` + "\n\n"
	want := `data: {"id":"x","model":"pea/auto"}` + "\n\n"

	for split := 0; split <= len(whole); split++ {
		var out strings.Builder
		w := newRouteRewriter(&out, "upstream-model", "pea/auto")
		_, _ = w.Write([]byte(whole[:split]))
		_, _ = w.Write([]byte(whole[split:]))
		if out.String() != want {
			t.Fatalf("split at %d: got %q want %q", split, out.String(), want)
		}
	}
}

// A stream that ends while the rewriter is still holding a partial token is
// the interesting case, because that is where the held bytes have no next write
// to arrive in.
func TestRouteRewriterKeepsHeldBytesWhenTheStreamSimplyStops(t *testing.T) {
	// The stream ends in the middle of the model name, with no terminal event.
	upstream := `data: {"id":"1","model":"upstream-mod`

	var out strings.Builder
	w := newRouteRewriter(&out, "upstream-model", "pea/auto")
	n, err := w.Write([]byte(upstream))
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != len(upstream) {
		t.Errorf("Write reported %d bytes for a %d byte payload", n, len(upstream))
	}

	if fl, ok := w.(interface{ Flush() error }); ok {
		_ = fl.Flush()
	}
	if out.String() == "" {
		t.Fatal("every byte of the stream was discarded; the client would see an empty stream")
	}
	if !strings.Contains(out.String(), "upstream-mod") {
		t.Fatalf("the held tail was dropped: %q", out.String())
	}
}
