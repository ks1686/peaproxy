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

func TestFactoryOAuthStubMethodsAndDefaultID(t *testing.T) {
	adp, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if adp.ID() != Name {
		t.Fatalf("ID = %q", adp.ID())
	}
	auth := adp.(adapter.Authenticator)
	if err := auth.AuthComplete(context.Background(), adapter.AuthSession{}, "code"); err == nil || !strings.Contains(err.Error(), "not yet") {
		t.Fatalf("complete = %v", err)
	}
	if _, err := adp.Chat(context.Background(), adapter.ChatRequest{}); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("chat: %v", err)
	}
	if err := adp.ChatStream(context.Background(), adapter.ChatRequest{}, nil); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("stream: %v", err)
	}
	if err := adp.Validate(context.Background()); !errors.Is(err, adapter.ErrNotImplemented) {
		t.Fatalf("validate: %v", err)
	}
}
