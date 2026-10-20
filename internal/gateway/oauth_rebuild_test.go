package gateway

import (
	"context"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/oauth"
)

func liveOAuthToken(g *Gateway, id string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, in := range g.inst {
		if in.Provider.ID == id && in.Provider.OAuth != nil {
			return in.Provider.OAuth.AccessToken
		}
	}
	return ""
}

// A refresh that started on an adapter replaced by a rebuild commits the new
// token to that old adapter and to the config, but the rebuilt adapter was
// made from the config as it was, holding the refresh token the provider has
// just rotated away. Saving the refresh has to reach the live adapter.
func TestARefreshSavedAfterARebuildReachesTheLiveAdapter(t *testing.T) {
	gw, _, hooks := newSavedGateway(t, nil, acct("a", "old"))
	stale := hooks.get("a")
	if stale == nil {
		t.Fatal("no persist hook for account a")
	}
	if err := gw.AddProvider(context.Background(), config.Provider{ID: "b", Adapter: "native", Tier: "paid"}); err != nil {
		t.Fatal(err)
	}
	if got := liveOAuthToken(gw, "a"); got != "old" {
		t.Fatalf("setup: live token after rebuild = %q, want old", got)
	}
	if err := stale(oauth.Token{AccessToken: "rotated", RefreshToken: "r2"}); err != nil {
		t.Fatal(err)
	}
	if got := liveOAuthToken(gw, "a"); got != "rotated" {
		t.Fatalf("live adapter token = %q after the refresh was saved, want rotated (it holds the spent token)", got)
	}
}
