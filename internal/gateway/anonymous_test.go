package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/router"
)

// A provider with no credentials is somebody else's machine. Sending a prompt
// there is publishing it, and the setting that says whether that is allowed was
// read by nothing: allowAnonymousProviders existed in the config and was checked
// nowhere.
func TestAutomaticRoutesRefuseAnAnonymousProviderByDefault(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	// Strip the credentials so the account is anonymous.
	for i := range gw.cfg.Providers {
		gw.cfg.Providers[i].APIKey = ""
		gw.cfg.Providers[i].APIKeyEnv = ""
	}
	gw.cfg.AutomaticRoutes.Enabled = true
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"`+router.RouteAuto+`","messages":[{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("a prompt went to a provider with no account attached")
	} else if !strings.Contains(err.Error(), "anonymous") {
		t.Fatalf("the refusal does not say what was refused: %v", err)
	}
	if hits != 0 {
		t.Fatalf("the anonymous upstream was called %d times", hits)
	}
}

// The opt-in is what the setting is for: a user who knows their anonymous
// endpoint is theirs, and says so, gets it back.
func TestAnonymousProvidersAreAllowedWhenTheUserSaysSo(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	for i := range gw.cfg.Providers {
		gw.cfg.Providers[i].APIKey = ""
		gw.cfg.Providers[i].APIKeyEnv = ""
	}
	gw.cfg.AutomaticRoutes.Enabled = true
	gw.cfg.Optimization.AllowAnonymousProviders = boolp(true)
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"`+router.RouteAuto+`","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("an explicitly permitted anonymous provider was refused: %v", err)
	}
	if hits == 0 {
		t.Fatal("the permitted provider was never called")
	}
}

// A local account has no key and never needed one: it is this machine. Refusing
// it would break every local-only setup to enforce a rule about somebody else's
// infrastructure.
func TestLocalProvidersAreNotTreatedAsAnonymous(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	for i := range gw.cfg.Providers {
		gw.cfg.Providers[i].APIKey = ""
		gw.cfg.Providers[i].APIKeyEnv = ""
		gw.cfg.Providers[i].Tier = "local"
	}
	gw.cfg.AutomaticRoutes.Enabled = true
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"`+router.RouteAuto+`","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("a local provider was refused as anonymous: %v", err)
	}
}

// The guard must not fire for a provider that does have credentials, which is
// every ordinary setup. This is the whole regression risk of the feature.
func TestCredentialedProvidersAreUnaffected(t *testing.T) {
	hits := 0
	gw := twoAccountGateway(t, countOK(&hits, "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	for i := range gw.cfg.Providers {
		gw.cfg.Providers[i].APIKey = "sk-test"
	}
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	if _, _, err := gw.Chat(context.Background(), []byte(`{"model":"`+router.RouteAuto+`","messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatalf("an ordinary authenticated setup was refused: %v", err)
	}
	if hits == 0 {
		t.Fatal("the provider was never called")
	}
}
