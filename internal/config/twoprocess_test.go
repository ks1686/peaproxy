package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/secretstore"
)

// #65, fourth item: a running `serve` saving at the same time as `peaproxy auth
// login` in another terminal. Tests only covered the merge in sequence; this is
// the two-process case, and it needs no provider credentials.
//
// The invariant: an account added or re-logged-in by one process must survive a
// save by the other. Losing it means a user logs in and their account silently
// disappears, and it is the worst failure this codebase has.

const (
	twoProcEnv  = "PEAPROXY_TWOPROC_CHILD"
	childDirEnv = "PEAPROXY_TWOPROC_DIR"
	childIDEnv  = "PEAPROXY_TWOPROC_ID"
)

func procChildExit(err error) {
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func procWaitForFile(path string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s", path)
}

func procChild() bool {
	if os.Getenv(twoProcEnv) == "" {
		return false
	}
	dir := os.Getenv(childDirEnv)
	id, _ := strconv.Atoi(os.Getenv(childIDEnv))
	path := filepath.Join(dir, "config.yaml")
	const rounds = 12

	if err := procWaitForFile(filepath.Join(dir, "start"), 30*time.Second); err != nil {
		procChildExit(err)
	}
	for round := 0; round < rounds; round++ {
		// Both processes do what the real binaries do: put the credential in
		// the config and let Save/SaveMerged persist it. Writing straight into
		// the store would be a shape no user can reach, and Prune is right to
		// delete a secret no account references.
		account := "cli-account"
		if id%2 == 1 {
			account = "server-account"
		}
		value := account[:3] + "-key-" + strconv.Itoa(round)
		setKey := func(c *Config) error {
			for i := range c.Providers {
				if c.Providers[i].ID == account {
					c.Providers[i].APIKey = value
					return nil
				}
			}
			c.Providers = append(c.Providers, Provider{
				ID: account, Adapter: "openai_compat", Tier: "paid",
				BaseURL: "http://127.0.0.1:1/v1", APIKey: value,
			})
			return nil
		}
		if id%2 == 0 {
			// The CLI: `auth login` adds the account under config.lock.
			if _, err := Update(path, setKey); err != nil {
				procChildExit(err)
			}
			continue
		}
		// The server: a token refresh rewrites its own account's secret and
		// saves the merged config, which is what serve does on every refresh.
		base := Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317}
		mine := base
		if err := setKey(&mine); err != nil {
			procChildExit(err)
		}
		if _, err := SaveMerged(path, base, mine); err != nil {
			procChildExit(err)
		}
	}
	procChildExit(nil)
	return true
}

// TestTwoProcessesSavingAtOnceKeepBothAccounts runs an "auth login" process and
// a "serve" process against one config directory simultaneously, and requires
// that both accounts survive.
func TestTwoProcessesSavingAtOnceKeepBothAccounts(t *testing.T) {
	if procChild() {
		return
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// Seed: the server's account, as it would be after `serve` had started.
	seed := Config{SchemaVersion: 1, Bind: "127.0.0.1", Port: 8317}
	seed.Providers = []Provider{cfgProvider("server-account")}
	if err := Save(path, seed); err != nil {
		t.Fatal(err)
	}

	const children = 4
	type proc struct {
		out string
		err error
	}
	done := make(chan proc, children)
	for i := 0; i < children; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTwoProcessesSavingAtOnceKeepBothAccounts$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			twoProcEnv+"=1", childDirEnv+"="+dir, childIDEnv+"="+strconv.Itoa(i))
		go func() {
			out, err := cmd.CombinedOutput()
			done <- proc{string(out), err}
		}()
	}
	if err := os.WriteFile(filepath.Join(dir, "start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < children; i++ {
		p := <-done
		if p.err != nil {
			t.Fatalf("child %d failed: %v\n%s", i, p.err, p.out)
		}
	}

	// Then: both accounts are on disk and both secrets still read back.
	got, err := Load(path)
	if err != nil {
		t.Fatalf("load after concurrent saves: %v", err)
	}
	var ids []string
	for _, p := range got.Providers {
		ids = append(ids, p.ID)
	}
	if !containsStr(ids, "server-account") {
		t.Errorf("the server's account did not survive: %v", ids)
	}
	if !containsStr(ids, "cli-account") {
		t.Errorf("the account added by the other process did not survive: %v", ids)
	}
	// And the secrets must not have been spliced or dropped by a torn index.
	st, err := secretstore.OpenFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"server-account", "cli-account"} {
		v, err := st.Get(id, secretstore.KindAPIKey)
		if err != nil {
			t.Errorf("secret for %s unreadable after concurrent saves: %v", id, err)
			continue
		}
		if !strings.HasPrefix(v, id[:3]+"-key-") {
			t.Errorf("secret for %s is spliced: %q", id, v)
		}
	}
}

// cfgProvider is a minimal account; the test is about who survives a save, not
// what is in the account.
func cfgProvider(id string) Provider {
	return Provider{ID: id, Adapter: "openai_compat", Tier: "paid", BaseURL: "http://127.0.0.1:1/v1"}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
