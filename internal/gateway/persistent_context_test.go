package gateway

import (
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/contextstore"
)

// optimization.persistentContext is documented as keeping stored request
// artifacts on disk between runs. It does not, and this file exists to hold
// that in place.
//
// The test asserts the absence on purpose, because the failure being prevented
// is a user believing the setting works. An implementation that made it pass
// would break it, and that is the correct outcome: when disk persistence ships,
// this test is deleted in the same change that makes the documentation true.
// Until then it stops the belief from hardening, and internal/config's
// inert-settings report tells the same user at runtime.
func TestPersistentContextDoesNotWriteToDisk(t *testing.T) {
	yes := true
	cfg := config.Default()
	cfg.Optimization.PersistentContext = &yes

	store := newArtifactStore(&cfg)
	if store == nil {
		t.Fatal("no artifact store: the setting is inert because persistence is absent, not because the store is missing")
	}

	if err := store.Put("sess-1", contextstore.Artifact{
		Key:      "search",
		Kind:     "passages",
		Source:   "pea_search",
		Body:     []byte("passage"),
		Portable: true,
	}); err != nil {
		t.Fatalf("storing an artifact: %v", err)
	}

	// A fresh store is what a restart looks like. If anything were persisted,
	// the artifact would still be here. That it is not is the whole claim.
	afterRestart := newArtifactStore(&cfg)
	if _, ok := afterRestart.Get("sess-1", "search"); ok {
		t.Fatal("the artifact survived a new store: persistentContext now persists to disk, " +
			"so this test and docs/V3.md's known limitations are both stale")
	}
}

// The default build must behave the same way, or the setting would be doing
// something nobody asked for.
func TestTheArtifactStoreIsMemoryOnlyByDefault(t *testing.T) {
	cfg := config.Default()
	if cfg.PersistentContextEnabled() {
		t.Fatal("persistence is on by default; artifacts would be written to disk without being asked for")
	}
	store := newArtifactStore(&cfg)
	if store == nil {
		t.Fatal("no artifact store for the default config")
	}
}
