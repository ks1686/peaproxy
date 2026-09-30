package oauth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// acct is the token state Ensure operates on, wired the way the adapters wire
// it: mu guards the token on the request path, commit serializes the two
// writers (a login and a refresh) against each other.
type acct struct {
	mu         sync.Mutex
	token      Token
	generation uint64
	commit     sync.Mutex
	persist    func(Token) error
}

func (a *acct) snapshot() (Token, uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token, a.generation
}

func (a *acct) store(tok Token) error {
	a.commit.Lock()
	defer a.commit.Unlock()
	a.mu.Lock()
	a.generation++
	a.token = tok
	persist := a.persist
	a.mu.Unlock()
	if persist == nil {
		return nil
	}
	return persist(tok)
}

// stale is expired, so Ensure must refresh it.
func stale(refresh string) Token {
	return Token{AccessToken: "old-access", RefreshToken: refresh, ExpiresAt: time.Now().Add(-time.Hour)}
}

func fresh(access, refresh string) Token {
	return Token{AccessToken: access, RefreshToken: refresh, ExpiresAt: time.Now().Add(time.Hour)}
}

// #76: a caller that snapshotted the old refresh token must not spend it after
// another caller already rotated it. Reusing a spent refresh token is what gets
// a provider grant revoked and logs the account out.
func TestEnsureDoesNotReuseSpentRefreshToken(t *testing.T) {
	var g RefreshGroup
	a := &acct{token: stale("rt-1")}

	var calls atomic.Int32
	refresh := func(_ context.Context, tok Token) (Token, error) {
		calls.Add(1)
		if tok.RefreshToken != "rt-1" {
			t.Errorf("refreshed with %q, want rt-1", tok.RefreshToken)
		}
		return fresh("access-2", "rt-2"), nil
	}

	// First caller refreshes and commits.
	if err := Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, refresh, nil, a.persist); err != nil {
		t.Fatal(err)
	}

	// Second caller snapshotted the OLD token before the first committed, and
	// only now reaches the flight.
	staleSnapshot, _ := a.snapshot()
	if staleSnapshot.RefreshToken != "rt-2" {
		t.Fatalf("precondition: snapshot should hold the new token, got %q", staleSnapshot.RefreshToken)
	}
	if err := Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, refresh, nil, a.persist); err != nil {
		t.Fatal(err)
	}

	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1: a second refresh spent an already-rotated token", calls.Load())
	}
	tok, _ := a.snapshot()
	if tok.RefreshToken != "rt-2" {
		t.Fatalf("token = %+v", tok)
	}
}

// #76: the window is between the flight finishing and the winner committing.
// A caller that arrives in it must not start a second refresh either, which is
// why the commit happens inside the flight.
func TestEnsureNoSecondRefreshDuringCommitWindow(t *testing.T) {
	var g RefreshGroup
	inPersist := make(chan struct{})
	release := make(chan struct{})
	a := &acct{token: stale("rt-1"), persist: func(tok Token) error {
		if tok.RefreshToken == "rt-2" {
			close(inPersist)
			<-release
		}
		return nil
	}}

	var calls atomic.Int32
	refresh := func(context.Context, Token) (Token, error) {
		calls.Add(1)
		return fresh("access-2", "rt-2"), nil
	}

	done := make(chan error, 1)
	go func() {
		done <- Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
			5*time.Minute, refresh, nil, a.persist)
	}()
	<-inPersist

	// The winner has refreshed but not finished persisting. A concurrent caller
	// must wait for that flight, not start its own.
	other := make(chan error, 1)
	go func() {
		other <- Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
			5*time.Minute, refresh, nil, a.persist)
	}()
	close(release)

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-other; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
}

