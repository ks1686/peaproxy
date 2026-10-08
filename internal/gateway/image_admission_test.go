package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// maxInFlight is the documented bound on concurrent upstream calls per account.
// Images took no admission slot, so a client could send unlimited image
// requests past the limit while every other path was bounded -- and images are
// the most expensive kind of call here, being the least token-bound.
//
// The test sends more concurrent image requests than the configured limit and
// asserts the upstream saw no more than the limit at once. It is written to
// fail before the change and pass after it.

func TestImagesTakeAnAdmissionSlotSoMaxInFlightBoundsThem(t *testing.T) {
	// The limit is per account, so the measurement is too. Two accounts at
	// maxInFlight=1 legitimately show two calls at once -- one each -- and
	// asserting a global peak of one would fail on correct behaviour.
	var mu sync.Mutex
	perAccount := map[string]int{}
	peakPerAccount := map[string]int{}
	release := make(chan struct{})
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "imagen-3"}}})
		case "/v1/images/generations":
			host := r.Host
			mu.Lock()
			perAccount[host]++
			n := perAccount[host]
			if n > peakPerAccount[host] {
				peakPerAccount[host] = n
			}
			mu.Unlock()
			// Hold the slot open so the test measures concurrency rather than
			// how fast a fake provider answers.
			<-release
			mu.Lock()
			perAccount[host]--
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": "http://example.invalid/i.png"}}})
		default:
			http.NotFound(w, r)
		}
	}
	gw := twoAccountGateway(t, upstream, upstream)
	// SetConfig, not a direct cfg write: it is what publishes the limit to the
	// admission gate. A test that mutates gw.cfg and calls rebuild() looks like
	// it configured the limit and quietly leaves the gate unbounded.
	cfg := gw.Config()
	cfg.RequestEngine.MaxInFlight = 1
	gw.SetConfig(cfg)
	gw.Refresh(context.Background())

	const callers = 6
	done := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			_, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"imagen-3","prompt":"a cat"}`))
			done <- err
		}()
	}
	// Let the admitted callers reach the upstream before unblocking them.
	time.Sleep(300 * time.Millisecond)
	close(release)
	for i := 0; i < callers; i++ {
		<-done
	}

	mu.Lock()
	defer mu.Unlock()
	for host, got := range peakPerAccount {
		if got > 1 {
			t.Fatalf("%d image calls were in flight at once against %s with maxInFlight=1", got, host)
		}
	}
	if len(peakPerAccount) == 0 {
		t.Fatal("no image call reached any upstream, so nothing was measured")
	}
}

// Images are not billed per token, so a token-derived spend hold would be
// meaningless for them. The slot is the part that matters, and taking it must
// not make an image call fail against a ceiling it was never priced by.
func TestAnImageCallIsNotRefusedByASpendCeilingItCannotBePricedAgainst(t *testing.T) {
	var hits atomic.Int32
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "imagen-3"}}})
		case "/v1/images/generations":
			hits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"url": "http://example.invalid/i.png"}}})
		default:
			http.NotFound(w, r)
		}
	}
	gw := twoAccountGateway(t, upstream, upstream)
	// A ceiling with nothing priced against it: chat would refuse here.
	gw.cfg.Optimization.SpendCeilingUSD = 0.000001
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	if _, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"imagen-3","prompt":"a cat"}`)); err != nil {
		t.Fatalf("an image call was refused by a token-priced ceiling: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatal("the image call never reached the upstream")
	}
}

// A refusal has to give its slot back. If an image call that is skipped for
// capacity leaked the slot, the account would look permanently busy.
func TestARefusedImageCallDoesNotLeakAnAdmissionSlot(t *testing.T) {
	upstream := func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "imagen-3"}}})
		default:
			http.NotFound(w, r)
		}
	}
	gw := twoAccountGateway(t, upstream, upstream)
	cfg := gw.Config()
	cfg.RequestEngine.MaxInFlight = 1
	gw.SetConfig(cfg)
	gw.Refresh(context.Background())

	// Every call is refused: no /v1/images/generations handler exists.
	for i := 0; i < 5; i++ {
		if _, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"imagen-3","prompt":"a cat"}`)); err == nil {
			t.Fatal("an image call to an upstream that 404s succeeded")
		}
	}
	// If slots leaked, the gate would now refuse for capacity reasons and the
	// error would stop naming the real cause.
	_, _, err := gw.GenerateImage(context.Background(), []byte(`{"model":"imagen-3","prompt":"a cat"}`))
	if err == nil {
		t.Fatal("the sixth image call succeeded against an upstream that serves no images")
	}
	if admissionRejection(err) {
		t.Fatalf("the failure is capacity, so slots leaked across refusals: %v", err)
	}
}
