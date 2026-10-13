package oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestLoopbackCapturesCodeAndState(t *testing.T) {
	lb, err := StartLoopback("127.0.0.1:0", "/callback")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lb.Close() })

	url := fmt.Sprintf("http://%s/callback?code=auth-code-1&state=xyz", lb.Addr())
	go func() {
		resp, err := http.Get(url)
		if err != nil {
			t.Errorf("callback get: %v", err)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := lb.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != "auth-code-1" || res.State != "xyz" {
		t.Fatalf("%#v", res)
	}
}

// A request to the callback path that does not carry the pending login's
// state must not be taken as the result. Any local process, or any web page
// via a plain GET, can reach a fixed loopback port; before this fix the first
// such request filled the one-slot channel, so the real browser redirect was
// dropped and the login failed with a state mismatch.
func TestLoopbackIgnoresCallbackWithWrongState(t *testing.T) {
	lb, err := StartLoopback("127.0.0.1:0", "/callback")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lb.Close() })
	lb.ExpectState("good-state")

	get := func(q string) int {
		resp, err := http.Get(fmt.Sprintf("http://%s/callback?%s", lb.Addr(), q))
		if err != nil {
			t.Fatalf("callback get: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := get("code=forged&state=bad"); code != http.StatusBadRequest {
		t.Fatalf("forged callback status = %d, want 400", code)
	}
	if code := get("error=access_denied"); code != http.StatusBadRequest {
		t.Fatalf("stateless error callback status = %d, want 400", code)
	}
	if code := get("code=real&state=good-state"); code != http.StatusOK {
		t.Fatalf("real callback status = %d, want 200", code)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := lb.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != "real" || res.State != "good-state" {
		t.Fatalf("got %#v, want the real callback", res)
	}
}