// #69: a request that is cancelled after the provider rotated the refresh token
// must not lose it. The refresh runs on a context detached from the caller.
func TestEnsureSurvivesCallerCancellation(t *testing.T) {
	var g RefreshGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &acct{token: stale("rt-1")}

	var persisted []Token
	a.persist = func(tok Token) error {
		persisted = append(persisted, tok)
		return nil
	}

	// The provider answered 200 and rotated the token; only then does the
	// caller's request go away.
	refresh := func(context.Context, Token) (Token, error) {
		cancel()
		return fresh("access-2", "rt-2"), nil
	}
	if err := Ensure(ctx, &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, refresh, nil, a.persist); err != nil {
		t.Fatalf("a cancelled caller must not fail the refresh: %v", err)
	}
	tok, _ := a.snapshot()
	if tok.RefreshToken != "rt-2" {
		t.Fatalf("rotated token lost: %+v", tok)
	}
	if len(persisted) != 1 || persisted[0].RefreshToken != "rt-2" {
		t.Fatalf("rotated token never persisted: %+v", persisted)
	}
}

// The refresh context must be detached: the caller's cancelation may not reach
// an in-flight refresh request, and the refresh still needs a timeout of its own
// so it cannot hang forever once the client is gone.
func TestEnsureRefreshContextIsDetached(t *testing.T) {
	var g RefreshGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &acct{token: stale("rt-1"), persist: func(Token) error { return nil }}

	// Checked from inside the refresh, while it is still running: Do cancels the
	// detached context as soon as fn returns, so inspecting it afterwards would
	// only prove that Do tidies up after itself.
	refresh := func(c context.Context, _ Token) (Token, error) {
		cancel() // the caller goes away mid-refresh
		if err := c.Err(); err != nil {
			t.Errorf("the caller's cancelation reached the refresh context: %v", err)
		}
		if _, ok := c.Deadline(); !ok {
			t.Error("the detached refresh context should still have its own timeout")
		}
		return fresh("access-2", "rt-2"), nil
	}
	if err := Ensure(ctx, &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, refresh, nil, a.persist); err != nil {
		t.Fatal(err)
	}
}

// A caller that arrives already cancelled must not start provider work.
func TestEnsureAlreadyCancelledCallerDoesNotRefresh(t *testing.T) {
	var g RefreshGroup
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &acct{token: stale("rt-1")}
	called := false
	err := Ensure(ctx, &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, func(context.Context, Token) (Token, error) {
			called = true
			return fresh("access-2", "rt-2"), nil
		}, nil, a.persist)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("refreshed for a caller that was already gone")
	}
}

// #68: a login that lands while a refresh is persisting must win on disk. The
// generation check and the write happen under the same lock.
func TestEnsurePersistCannotOverwriteNewerLogin(t *testing.T) {
	var g RefreshGroup
	a := &acct{token: stale("rt-1")}

	var mu sync.Mutex
	var onDisk []Token
	a.persist = func(tok Token) error {
		mu.Lock()
		onDisk = append(onDisk, tok)
		mu.Unlock()
		return nil
	}

	// The refresh's persist is held open while a login lands.
	held := make(chan struct{})
	release := make(chan struct{})
	a.persist = func(tok Token) error {
		mu.Lock()
		onDisk = append(onDisk, tok)
		mu.Unlock()
		if tok.RefreshToken == "rt-2" {
			close(held)
			<-release
		}
		return nil
	}

	done := make(chan error, 1)
	go func() {
		done <- Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
			5*time.Minute, func(context.Context, Token) (Token, error) {
				return fresh("access-2", "rt-2"), nil
			}, nil, a.persist)
	}()
	<-held

	// The login blocks on the commit lock rather than racing the write.
	login := make(chan error, 1)
	go func() { login <- a.store(fresh("access-login", "rt-login")) }()

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-login; err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(onDisk) == 0 {
		t.Fatal("nothing persisted")
	}
	// Whatever the interleaving, the last write on disk must be the login's
	// token: a stale refresh token left behind means the next start uses a
	// token the provider has already rotated.
	last := onDisk[len(onDisk)-1]
	if last.RefreshToken != "rt-login" {
		t.Fatalf("disk holds %q, want the login's token: %+v", last.RefreshToken, onDisk)
	}
	tok, _ := a.snapshot()
	if tok.RefreshToken != "rt-login" {
		t.Fatalf("memory holds %q, want the login's token", tok.RefreshToken)
	}
}

