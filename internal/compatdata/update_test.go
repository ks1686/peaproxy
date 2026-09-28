package compatdata

import (
	"crypto/ed25519"
	"path/filepath"
	"testing"
)

func TestRejectUnsignedMetadata(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	raw := MustJSON(Document{Schema: SchemaVersion, Profiles: map[string]Profile{"x": {Tools: "yes"}}})
	if err := s.Apply(priv.Public().(ed25519.PublicKey), raw, nil); err == nil {
		t.Fatal("unsigned update was accepted")
	}
}

func TestIncompatibleSchemaKeepsLastGood(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	before := s.Current().Schema
	raw := []byte(`{"schema":99,"profiles":{}}`)
	sig := ed25519.Sign(priv, raw)
	if err := s.Apply(pub, raw, sig); err == nil {
		t.Fatal("incompatible schema was applied")
	}
	if s.Current().Schema != before {
		t.Fatal("last good document was replaced")
	}
}

func TestRollbackAtomic(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "compat.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	good := MustJSON(Document{Schema: SchemaVersion, Profiles: map[string]Profile{"ok": {Tools: "yes"}}})
	if err := s.Apply(pub, good, ed25519.Sign(priv, good)); err != nil {
		t.Fatal(err)
	}
	bad := []byte(`{"schema":1`)
	if err := s.Apply(pub, bad, ed25519.Sign(priv, bad)); err == nil {
		t.Fatal("truncated update was applied")
	}
	if _, ok := s.Current().Profiles["ok"]; !ok {
		t.Fatal("good profile was lost")
	}
}

func TestMetadataCannotCreateCatalogModels(t *testing.T) {
	raw := []byte(`{"schema":1,"models":["gpt-fake"],"profiles":{}}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("model list was accepted")
	}
}
