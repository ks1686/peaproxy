package server

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// A peer that sends headers and then stalls the body held its connection and
// handler goroutine forever: only ReadHeaderTimeout was set.
func TestAStalledRequestBodyIsCutOff(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old })

	s, _ := testServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.http.Handler, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1:8317\r\nContent-Type: application/json\r\nContent-Length: 10\r\n\r\n{")
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 512)
	start := time.Now()
	_, err = conn.Read(buf)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("the server was still waiting for the body after %v", time.Since(start))
	}
}
