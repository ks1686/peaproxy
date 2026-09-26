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
	if strings.Contains(got[0].Preview, "sk-secret") || strings.Contains(got[0].Error, "leaked") && !strings.Contains(got[0].Error, "[redacted]") {
		t.Fatalf("%#v", got[0])
	}
	if !strings.Contains(got[0].Preview, "[redacted]") || !strings.Contains(got[0].Error, "[redacted]") {
		t.Fatalf("expected surgical redaction: %#v", got[0])
	}
	b, err := os.ReadFile(filepath.Join(dir, "requests.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "sk-secret") || strings.Contains(string(b), "x-api-key leaked") {
		t.Fatalf("log leaked secret: %s", b)
	}
}

func TestRequestLogDisabledDoesNotWriteFile(t *testing.T) {
	dir := t.TempDir()
	s := Open(filepath.Join(dir, "usage.json"))
	s.Add(Event{AccountID: "a", Model: "m", Status: 200, Preview: "hello"})
	if _, err := os.Stat(filepath.Join(dir, "requests.log")); !os.IsNotExist(err) {
		t.Fatalf("requests.log should be absent when opt-in is off: %v", err)
	}
}

func TestRequestLogTailNewestFirstAndDropsSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.log")
	s := Open(filepath.Join(dir, "usage.json"))
	s.SetRequestLog(path)
	s.Add(Event{AccountID: "a", Model: "one", Status: 200, Preview: "first"})
	s.Add(Event{AccountID: "a", Model: "two", Status: 200, Preview: "second Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.aaa.bbb"})
	got := s.Tail(10)
	if len(got) != 2 {
		t.Fatalf("%#v", got)
	}
	if got[0].Model != "two" || got[1].Model != "one" {
		t.Fatalf("newest first: %#v", got)
	}
	if strings.Contains(got[0].Preview, "eyJ") {
		t.Fatalf("jwt leaked: %#v", got[0])
	}
}

func TestRequestLogRotatesWhenOverMaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.log")
	s := Open("")
	s.SetRequestLog(path)
	s.SetMaxLogBytes(200)
	for i := 0; i < 40; i++ {
		s.Add(Event{AccountID: "acct", Model: "m", Status: 200, Preview: "abcdefghijklmnopqrstuvwxyz"})
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 200+80 {
		t.Fatalf("log not rotated: size=%d", info.Size())
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", st, err)
	}
}
