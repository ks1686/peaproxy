package gateway

import (
	"context"
	"log"
	"os"
	"reflect"
	"time"

	"github.com/ks1686/peaproxy/internal/config"
)

// configWatchInterval is how often the config file is checked. Polling beats
// fsnotify here: a rename-replace writer -- which is what every atomic save is,
// including our own -- produces an event the watcher may never see on some
// platforms, and the fix for that is to watch the directory and compare, which is
// what a stat every couple of seconds already does. Two seconds is well under
// the time it takes to notice a wrong model list and well over the cost of one
// stat.
const configWatchInterval = 2 * time.Second

// fileSig is what the watcher compares to decide whether anything changed. Size
// alone misses an in-place edit of the same length, and mtime alone is
// ambiguous on filesystems with a coarse timestamp, so both.
type fileSig struct {
	mod  time.Time
	size int64
}

func statSig(path string) (fileSig, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return fileSig{}, false
	}
	return fileSig{mod: info.ModTime(), size: info.Size()}, true
}

// WatchConfig adopts changes another process makes to the config file until ctx
// is done. It is a no-op for a gateway with no config path. Call it once, from
// whoever owns the server's lifetime.
func (g *Gateway) WatchConfig(ctx context.Context) {
	g.watchConfig(ctx, configWatchInterval)
}

// watchConfig adopts changes another process makes to the config file: an
// account added by `peaproxy auth login`, a catalog or hide edit from the CLI, a
// hand edit. Until now those only reached a running server at its next save,
// which for an idle server is never (#52).
//
// It runs until ctx is done and returns immediately for a gateway with no
// config file.
func (g *Gateway) watchConfig(ctx context.Context, interval time.Duration) {
	g.mu.RLock()
	path := g.path
	g.mu.RUnlock()
	if path == "" {
		return
	}
	// Start from what is on disk now: serve has already loaded this file, and
	// adopting it again would be a pointless rebuild.
	last, _ := statSig(path)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		sig, ok := statSig(path)
		if !ok {
			// Deleted, or being replaced right now. Keep serving what is in
			// memory; if it comes back it is adopted then.
			continue
		}
		if sig == last {
			continue
		}
		// A reload that fails is retried, so last only advances once the change
		// has actually been adopted -- or has been deliberately skipped, which
		// is the one case where the file has moved without us taking it. A user
		// halfway through an edit therefore costs one stat every couple of
		// seconds and one log line, not a half-applied config.
		skipped, err := g.reloadFromDisk(path)
		if err != nil {
			g.noteReloadErr(path, err)
			continue
		}
		if skipped {
			// A save was in progress. Our own save moves the signature too, but
			// a save that FAILED leaves the external edit sitting there, so the
			// next tick has to look again.
			last = fileSig{}
			continue
		}
		last = sig
	}
}

// reloadFromDisk loads the config file and adopts it. Secrets are hydrated by
// the load, so an account added by another process arrives with its token, not
// just its metadata.
//
// It never waits for saveMu: a reload that ran during a save would read a file
// another writer is in the middle of replacing and then write its own idea of it
// straight back. It reports the skip instead, so the watcher comes back to it on
// the next tick rather than deciding the change was handled.
func (g *Gateway) reloadFromDisk(path string) (skipped bool, err error) {
	if !g.saveMu.TryLock() {
		return true, nil
	}
	defer g.saveMu.Unlock()
	disk, err := config.Load(path)
	if err != nil {
		return false, err
	}
	rebuilt, err := g.adopt(disk)
	if err != nil {
		return false, err
	}
	if rebuilt {
		go g.Refresh(context.Background())
	}
	return false, nil
}

// adopt installs disk as the running config, keeping the fields that only a
// restart can change. The caller holds saveMu.
func (g *Gateway) adopt(disk config.Config) (rebuilt bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	next := config.Clone(g.cfg)
	// Request-path settings. These are read per request, so a file edit should
	// take effect without a restart.
	next.Providers = disk.Providers
	next.Hide = disk.Hide
	next.Expose = disk.Expose
	next.Catalog = disk.Catalog
	next.Routes = disk.Routes
	next.Failover = disk.Failover
	next.AutomaticRoutes = disk.AutomaticRoutes
	next.RequestEngine = disk.RequestEngine
	// Deliberately not adopted, because they cannot change under a running
	// server and adopting them would be a surprise rather than a feature:
	// Bind and Port (the listener is already bound), AllowNonLoopback and
	// AdminToken (a security posture that should not change by editing a file
	// while requests are in flight), RequestLog (a file handle opened at start),
	// and SchemaVersion. The next start picks them up.
	next.Bind, next.Port = g.cfg.Bind, g.cfg.Port
	next.AllowNonLoopback, next.AdminToken = g.cfg.AllowNonLoopback, g.cfg.AdminToken
	next.RequestLog, next.SchemaVersion = g.cfg.RequestLog, g.cfg.SchemaVersion
	g.cfg = next
	// The merge base moves with it, so the next save does not undo what was just
	// adopted.
	g.saved = next
	changed := !reflect.DeepEqual(g.cfg.Providers, disk.Providers)
	if changed {
		kept := make(map[string]bool, len(g.cfg.Providers))
		for _, p := range g.cfg.Providers {
			kept[p.ID] = true
		}
		g.mu.Unlock()
		err = g.rebuild()
		g.mu.Lock()
		for _, p := range disk.Providers {
			if !kept[p.ID] {
				g.mu.Lock()
				g.forgetAccountLocked(p.ID)
				g.mu.Unlock()
			}
		}
	}
	return changed, err
}

// noteReloadErr logs a failed reload once per distinct error, the way a failed
// save is logged, and clears on the next success.
func (g *Gateway) noteReloadErr(path string, err error) {
	msg := tempName.ReplaceAllString(err.Error(), ".*.tmp")
	g.mu.Lock()
	changed := msg != g.lastReloadErr
	g.lastReloadErr = msg
	g.mu.Unlock()
	if changed {
		log.Printf("peaproxy: config reload failed (%s), still serving the last good config: %v", path, err)
	}
}
