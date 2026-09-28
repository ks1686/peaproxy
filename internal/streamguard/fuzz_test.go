package streamguard

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func FuzzSSE(f *testing.F) {
	f.Add([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n"))
	f.Add([]byte("data: {\"error\":{\"message\":\"x\"}}\n"))
	f.Add([]byte(": ping\n\ndata: {\"type\":\"message_start\"}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		g := New(io.Discard, 4096, time.Minute)
		buf := data
		for len(buf) > 0 {
			n := len(buf)
			if n > 7 {
				n = 7
			}
			_, _ = g.Write(buf[:n])
			buf = buf[n:]
			if g.Committed() {
				_, _ = g.Write(buf)
				return
			}
		}
		var dst bytes.Buffer
		g2 := New(&dst, 4096, time.Minute)
		_, _ = g2.Write(data)
		if g2.Committed() && dst.Len() == 0 && len(data) > 0 {
			t.Fatal("committed without forwarding")
		}
	})
}
