package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/fslock"
	"github.com/ks1686/peaproxy/internal/secretstore"
)

func prov(id, token string) Provider {
	p := Provider{ID: id, Adapter: "native", Tier: "paid"}
	if token != "" {
		p.OAuth = &OAuthToken{AccessToken: token}
	}
	return p
}

func ids(ps []Provider) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		tok := ""
		if p.OAuth != nil {
			tok = ":" + p.OAuth.AccessToken
		}
		out = append(out, p.ID+tok)
	}
	return out
}

func TestMergeProviders(t *testing.T) {
	cases := []struct {
		name             string
		base, disk, mine []Provider
		want             []string
	}{
		{"in both, changed by me: mine wins",
			[]Provider{prov("a", "1")}, []Provider{prov("a", "disk")}, []Provider{prov("a", "mine")}, []string{"a:mine"}},
		{"in both, untouched by me: disk wins",
			[]Provider{prov("a", "1")}, []Provider{prov("a", "disk")}, []Provider{prov("a", "1")}, []string{"a:disk"}},
		{"in both, absent from base: mine wins",
			nil, []Provider{prov("a", "disk")}, []Provider{prov("a", "mine")}, []string{"a:mine"}},
		{"mine only, untouched: deleted elsewhere, dropped",
			[]Provider{prov("a", "1"), prov("b", "1")}, []Provider{prov("b", "1")}, []Provider{prov("a", "1"), prov("b", "1")}, []string{"b:1"}},
		{"mine only, changed by me: kept",
			[]Provider{prov("a", "1")}, nil, []Provider{prov("a", "mine")}, []string{"a:mine"}},
		{"mine only, new: kept",
			nil, nil, []Provider{prov("a", "1")}, []string{"a:1"}},
		{"disk only, in base: deleted by me, dropped",
			[]Provider{prov("a", "1"), prov("b", "1")}, []Provider{prov("a", "1"), prov("b", "1")}, []Provider{prov("a", "1")}, []string{"a:1"}},
		{"disk only, new: added elsewhere, appended in disk order",
			[]Provider{prov("a", "1")}, []Provider{prov("c", "1"), prov("a", "1"), prov("b", "1")}, []Provider{prov("a", "1")}, []string{"a:1", "c:1", "b:1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(Config{Providers: tc.base}, Config{Providers: tc.disk}, Config{Providers: tc.mine})
			if !reflect.DeepEqual(ids(got.Providers), tc.want) {
				t.Fatalf("got %v, want %v", ids(got.Providers), tc.want)
			}
		})
	}
}

func TestMergeFieldsMineWinsOnlyWhenChanged(t *testing.T) {
	base := Default()
	disk := Clone(base)
	disk.Port = 8318
	disk.Hide.Models = []string{"disk-hidden"}
	mine := Clone(base)
	mine.Hide.Models = []string{"mine-hidden"}

	got := Merge(base, disk, mine)
	if got.Port != 8318 {
		t.Fatalf("untouched port should come from disk, got %d", got.Port)
	}
	if !reflect.DeepEqual(got.Hide.Models, []string{"mine-hidden"}) {
		t.Fatalf("changed hide should come from mine, got %v", got.Hide.Models)
	}
}

func TestMergeHandlesEveryConfigField(t *testing.T) {
	typ := reflect.TypeOf(Config{})
	if typ.NumField() != 15 {
		t.Fatalf("Config has %d fields; Merge handles 15 — add the new field to Merge and this test", typ.NumField())
	}
	var full Config
	populate(reflect.ValueOf(&full).Elem())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "Providers" {
			continue
		}
		set := func() Config {
			var c Config
			reflect.ValueOf(&c).Elem().Field(i).Set(reflect.ValueOf(full).Field(i))
			return c
		}
		t.Run(f.Name, func(t *testing.T) {
			changed := Merge(Config{}, Config{}, set())
			if !reflect.DeepEqual(reflect.ValueOf(changed).Field(i).Interface(), reflect.ValueOf(full).Field(i).Interface()) {
				t.Errorf("mine changed %s but Merge did not keep it", f.Name)
			}
			untouched := Merge(Config{}, set(), Config{})
			if !reflect.DeepEqual(reflect.ValueOf(untouched).Field(i).Interface(), reflect.ValueOf(full).Field(i).Interface()) {
				t.Errorf("mine left %s untouched but Merge did not take disk", f.Name)
			}
		})
	}
}

