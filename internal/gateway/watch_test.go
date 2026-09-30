package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/config"
)

// #52: changes another process writes to config.yaml must reach a running
// server. Nothing watches the file today, so an account added by `auth login`
// or a catalog edit from the CLI only appears at the next save -- which for an
// idle server is never.

func watchTestGateway(t *testing.T) (*Gateway, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.RequestLog = true
	cfg.Providers = []config.Provider{{ID: "a", Adapter: "openai_compat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-a"}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	g, err := New(cfg, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return g, path
}

func TestConfigWatcherAdoptsAnAccountAddedElsewhere(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	writeConfig(t, path, func(c *config.Config) {
		c.Providers = append(c.Providers, config.Provider{ID: "b", Adapter: "openai_compat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-b"})
	})
	waitFor(t, 3*time.Second, func() bool { return len(g.Config().Providers) == 2 }, "the added account was never adopted")

	got := g.Config()
	if got.Providers[1].ID != "b" {
		t.Fatalf("adopted %+v", got.Providers)
	}
	// Secrets are hydrated for the adopted account, not just its metadata.
	if got.Providers[1].APIKey == "" {
		t.Error("the adopted account has no api key; it would fail every request")
	}
}

func TestConfigWatcherAdoptsARemovedAccount(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	writeConfig(t, path, func(c *config.Config) { c.Providers = nil })
	waitFor(t, 3*time.Second, func() bool { return len(g.Config().Providers) == 0 }, "the removed account was never dropped")
}

func TestConfigWatcherAdoptsPerRequestSettings(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	// These are read per request but were merged into the file without ever
	// reaching the running server (#52).
	writeConfig(t, path, func(c *config.Config) {
		c.Failover.Policy = "adaptive"
		c.RequestEngine.MaxInFlight = 7
		c.AutomaticRoutes.Enabled = true
	})
	waitFor(t, 3*time.Second, func() bool {
		cfg := g.Config()
		return cfg.Failover.Policy == "adaptive" && cfg.RequestEngine.MaxInFlight == 7 && cfg.AutomaticRoutes.Enabled
	}, "a per-request setting was not adopted")
}

// The server's own writes must not bounce back through the watcher and reload
// what the server already has.
func TestConfigWatcherIgnoresTheServersOwnWrite(t *testing.T) {
	g, _ := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	before := g.Config()
	if err := g.SetRequestLog(true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * testWatchInterval)
	got := g.Config()
	if len(got.Providers) != len(before.Providers) {
		t.Fatalf("providers changed after our own save: %+v", got.Providers)
	}
	for _, p := range got.Providers {
		if p.APIKey == "" {
			t.Errorf("%s lost its key to its own save", p.ID)
		}
	}
}

// A reload must never run during a save: it would read a file another writer is
// replacing and then write its own idea of it straight back.
func TestConfigWatcherSkipsWhileASaveIsInProgress(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	g.saveMu.Lock()
	writeConfig(t, path, func(c *config.Config) {
		c.Providers = append(c.Providers, config.Provider{ID: "b", Adapter: "openai_compat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-b"})
	})
	// Several ticks with the save lock held.
	time.Sleep(20 * testWatchInterval)
	if n := len(g.Config().Providers); n != 1 {
		t.Fatalf("a reload ran during a save: %d providers", n)
	}
	g.saveMu.Unlock()
	waitFor(t, 3*time.Second, func() bool { return len(g.Config().Providers) == 2 }, "the change was never adopted after the save finished")
}

// A config the user is halfway through editing is not a reason to stop
// serving, and not a reason to log on every tick.
func TestConfigWatcherSurvivesABrokenConfig(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	if err := os.WriteFile(path, []byte("this: [is: not: yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * testWatchInterval)
	if len(g.Config().Providers) != 1 {
		t.Fatal("a broken config emptied the running server")
	}
	// Fixing it adopts the fix. Written from scratch rather than through
	// writeConfig, which would have to load the broken file first.
	good := config.Default()
	good.Providers = []config.Provider{
		{ID: "a", Adapter: "openai_compat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-a"},
		{ID: "b", Adapter: "openai_compat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-b"},
	}
	if err := config.Save(path, good); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return len(g.Config().Providers) == 2 }, "the repaired config was not adopted")
}

// The file going away is not a reason to panic or to stop.
func TestConfigWatcherSurvivesTheFileVanishing(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * testWatchInterval)
	if len(g.Config().Providers) != 1 {
		t.Fatal("losing the file emptied the running server")
	}
}

// A gateway with no config path has nothing to watch.
func TestConfigWatcherIsInertWithoutAPath(t *testing.T) {
	g, err := New(config.Default(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan struct{})
	go func() { g.watchConfig(ctx, testWatchInterval); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchConfig did not return for a gateway with no path")
	}
}

// After adopting, the next save must not undo what was adopted -- the base the
// merge compares against has moved too.
func TestAdoptedChangeSurvivesTheNextServerSave(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	writeConfig(t, path, func(c *config.Config) {
		c.Providers = append(c.Providers, config.Provider{ID: "b", Adapter: "openai_compat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-b"})
		c.Failover.Policy = "adaptive"
	})
	waitFor(t, 3*time.Second, func() bool {
		cfg := g.Config()
		return len(cfg.Providers) == 2 && cfg.Failover.Policy == "adaptive"
	}, "nothing was adopted")

	// Now the server saves for an unrelated reason.
	if err := g.SetRequestLog(true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "adaptive") {
		t.Errorf("the next save dropped the adopted policy:\n%s", raw)
	}
	if !strings.Contains(string(raw), "id: b") {
		t.Errorf("the next save dropped the adopted account:\n%s", raw)
	}
}

// Bind, Port, AllowNonLoopback, AdminToken and RequestLog are not adopted:
// a listener, a security posture and a file handle cannot change under a
// running server without surprising someone. They are adopted by the next
// start, which is the documented rule.
func TestSecurityAndListenerSettingsNeedARestart(t *testing.T) {
	g, path := watchTestGateway(t)
	startWatch(t, g, testWatchInterval)

	writeConfig(t, path, func(c *config.Config) {
		// A non-loopback bind needs allowNonLoopback and an admin token to be
		// valid at all, so a file carrying one has to carry both.
		c.AllowNonLoopback = true
		c.AdminToken = "rotated-token"
		c.RequestLog = false
		c.Bind = "0.0.0.0"
	})
	time.Sleep(10 * testWatchInterval)
	cfg := g.Config()
	if cfg.AllowNonLoopback {
		t.Error("allowNonLoopback was adopted live; the proxy would start accepting remote clients with no restart")
	}
	if cfg.RequestLog != true {
		t.Error("requestLog was turned off live; the log file handle was opened at start")
	}
	if cfg.Bind == "0.0.0.0" {
		t.Error("bind was adopted live; the listener is already bound")
	}
	if cfg.AdminToken != "" {
		t.Error("adminToken was adopted live; rotating a token should not change the auth requirement under live traffic")
	}
}

// testWatchInterval keeps the suite quick; the real interval is asserted
// separately and is a documented part of the contract.
const testWatchInterval = 5 * time.Millisecond

// startWatch runs the watcher for the life of the test and does not return
// until it has stopped.
//
// Cancelling is not enough: a tick already past its select is inside a
// config.Load or a SaveMerged, and the secret store writes into the config
// directory. Without waiting, t.TempDir cleanup races those writes and fails
// with "directory not empty" -- which is what happened on the -race job in CI,
// where the wider timing window makes it reliable.
func startWatch(t *testing.T, g *Gateway, interval time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.watchConfig(ctx, interval)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func writeConfig(t *testing.T, path string, edit func(*config.Config)) {
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

func waitFor(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// The real interval is part of the contract: an edit has to show up quickly
// enough that it reads as live, without polling so often it shows up in a
// profile.
func TestConfigWatchIntervalIsSensible(t *testing.T) {
	if configWatchInterval < 100*time.Millisecond {
		t.Errorf("polling every %v is too often", configWatchInterval)
	}
	if configWatchInterval > 5*time.Second {
		t.Errorf("polling every %v makes a file edit feel ignored", configWatchInterval)
	}
}
