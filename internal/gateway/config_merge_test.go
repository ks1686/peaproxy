package gateway

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/secretstore"
)

// persistingNative calls its PersistOAuth callback from the first ListModels,
// the way OAuth adapters refresh a token during Refresh.
type persistingNative struct {
	slowNative
	persist func(oauth.Token) error
	once    sync.Once
}

func (a *persistingNative) ListModels(ctx context.Context) ([]catalog.Model, error) {
	if a.persist != nil {
		a.once.Do(func() { _ = a.persist(oauth.Token{AccessToken: "refreshed"}) })
	}
	return a.slowNative.ListModels(ctx)
}

type persistHooks struct {
	mu   sync.Mutex
	byID map[string]func(oauth.Token) error
}

func (h *persistHooks) get(id string) func(oauth.Token) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.byID[id]
}

func mergeRegistry() (*adapter.Registry, *persistHooks) {
	hooks := &persistHooks{byID: map[string]func(oauth.Token) error{}}
	reg := adapter.NewRegistry()
	reg.Register("native", func(opts adapter.Options) (adapter.Adapter, error) {
		if opts.PersistOAuth != nil {
			hooks.mu.Lock()
			hooks.byID[opts.ID] = opts.PersistOAuth
			hooks.mu.Unlock()
		}
		return &slowNative{id: opts.ID}, nil
	})
	reg.Register("persisting", func(opts adapter.Options) (adapter.Adapter, error) {
		return &persistingNative{slowNative: slowNative{id: opts.ID}, persist: opts.PersistOAuth}, nil
	})
	return reg, hooks
}

func acct(id, token string) config.Provider {
	return config.Provider{ID: id, Adapter: "native", Tier: "paid", OAuth: &config.OAuthToken{AccessToken: token}}
}

// newSavedGateway writes providers to a fresh config file and opens a gateway
// on what Load returns, like serve does. overlay runs on the loaded config
// before New (flags/env).
func newSavedGateway(t *testing.T, overlay func(*config.Config), providers ...config.Provider) (*Gateway, string, *persistHooks) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Providers = providers
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if overlay != nil {
		overlay(&loaded)
	}
	reg, hooks := mergeRegistry()
	gw, err := New(loaded, path, reg)
	if err != nil {
		t.Fatal(err)
	}
	return gw, path, hooks
}

// externalEdit is another process (peaproxy auth login, catalog …) editing the file.
func externalEdit(t *testing.T, path string, edit func(*config.Config)) {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	edit(&cfg)
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
}

func loadDisk(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func tokens(ps []config.Provider) map[string]string {
	out := map[string]string{}
	for _, p := range ps {
		tok := ""
		if p.OAuth != nil {
			tok = p.OAuth.AccessToken
		}
		out[p.ID] = tok
	}
	return out
}

func diskPort(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "port:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "port:"))
		}
	}
	return ""
}

func TestSaveOAuthKeepsAccountAddedByAnotherProcess(t *testing.T) {
	gw, path, _ := newSavedGateway(t, nil, acct("a", "tok-a"))
	externalEdit(t, path, func(c *config.Config) { c.Providers = append(c.Providers, acct("b", "tok-b")) })

	if err := gw.SaveOAuth("a", oauth.Token{AccessToken: "tok-a2"}); err != nil {
		t.Fatal(err)
	}

	if got, want := tokens(loadDisk(t, path).Providers), map[string]string{"a": "tok-a2", "b": "tok-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disk %v, want %v", got, want)
	}
	store, err := config.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("b", secretstore.KindOAuth); err != nil {
		t.Fatalf("b's secret was pruned: %v", err)
	}
	if got := tokens(gw.Config().Providers); got["b"] != "tok-b" {
		t.Fatalf("gateway did not adopt b: %v", got)
	}
}

func TestSaveOAuthWinsOverUnchangedDisk(t *testing.T) {
	gw, path, _ := newSavedGateway(t, nil, acct("a", "tok-a"))
	if err := gw.SaveOAuth("a", oauth.Token{AccessToken: "tok-new"}); err != nil {
		t.Fatal(err)
	}
	if got := tokens(loadDisk(t, path).Providers)["a"]; got != "tok-new" {
		t.Fatalf("disk token %q, want tok-new", got)
	}
}

