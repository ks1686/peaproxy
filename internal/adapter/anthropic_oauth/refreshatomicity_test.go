package anthropic_oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/oauth"
)

// tokenServer answers the refresh endpoint, counting hits and rotating the
// refresh token on every call, the way a provider that revokes on reuse does.
type tokenServer struct {
	srv   *httptest.Server
	hits  atomic.Int32
	delay time.Duration
	// gate, when set, blocks each refresh until it is closed.
	gate chan struct{}
}

func newTokenServer(t *testing.T) *tokenServer {
	ts := &tokenServer{}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var form struct {
			GrantType string `json:"grant_type"`
		}
		_ = json.Unmarshal(body, &form)
		if form.GrantType == "authorization_code" {
			// A login: a token of its own, unrelated to any refresh.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-login", "refresh_token": "rt-login", "expires_in": 3600,
			})
			return
		}
		n := ts.hits.Add(1)
		if ts.gate != nil {
			<-ts.gate
		}
		if ts.delay > 0 {
			time.Sleep(ts.delay)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-" + string(rune('0'+n)),
			"refresh_token": "rt-" + string(rune('0'+n)),
			"expires_in":    3600,
		})
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func expired(rt string) oauth.Token {
	return oauth.Token{AccessToken: "at-old", RefreshToken: rt, ExpiresAt: time.Now().Add(-time.Hour)}
}

func persistedToken(t *testing.T, a *Adapter) oauth.Token {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token
}

// #76 through the real adapter: once a refresh has committed, the next caller
// must reuse the token that flight stored rather than present the one it spent.
func TestEnsureTokenReusesTheTokenACommitStored(t *testing.T) {
	ts := newTokenServer(t)
	a := testAdapter(t, ts.srv.URL)
	a.token = expired("rt-0")

	if err := a.ensureToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ts.hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", ts.hits.Load())
	}
	first := persistedToken(t, a)
	if first.RefreshToken != "rt-1" {
		t.Fatalf("refresh token = %q, want the rotated one", first.RefreshToken)
	}

	for i := 0; i < 3; i++ {
		if err := a.ensureToken(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if ts.hits.Load() != 1 {
		t.Fatalf("hits = %d: a stored-but-fresh token was refreshed again", ts.hits.Load())
	}
	if got := persistedToken(t, a); got.RefreshToken != "rt-1" {
		t.Fatalf("refresh token = %q, want rt-1 left alone", got.RefreshToken)
	}
}

// #76 again, this time on the flight itself: many concurrent callers must
// produce exactly one provider call.
func TestEnsureTokenConcurrentCallersHitTheProviderOnce(t *testing.T) {
	ts := newTokenServer(t)
	ts.delay = 20 * time.Millisecond
	a := testAdapter(t, ts.srv.URL)
	a.token = expired("rt-0")

	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- a.ensureToken(context.Background())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if ts.hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", ts.hits.Load())
	}
}

// #69 through the real adapter: the caller goes away after the provider answered,
// and the rotated token is still kept and persisted.
func TestEnsureTokenKeepsRotatedTokenWhenCallerGoesAway(t *testing.T) {
	ts := newTokenServer(t)
	a := testAdapter(t, ts.srv.URL)
	a.token = expired("rt-0")

	var saved []oauth.Token
	var smu sync.Mutex
	a.persist = func(tok oauth.Token) error {
		smu.Lock()
		saved = append(saved, tok)
		smu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.ensureToken(ctx) }()

	// Let the refresh reach the provider, then pull the request out from under
	// it. The provider still answers.
	for ts.hits.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()

	if err := <-done; err != nil && ctx.Err() == nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := persistedToken(t, a).RefreshToken; got != "rt-1" {
		t.Fatalf("rotated token lost when the caller went away: %q", got)
	}
	smu.Lock()
	defer smu.Unlock()
	if len(saved) != 1 || saved[0].RefreshToken != "rt-1" {
		t.Fatalf("rotated token never persisted: %+v", saved)
	}
}

// #68 through the real adapter: a login that lands while a refresh is in flight
// must be what ends up stored and persisted.
func TestLoginDuringRefreshWinsOnDisk(t *testing.T) {
	ts := newTokenServer(t)
	ts.gate = make(chan struct{})
	a := testAdapter(t, ts.srv.URL)
	a.token = expired("rt-0")

	var mu sync.Mutex
	var onDisk []oauth.Token
	a.persist = func(tok oauth.Token) error {
		mu.Lock()
		onDisk = append(onDisk, tok)
		mu.Unlock()
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- a.ensureToken(context.Background()) }()
	for ts.hits.Load() == 0 {
		time.Sleep(time.Millisecond)
	}

	// A real login lands mid-refresh and must not be overwritten by it.
	a.pending = &pendingAuth{state: "st", pkce: oauth.PKCE{Verifier: "ver"}}
	login := make(chan error, 1)
	go func() {
		login <- a.AuthComplete(context.Background(), adapter.AuthSession{State: "st"}, "code-login")
	}()
	time.Sleep(20 * time.Millisecond)
	close(ts.gate)

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-login; err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(onDisk) == 0 {
		t.Fatal("nothing was persisted")
	}
	if last := onDisk[len(onDisk)-1]; last.RefreshToken != "rt-login" {
		t.Fatalf("disk ends on %q, want the login's token: %+v", last.RefreshToken, onDisk)
	}
	if got := persistedToken(t, a).RefreshToken; got != "rt-login" {
		t.Fatalf("memory holds %q, want the login's token", got)
	}
}
