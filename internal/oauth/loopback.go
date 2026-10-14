package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Result is a loopback OAuth callback.
type Result struct {
	Code  string
	State string
	Error string
}

// Loopback is a one-shot localhost callback server.
type Loopback struct {
	ln     net.Listener
	srv    *http.Server
	path   string
	ch     chan Result
	mu     sync.Mutex
	closed bool
	state  string
}

// ExpectState makes the callback accept only a request carrying state, the
// value sent in the authorize URL. Any other request to the callback path is
// answered 400 and does not count as the result, so a stray or forged hit on
// the loopback port cannot take the place of the real browser redirect.
// Call it before handing out the login URL.
func (l *Loopback) ExpectState(state string) {
	l.mu.Lock()
	l.state = state
	l.mu.Unlock()
}

// StartLoopback listens on addr (host:port) and handles GET path.
func StartLoopback(addr, path string) (*Loopback, error) {
	if path == "" {
		path = "/callback"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("oauth loopback: %w", err)
	}
	lb := &Loopback{
		ln:   ln,
		path: path,
		ch:   make(chan Result, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path, lb.handle)
	mux.HandleFunc("/success", lb.handleSuccess)
	lb.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = lb.srv.Serve(ln) }()
	return lb, nil
}

// Addr is host:port the server bound.
func (l *Loopback) Addr() string {
	if l == nil || l.ln == nil {
		return ""
	}
	return l.ln.Addr().String()
}

// Wait blocks until a callback or ctx is done.
func (l *Loopback) Wait(ctx context.Context) (Result, error) {
	select {
	case res := <-l.ch:
		if res.Error != "" {
			return res, fmt.Errorf("oauth callback: %s", res.Error)
		}
		return res, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Close shuts the listener.
func (l *Loopback) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if l.srv != nil {
		_ = l.srv.Shutdown(ctx)
	}
	if l.ln != nil {
		_ = l.ln.Close()
	}
	return nil
}

func (l *Loopback) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	res := Result{
		Code:  q.Get("code"),
		State: q.Get("state"),
		Error: q.Get("error"),
	}
	l.mu.Lock()
	want := l.state
	l.mu.Unlock()
	if want != "" && CheckState(want, res.State) != nil {
		http.Error(w, "oauth callback state mismatch", http.StatusBadRequest)
		return
	}
	// A hit with neither a code nor an OAuth error is not a login result.
	// Delivering it used to fill the one-slot channel with an empty code,
	// so Wait returned success and the real redirect was dropped.
	if res.Code == "" && res.Error == "" {
		http.Error(w, "oauth callback missing code", http.StatusBadRequest)
		return
	}
	select {
	case l.ch <- res:
	default:
	}
	if res.Error != "" {
		http.Error(w, "oauth callback missing code", http.StatusBadRequest)
		return
	}
	l.handleSuccess(w, r)
}

func (l *Loopback) handleSuccess(w http.ResponseWriter, r *http.Request) {
	_ = r
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><body><p>PeaProxy login complete. You can close this tab.</p></body></html>`))
}
