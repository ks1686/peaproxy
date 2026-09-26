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
		defer resp.Body.Close()
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
