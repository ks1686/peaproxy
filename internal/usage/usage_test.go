package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheHitDoesNotReplayUpstreamUsage(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "usage.json"))
	body := []byte(`{"usage":{"prompt_tokens":12,"completion_tokens":4}}`)
	live := Event{AccountID: "a", Model: "m", Status: 200}
	ApplyPublishedUsage(&live, body, false)
	s.Add(live)
	hit := Event{AccountID: "a", Model: "m", Status: 200}
	ApplyPublishedUsage(&hit, body, true)
	s.Add(hit)
	days := s.ByDay()
	if len(days) != 1 || days[0].Calls != 2 || days[0].PromptTokens != 12 || days[0].CompletionTokens != 4 {
		t.Fatalf("%#v", days)
	}
	recent := s.Recent()
	if len(recent) != 2 || !recent[0].CacheHit || recent[0].PromptTokens != 0 || !recent[0].TokensKnown {
		t.Fatalf("%#v", recent)
	}
}

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

func TestRequestLogKeepsQuotaHintAndOmitsUnknown(t *testing.T) {
	dir := t.TempDir()
	s := Open(filepath.Join(dir, "usage.json"))
	s.SetRequestLog(filepath.Join(dir, "requests.log"))
	s.Add(Event{AccountID: "a", Model: "m", Status: 200, Preview: "ok", QuotaHint: "req=0"})
	s.Add(Event{AccountID: "b", Model: "m", Status: 200, Preview: "ok"})
	got := s.Recent()
	if len(got) != 2 {
		t.Fatalf("%#v", got)
	}
	if got[0].QuotaHint != "" {
		t.Fatalf("unknown remaining must not invent a hint: %#v", got[0])
	}
	if got[1].QuotaHint != "req=0" {
		t.Fatalf("honest 0 must persist: %#v", got[1])
	}
	b, err := os.ReadFile(filepath.Join(dir, "requests.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"quotaHint":"req=0"`) {
		t.Fatalf("request log missing hint: %s", b)
	}
	if strings.Count(string(b), `"quotaHint"`) != 1 {
		t.Fatalf("unreported remaining leaked onto a row: %s", b)
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

func TestByProviderRollup(t *testing.T) {
	s := Open("")
	s.Add(Event{AccountID: "ollama-local", Provider: "ollama", Model: "llama3.2", Status: 200, PromptTokens: 3, CompletionTokens: 5})
	s.Add(Event{AccountID: "openai-key", Provider: "openai", Model: "gpt-4o", Status: 200, PromptTokens: 10, CompletionTokens: 2})
	s.Add(Event{AccountID: "openai-oauth", Provider: "openai", Model: "gpt-4o", Status: 429, Error: "rate"})
	s.Add(Event{AccountID: "legacy", Model: "old", Status: 200})
	got := s.ByProvider()
	if len(got) != 3 {
		t.Fatalf("%#v", got)
	}
	by := map[string]ProviderRollup{}
	for _, r := range got {
		by[r.Provider] = r
	}
	if by["ollama"].Calls != 1 || by["ollama"].Tokens != 8 || by["ollama"].Accounts != 1 {
		t.Fatalf("ollama: %#v", by["ollama"])
	}
	if by["openai"].Calls != 2 || by["openai"].Errors != 1 || by["openai"].Accounts != 2 || by["openai"].Tokens != 12 {
		t.Fatalf("openai: %#v", by["openai"])
	}
	if by["legacy"].Calls != 1 || by["legacy"].Accounts != 1 {
		t.Fatalf("fallback account id: %#v", by["legacy"])
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