func TestMergeDoesNotAliasInputs(t *testing.T) {
	disk := Config{Hide: HideList{Models: []string{"a"}}, Providers: []Provider{prov("a", "1")}}
	got := Merge(Config{}, disk, Config{})
	got.Hide.Models[0] = "changed"
	got.Providers[0].OAuth.AccessToken = "changed"
	if disk.Hide.Models[0] != "a" || disk.Providers[0].OAuth.AccessToken != "1" {
		t.Fatal("Merge result aliases its input")
	}
}

func withLockTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := lockTimeout
	lockTimeout = d
	t.Cleanup(func() { lockTimeout = old })
}

func TestSaveMergedKeepsForeignAccountSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := Default()
	base.Providers = []Provider{prov("a", "tok-a")}
	if err := Save(path, base); err != nil {
		t.Fatal(err)
	}
	// Another process adds account b.
	other := Clone(base)
	other.Providers = append(other.Providers, prov("b", "tok-b"))
	if err := Save(path, other); err != nil {
		t.Fatal(err)
	}
	mine := Clone(base)
	mine.Providers[0].OAuth.AccessToken = "tok-a2"

	merged, err := SaveMerged(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(merged.Providers); !reflect.DeepEqual(got, []string{"a:tok-a2", "b:tok-b"}) {
		t.Fatalf("merged providers %v", got)
	}
	disk, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(disk.Providers); !reflect.DeepEqual(got, []string{"a:tok-a2", "b:tok-b"}) {
		t.Fatalf("disk providers %v", got)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("b", secretstore.KindOAuth); err != nil {
		t.Fatalf("b's secret was pruned: %v", err)
	}
}

func TestSaveMergedRefusesUnreadableDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := Default()
	if err := Save(path, base); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("providers: [\n")
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	mine := Clone(base)
	mine.Port = 9999
	if _, err := SaveMerged(path, base, mine); err == nil {
		t.Fatal("SaveMerged overwrote a file it could not read")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(garbage) {
		t.Fatalf("unreadable file was modified: %q", got)
	}
}

// fslock is not reentrant: a public entry point that took config.lock twice
// would wait on itself and fail with ErrBusy after lockTimeout.
func TestPublicEntryPointsLockOnce(t *testing.T) {
	withLockTimeout(t, 200*time.Millisecond)
	nested := func(t *testing.T) string {
		return filepath.Join(t.TempDir(), "nested", "config.yaml")
	}
	t.Run("EnsureFile", func(t *testing.T) {
		if _, _, _, err := EnsureFile(nested(t)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("EnsureFileExisting", func(t *testing.T) {
		path := nested(t)
		if _, _, _, err := EnsureFile(path); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := EnsureFile(path); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Update", func(t *testing.T) {
		if _, err := Update(nested(t), func(c *Config) error {
			c.Providers = append(c.Providers, prov("a", "1"))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Save", func(t *testing.T) {
		if err := Save(nested(t), Default()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("SaveMerged", func(t *testing.T) {
		path := nested(t)
		if _, err := SaveMerged(path, Default(), Default()); err != nil {
			t.Fatal(err)
		}
		if _, err := SaveMerged(path, Default(), Default()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("busy while another holder has config.lock", func(t *testing.T) {
		path := nested(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		unlock, err := fslock.Lock(filepath.Join(filepath.Dir(path), "config.lock"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if err := Save(path, Default()); !errors.Is(err, fslock.ErrBusy) {
			t.Fatalf("Save while locked: %v, want ErrBusy", err)
		}
		if _, err := Update(path, func(*Config) error { return nil }); !errors.Is(err, fslock.ErrBusy) {
			t.Fatalf("Update while locked: %v, want ErrBusy", err)
		}
	})
}

// An account's secret that could not be read back from disk during a merged
// save must not replace the live secret of an account this writer left alone --
// but only when it really was unreadable (#54). A secret that is merely absent
// from disk was removed on purpose, and stays removed.
func TestMergeKeepsSecretDiskCopyLacks(t *testing.T) {
	withKey := Provider{ID: "k", Adapter: "native", Tier: "paid", APIKey: "sk-k"}
	withTok := prov("o", "tok-o")
	withTok.OAuth.RefreshToken = "ref-o"
	withTok.OAuth.ExpiresAt = "2026-09-29T10:00:00Z"
	withTok.OAuth.Extra = map[string]string{"dca_token": "dca", "project": "p1"}
	base := Default()
	base.Providers = []Provider{withKey, withTok}
	mine := Clone(base)
	disk := Clone(base)
	disk.Providers[0].APIKey = ""
	disk.Providers[1].OAuth = &OAuthToken{Email: "o@x", ExpiresAt: "2026-09-29T11:00:00Z", Extra: map[string]string{"project": "p2"}}

	unreadable := unreadableSecrets{{"k", secretAPIKey}, {"o", secretOAuth}}
	got := merge(base, disk, mine, unreadable).Providers
	if got[0].APIKey != "sk-k" {
		t.Fatalf("api key dropped: %#v", got[0])
	}
	want := &OAuthToken{AccessToken: "tok-o", RefreshToken: "ref-o", ExpiresAt: "2026-09-29T10:00:00Z", Email: "o@x", Extra: map[string]string{"dca_token": "dca", "project": "p2"}}
	if !reflect.DeepEqual(got[1].OAuth, want) {
		t.Fatalf("oauth %#v, want %#v", got[1].OAuth, want)
	}
}

// The same merge with nothing reported unreadable: disk's token-less copy wins,
// and the server's token is not written back.
func TestMergeDoesNotResurrectADeletedSecret(t *testing.T) {
	withKey := Provider{ID: "k", Adapter: "native", APIKey: "sk-k"}
	withTok := prov("o", "tok-o")
	base := Default()
	base.Providers = []Provider{withKey, withTok}
	mine := Clone(base)
	disk := Clone(base)
	disk.Providers[0].APIKey = ""
	disk.Providers[1].OAuth = &OAuthToken{Email: "o@x"}

	got := Merge(base, disk, mine).Providers
	if got[0].APIKey != "" {
		t.Errorf("an api key the user deleted came back: %q", got[0].APIKey)
	}
	if oauthHasSecret(got[1].OAuth) {
		t.Errorf("a deleted oauth token came back: %#v", got[1].OAuth)
	}
	if got[1].OAuth == nil || got[1].OAuth.Email != "o@x" {
		t.Errorf("the public fields should still come from disk: %#v", got[1].OAuth)
	}
}

func TestSaveMergedKeepsTokenWhenDiskSecretUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := Default()
	base.Providers = []Provider{prov("a", "tok-a"), prov("b", "tok-b")}
	if err := Save(path, base); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("a", secretstore.KindOAuth, `{"accessToken":"trunc`); err != nil {
		t.Fatal(err)
	}
	mine := Clone(base)
	mine.Providers[1].OAuth.AccessToken = "tok-b2"

	merged, err := SaveMerged(path, base, mine)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(merged.Providers); !reflect.DeepEqual(got, []string{"a:tok-a", "b:tok-b2"}) {
		t.Fatalf("merged providers %v", got)
	}
	if _, err := store.Get("a", secretstore.KindOAuth); err != nil {
		t.Fatalf("a's secret was deleted: %v", err)
	}
	disk, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(disk.Providers); !reflect.DeepEqual(got, []string{"a:tok-a", "b:tok-b2"}) {
		t.Fatalf("disk providers %v", got)
	}
}
