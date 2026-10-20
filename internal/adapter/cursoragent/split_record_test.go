package cursoragent

import (
	"bytes"
	"strings"
	"testing"
)

// os/exec copies the CLI's stdout in reads of arbitrary size, so a stream-json
// record can arrive split across two writes. Both halves used to fail to parse
// and the text was dropped.
func TestSSEWriterKeepsARecordSplitAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	s := &sseWriter{w: &out, model: "m"}
	rec := `{"type":"assistant","text":"hello world"}` + "\n"
	cut := len(rec) / 2
	_, _ = s.Write([]byte(rec[:cut]))
	_, _ = s.Write([]byte(rec[cut:]))
	if err := s.finish(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hello world") {
		t.Fatalf("split record lost:\n%s", out.String())
	}
}
