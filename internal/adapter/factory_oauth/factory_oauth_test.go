package factory_oauth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
)

func TestFactoryOAuthIsNotYet(t *testing.T) {
	adp, err := New(adapter.Options{ID: "factory-oauth"})
	if err != nil {
		t.Fatal(err)
	}
	auth, ok := adp.(adapter.Authenticator)
	if !ok {
		t.Fatal("must implement Authenticator so CLI can report not-yet")
	}
	_, err = auth.AuthStart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not yet") {
		t.Fatalf("want not yet, got %v", err)
	}
	if !strings.Contains(err.Error(), "Droid") && !strings.Contains(err.Error(), "droid") {
		t.Fatalf("stub should point at Droid as a PeaProxy client: %v", err)
	}
	if _, err := adp.ListModels(context.Background()); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("list: %v", err)
	}
	if !adp.Capabilities().OAuth || adp.Capabilities().Chat {
		t.Fatalf("%#v", adp.Capabilities())
	}
}
