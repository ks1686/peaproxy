package secretstore

import (
	"testing"
)

func openForTest(t *testing.T) *Store {
	t.Helper()
	s, err := OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// #77: an account id may contain a slash. Item keys are id + "/" + kind, and
// kind never contains one, so the id is everything before the last slash.
func TestItemKeyWithASlashInTheIDRoundTrips(t *testing.T) {
	for _, tc := range []struct{ id string }{
		{"work/openai"},
		{"a/b/c"},
		{"team/sub/team/openai-oauth"},
		{"/leading"},
		{"trailing/"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			for _, kind := range []Kind{KindOAuth, KindAPIKey} {
				id, gotKind, ok := parseItemKey(itemKey(tc.id, kind))
				if !ok {
					t.Fatalf("itemKey(%q, %q) did not parse", tc.id, kind)
				}
				if id != tc.id {
					t.Errorf("id round-tripped as %q, want %q", id, tc.id)
				}
				if gotKind != kind {
					t.Errorf("kind = %q, want %q", gotKind, kind)
				}
				if got := accountOf(itemKey(tc.id, kind)); got != tc.id {
					t.Errorf("accountOf = %q, want %q", got, tc.id)
				}
			}
		})
	}
}

// An id that is only a slash, or a key with no kind at all, is not a valid item
// key and must not resolve to some other account.
func TestItemKeyRejectsKeysThatAreNotOne(t *testing.T) {
	for _, k := range []string{"", "/", "oauth", "acct/"} {
		if _, _, ok := parseItemKey(k); ok {
			t.Errorf("%q parsed as an item key", k)
		}
		if got := accountOf(k); got != "" {
			t.Errorf("accountOf(%q) = %q, want empty", k, got)
		}
	}
}

// The actual reported bug: a secret is written for an id with a slash, the
// prune that follows does not recognise the id, and deletes the secret that was
// just written. Everything works until the process restarts.
func TestPruneKeepsSecretsForIDsWithSlashes(t *testing.T) {
	s := openForTest(t)
	ids := []string{"work/openai", "personal", "a/b/c"}
	for _, id := range ids {
		if err := s.Set(id, KindAPIKey, "key-for-"+id); err != nil {
			t.Fatal(err)
		}
		if err := s.Set(id, KindOAuth, "oauth-for-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune(ids); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		for kind, want := range map[Kind]string{KindAPIKey: "key-for-" + id, KindOAuth: "oauth-for-" + id} {
			got, err := s.Get(id, kind)
			if err != nil {
				t.Fatalf("%s/%s was pruned: %v", id, kind, err)
			}
			if got != want {
				t.Errorf("%s/%s = %q, want %q", id, kind, got, want)
			}
		}
	}
}

// Prune still removes what it should, including an id whose prefix is another
// account's id: "work/openai" being kept must not keep "work/openai-other".
func TestPruneStillDeletesUnrelatedSecrets(t *testing.T) {
	s := openForTest(t)
	if err := s.Set("work/openai", KindAPIKey, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("work/openai-other", KindAPIKey, "drop"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("work", KindAPIKey, "drop"); err != nil {
		t.Fatal(err)
	}
	if err := s.Prune([]string{"work/openai"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("work/openai", KindAPIKey); err != nil {
		t.Fatalf("the kept id was pruned: %v", err)
	}
	for _, id := range []string{"work/openai-other", "work"} {
		if _, err := s.Get(id, KindAPIKey); err == nil {
			t.Errorf("%s survived the prune", id)
		}
	}
}

// Two kinds for the same slashed id must stay distinct items, and deleting one
// must not take the other with it.
func TestSlashIDKindsStaySeparate(t *testing.T) {
	s := openForTest(t)
	if err := s.Set("work/openai", KindAPIKey, "key"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("work/openai", KindOAuth, "token"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("work/openai", KindAPIKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("work/openai", KindAPIKey); err == nil {
		t.Error("the api key survived the delete")
	}
	got, err := s.Get("work/openai", KindOAuth)
	if err != nil || got != "token" {
		t.Errorf("deleting the api key took the oauth token too: %q %v", got, err)
	}
}