func TestRemoveProviderStillRemovesAfterExternalAdd(t *testing.T) {
	gw, path, _ := newSavedGateway(t, nil, acct("a", "tok-a"))
	externalEdit(t, path, func(c *config.Config) { c.Providers = append(c.Providers, acct("b", "tok-b")) })

	if err := gw.RemoveProvider(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if got, want := tokens(loadDisk(t, path).Providers), map[string]string{"b": "tok-b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("disk %v, want %v", got, want)
	}
	if got := tokens(gw.Config().Providers); !reflect.DeepEqual(got, map[string]string{"b": "tok-b"}) {
		t.Fatalf("gateway %v", got)
	}
}

func TestExternalReloginWinsWhenServerUntouched(t *testing.T) {
	gw, path, _ := newSavedGateway(t, nil, acct("a", "tok-a"))
	externalEdit(t, path, func(c *config.Config) { c.Providers[0].OAuth.AccessToken = "tok-relogin" })

	if err := gw.ToggleHide("model", "m", true); err != nil {
		t.Fatal(err)
	}
	if got := tokens(loadDisk(t, path).Providers)["a"]; got != "tok-relogin" {
		t.Fatalf("disk token %q, want tok-relogin", got)
	}
	if got := tokens(gw.Config().Providers)["a"]; got != "tok-relogin" {
		t.Fatalf("gateway token %q, want tok-relogin", got)
	}
}

func TestServerRefreshWinsOverExternalReloginConflict(t *testing.T) {
	gw, path, _ := newSavedGateway(t, nil, acct("a", "tok-a"))
	externalEdit(t, path, func(c *config.Config) { c.Providers[0].OAuth.AccessToken = "tok-relogin" })

	if err := gw.SaveOAuth("a", oauth.Token{AccessToken: "tok-server"}); err != nil {
		t.Fatal(err)
	}
	if got := tokens(loadDisk(t, path).Providers)["a"]; got != "tok-server" {
		t.Fatalf("disk token %q, want tok-server", got)
	}
}

func TestServerSaveDoesNotPersistEnvPortOverlay(t *testing.T) {
	gw, path, _ := newSavedGateway(t, func(c *config.Config) { c.Port = 9000 }, acct("a", "tok-a"))

	if err := gw.ToggleHide("model", "m", true); err != nil {
		t.Fatal(err)
	}
	if got := diskPort(t, path); got != "8317" {
		t.Fatalf("after first save port %s, want 8317", got)
	}
	if err := gw.SaveOAuth("a", oauth.Token{AccessToken: "tok-a2"}); err != nil {
		t.Fatal(err)
	}
	if got := diskPort(t, path); got != "8317" {
		t.Fatalf("after second save port %s, want 8317", got)
	}
	if gw.Config().Port != 9000 {
		t.Fatalf("running port %d, want 9000", gw.Config().Port)
	}
}

func TestMergeBaseAfterPersist(t *testing.T) {
	gw, path, _ := newSavedGateway(t, func(c *config.Config) { c.Port = 9000 }, acct("a", "tok-a"))
	if err := gw.ToggleHide("model", "x", true); err != nil {
		t.Fatal(err)
	}
	externalEdit(t, path, func(c *config.Config) {
		c.Providers = append(c.Providers, acct("b", "tok-b"))
		c.Hide.Models = []string{"ext"}
	})
	pinned := true
	if err := gw.SetCatalogOverlay("m", nil, &pinned); err != nil {
		t.Fatal(err)
	}

	disk := loadDisk(t, path)
	if got := tokens(disk.Providers); got["b"] != "tok-b" {
		t.Fatalf("disk lost b: %v", got)
	}
	if !reflect.DeepEqual(disk.Hide.Models, []string{"ext"}) {
		t.Fatalf("disk hide.models %v, want [ext]", disk.Hide.Models)
	}
	if !reflect.DeepEqual(disk.Catalog.Pin, []string{"m"}) {
		t.Fatalf("disk catalog.pin %v, want [m]", disk.Catalog.Pin)
	}
	if disk.Port != 8317 {
		t.Fatalf("disk port %d", disk.Port)
	}
	running := gw.Config()
	if !reflect.DeepEqual(running.Hide.Models, []string{"ext"}) {
		t.Fatalf("running hide.models %v, want adopted [ext]", running.Hide.Models)
	}
	if running.Port != 9000 {
		t.Fatalf("running port %d, want 9000", running.Port)
	}
	if got := tokens(running.Providers); got["b"] != "tok-b" {
		t.Fatalf("running providers %v", got)
	}
}

func TestExternalHideChangeSurvivesServerSave(t *testing.T) {
	gw, path, _ := newSavedGateway(t, nil, acct("a", "tok-a"))
	externalEdit(t, path, func(c *config.Config) { c.Hide.Providers = []string{"native"} })
	pinned := true
	if err := gw.SetCatalogOverlay("m", nil, &pinned); err != nil {
		t.Fatal(err)
	}
	disk := loadDisk(t, path)
	if !reflect.DeepEqual(disk.Hide.Providers, []string{"native"}) {
		t.Fatalf("disk hide.providers %v, want [native]", disk.Hide.Providers)
	}
	if !reflect.DeepEqual(disk.Catalog.Pin, []string{"m"}) {
		t.Fatalf("disk catalog.pin %v", disk.Catalog.Pin)
	}
}

func TestSaveOAuthDuringRefreshDoesNotDeadlock(t *testing.T) {
	gw, path, _ := newSavedGateway(t, nil, acct("a", "tok-a"))
	done := make(chan error, 1)
	go func() {
		done <- gw.AddProvider(context.Background(), config.Provider{ID: "b", Adapter: "persisting", Tier: "paid"})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AddProvider deadlocked on a PersistOAuth callback from ListModels")
	}
	if got := tokens(loadDisk(t, path).Providers)["b"]; got != "refreshed" {
		t.Fatalf("persisted token %q, want refreshed", got)
	}
}

func TestSaveOAuthPersistFailureDoesNotFailRefresh(t *testing.T) {
	gw, path, hooks := newSavedGateway(t, nil, acct("a", "tok-a"))
	persist := hooks.get("a")
	if persist == nil {
		t.Fatal("PersistOAuth not wired")
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	failures := func() int { return strings.Count(logs.String(), "config save failed") }

	garbage := []byte("providers: [\n")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"tok-1", "tok-2"} {
		if err := persist(oauth.Token{AccessToken: tok}); err != nil {
			t.Fatalf("PersistOAuth(%s) = %v, want nil", tok, err)
		}
	}
	if got := gw.Config().Providers[0].OAuth.AccessToken; got != "tok-2" {
		t.Fatalf("in-memory token %q, want tok-2", got)
	}
	if raw, _ := os.ReadFile(path); !bytes.Equal(raw, garbage) {
		t.Fatalf("unreadable config was overwritten: %q", raw)
	}
	if n := failures(); n != 1 {
		t.Fatalf("logged %d failures, want 1:\n%s", n, logs.String())
	}

	repaired := config.Default()
	repaired.Providers = []config.Provider{acct("a", "tok-a")}
	if err := config.Save(path, repaired); err != nil {
		t.Fatal(err)
	}
	if err := persist(oauth.Token{AccessToken: "tok-3"}); err != nil {
		t.Fatal(err)
	}
	if got := tokens(loadDisk(t, path).Providers)["a"]; got != "tok-3" {
		t.Fatalf("disk token %q, want tok-3", got)
	}

	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := persist(oauth.Token{AccessToken: "tok-4"}); err != nil {
		t.Fatal(err)
	}
	if n := failures(); n != 2 {
		t.Fatalf("logged %d failures after a success cleared the last error, want 2:\n%s", n, logs.String())
	}
}
