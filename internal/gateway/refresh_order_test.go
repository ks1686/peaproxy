package gateway

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
)

type blockedCatalog struct {
	slowNative
	lists   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (a *blockedCatalog) ListModels(ctx context.Context) ([]catalog.Model, error) {
	id := "new"
	if a.lists.Add(1) == 1 {
		close(a.entered)
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		id = "old"
	}
	return []catalog.Model{{ID: id, AccountID: a.id, Provider: "native", Tier: catalog.TierPaid}}, nil
}

func TestRefreshDiscardsStaleSnapshot(t *testing.T) {
	for _, scenario := range []string{"newer_refresh", "rebuild", "remove_provider", "add_provider"} {
		t.Run(scenario, func(t *testing.T) {
			// Given an older refresh held inside ListModels.
			a := &blockedCatalog{slowNative: slowNative{id: "original"}, entered: make(chan struct{}), release: make(chan struct{})}
			reg := adapter.NewRegistry()
			reg.Register("native", func(opts adapter.Options) (adapter.Adapter, error) {
				if opts.ID == "original" {
					return a, nil
				}
				return &slowNative{id: opts.ID}, nil
			})
			gw, err := New(config.Config{Providers: []config.Provider{{ID: "original", Adapter: "native"}}}, "", reg)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				gw.Refresh(ctx)
			}()
			t.Cleanup(func() { cancel(); <-done })
			<-a.entered

			// When a newer refresh commits or the instance set is rebuilt.
			switch scenario {
			case "newer_refresh":
				gw.Refresh(ctx)
			case "rebuild":
				gw.mu.Lock()
				err = gw.rebuild()
				gw.mu.Unlock()
			case "remove_provider":
				err = gw.RemoveProvider(ctx, "original")
			case "add_provider":
				err = gw.AddProvider(ctx, config.Provider{ID: "added", Adapter: "native"})
			}
			if err != nil {
				t.Fatal(err)
			}
			want := gw.Models()
			close(a.release)
			<-done

			// Then the late older result cannot replace the accepted catalog.
			if got := gw.Models(); !reflect.DeepEqual(got, want) {
				t.Fatalf("stale refresh replaced catalog: got=%v want=%v", got, want)
			}
		})
	}
}