// A login that lands before the refresh even starts wins too: the refresh must
// not resurrect the token the login replaced.
func TestEnsureRefreshAfterLoginKeepsLogin(t *testing.T) {
	var g RefreshGroup
	a := &acct{token: stale("rt-1")}
	if err := a.store(fresh("access-login", "rt-login")); err != nil {
		t.Fatal(err)
	}
	called := false
	err := Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, func(context.Context, Token) (Token, error) {
			called = true
			return fresh("access-2", "rt-2"), nil
		}, nil, a.persist)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("refreshed a token a fresh login had already replaced")
	}
}

// The fixup hook carries the old token's non-secret identity forward, which is
// what the adapters used to do by hand after Do returned.
func TestEnsureAppliesFixup(t *testing.T) {
	var g RefreshGroup
	a := &acct{token: Token{AccessToken: "old", RefreshToken: "rt-1",
		ExpiresAt: time.Now().Add(-time.Hour),
		AccountID: "acct-9", Email: "a@example.com",
		Extra: map[string]string{"github_token": "gho-x"},
	}}
	err := Ensure(context.Background(), &g, "k", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, func(context.Context, Token) (Token, error) {
			return Token{AccessToken: "new", RefreshToken: "rt-2", ExpiresAt: time.Now().Add(time.Hour)}, nil
		},
		func(old, next Token) Token {
			next = next.KeepExtra(old)
			if next.AccountID == "" {
				next.AccountID = old.AccountID
			}
			if next.Email == "" {
				next.Email = old.Email
			}
			return next
		}, a.persist)
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := a.snapshot()
	if tok.AccountID != "acct-9" || tok.Email != "a@example.com" {
		t.Fatalf("identity not carried forward: %+v", tok)
	}
	if tok.ExtraGet("github_token") != "gho-x" {
		t.Fatalf("extra not carried forward: %+v", tok.Extra)
	}
}

// A refresh that fails leaves the old token alone.
func TestEnsureKeepsTokenWhenRefreshFails(t *testing.T) {
	var g RefreshGroup
	a := &acct{token: stale("rt-1")}
	boom := errors.New("provider said no")
	err := Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, func(context.Context, Token) (Token, error) {
			return Token{}, boom
		}, nil, a.persist)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	tok, _ := a.snapshot()
	if tok.RefreshToken != "rt-1" {
		t.Fatalf("token mutated by a failed refresh: %+v", tok)
	}
}

// A persist failure must not roll the token back: the old refresh token is
// already spent, so keeping it would lock the account out.
func TestEnsureKeepsTokenWhenPersistFails(t *testing.T) {
	var g RefreshGroup
	a := &acct{token: stale("rt-1"), persist: func(Token) error { return errors.New("disk full") }}
	err := Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
		5*time.Minute, func(context.Context, Token) (Token, error) {
			return fresh("access-2", "rt-2"), nil
		}, nil, a.persist)
	if err == nil {
		t.Fatal("persist failure should be reported")
	}
	tok, _ := a.snapshot()
	if tok.RefreshToken != "rt-2" {
		t.Fatalf("rolled back to a spent token: %+v", tok)
	}
}

// Concurrent callers all end up on the same token and the provider is hit once.
func TestEnsureConcurrentCallersShareOneRefresh(t *testing.T) {
	var g RefreshGroup
	var calls atomic.Int32
	var persisted atomic.Int32
	a := &acct{token: stale("rt-1"), persist: func(Token) error {
		persisted.Add(1)
		return nil
	}}
	refresh := func(context.Context, Token) (Token, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return fresh("access-2", "rt-2"), nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Ensure(context.Background(), &g, "acct", &a.commit, &a.mu, &a.token, &a.generation,
				5*time.Minute, refresh, nil, a.persist)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
	if persisted.Load() != 1 {
		t.Fatalf("persists = %d, want 1", persisted.Load())
	}
}
