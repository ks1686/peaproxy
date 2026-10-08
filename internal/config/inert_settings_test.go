package config

import (
	"strings"
	"testing"
)

// A setting that is accepted and validated but does nothing is the same failure
// class as a misspelled key, and harder to notice: the spelling is right, so
// nothing looks wrong. These tests hold the reporting in place. The behaviour
// being reported is the *absence* of disk persistence, which is asserted
// directly in the gateway test TestPersistentContextDoesNotWriteToDisk.

func TestAnUnsetInertSettingIsNotReported(t *testing.T) {
	if got := Default().InertSettings(); len(got) != 0 {
		t.Fatalf("default config reported inert settings: %+v", got)
	}
}

func TestPersistentContextTrueIsReportedAsInert(t *testing.T) {
	yes := true
	c := Default()
	c.Optimization.PersistentContext = &yes

	got := c.InertSettings()
	if len(got) != 1 {
		t.Fatalf("inert settings = %+v, want exactly one", got)
	}
	if got[0].Path != "optimization.persistentContext" {
		t.Fatalf("path = %q, want optimization.persistentContext", got[0].Path)
	}
	// The detail has to name what is missing, or the report is a key the user
	// cannot act on.
	for _, want := range []string{"memory-only", "disk"} {
		if !strings.Contains(got[0].Detail, want) {
			t.Errorf("detail %q does not mention %q", got[0].Detail, want)
		}
	}
}

func TestPersistentContextFalseIsNotInert(t *testing.T) {
	no := false
	c := Default()
	c.Optimization.PersistentContext = &no
	if got := c.InertSettings(); len(got) != 0 {
		t.Fatalf("setting it to false reported inert settings: %+v", got)
	}
}

// The reporter has to be fed by the real loader. The first draft of this file
// built the struct in the test, which proves the reporter reads a struct and
// nothing about whether the YAML key reaches it — a reporter fed nothing
// reports nothing, and that reads as a check that passed.
func TestInertSettingsAreReachableFromYAML(t *testing.T) {
	loaded := writeConfig(t, "optimization:\n  persistentContext: true\n")
	if !loaded.PersistentContextEnabled() {
		t.Fatal("persistentContext: true did not survive the loader")
	}
	got := loaded.InertSettings()
	if len(got) != 1 || got[0].Path != "optimization.persistentContext" {
		t.Fatalf("from YAML, inert settings = %+v", got)
	}
}
