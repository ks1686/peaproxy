package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	s := Open(path)
	s.Add(Event{AccountID: "a", Model: "m", Protocol: "openai", Status: 200, Preview: "hello"})
	again := Open(path)
	got := again.Recent()
	if len(got) != 1 || got[0].AccountID != "a" || got[0].Preview != "hello" {
		t.Fatalf("%#v", got)
	}
}

func TestRedactAndRequestLog(t *testing.T) {
	dir := t.TempDir()
	s := Open(filepath.Join(dir, "usage.json"))
	s.SetRequestLog(filepath.Join(dir, "requests.log"))
	s.Add(Event{AccountID: "a", Model: "m", Status: 200, Preview: "Authorization: Bearer sk-secret", Error: "x-api-key leaked"})
	got := s.Recent()
	if got[0].Preview != "[redacted]" || got[0].Error != "[redacted]" {
		t.Fatalf("%#v", got[0])
	}
	b, err := os.ReadFile(filepath.Join(dir, "requests.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "[redacted]") || strings.Contains(string(b), "sk-secret") {
		t.Fatalf("log: %s", b)
	}
}
