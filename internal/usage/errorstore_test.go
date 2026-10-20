package usage

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newErrorStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s := &Store{path: filepath.Join(dir, "usage.json")}
	s.SetErrorLog(ErrorLogPath(dir))
	return s, dir
}

func readErrorLines(t *testing.T, path string) []Event {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // the store removes an emptied file rather than leaving a stub
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line is not a valid record: %s", line)
		}
		out = append(out, e)
	}
	return out
}

func TestErrorStoreKeepsAFailureAndSkipsACleanSuccess(t *testing.T) {
	s, dir := newErrorStore(t)
	now := time.Now()

	s.AddError(Event{AccountID: "xai-oauth", Provider: "xai_oauth", Model: "grok-4.7", Status: 401,
		Class: ClassAuthRevoked, Time: now})
	// A clean success has nothing to say here and must not push out a failure.
	s.AddError(Event{AccountID: "workers-ai", Status: 200, Class: FailureNone, Time: now})

	got := readErrorLines(t, ErrorLogPath(dir))
	if len(got) != 1 {
		t.Fatalf("wrote %d records, want 1 -- the clean success should not be here: %+v", len(got), got)
	}
	if got[0].Class != ClassAuthRevoked || got[0].AccountID != "xai-oauth" {
		t.Fatalf("wrong record: %+v", got[0])
	}
}

// A client disconnect is retained, but as a cancel and not as a failure. 25 of
// this machine's 26 current errors are these; counting them as faults would
// bury the ones that are.
func TestClientCancelIsRetainedButNotAFailure(t *testing.T) {
	s, dir := newErrorStore(t)
	s.AddError(Event{AccountID: "opencode-zen", Error: "context canceled", Class: ClassClientCancel, Time: time.Now()})

	got := readErrorLines(t, ErrorLogPath(dir))
	if len(got) != 1 {
		t.Fatalf("cancels must be retained: %+v", got)
	}
	if got[0].Class.IsFailure() {
		t.Fatal("a client cancel is being counted as a failure")
	}
	sum := SummarizeErrors(got)
	if sum.Cancels != 1 || sum.Failures != 0 {
		t.Fatalf("summary = %+v, want 1 cancel and 0 failures", sum)
	}
}

// The point of the separate file: it is meant to be uploaded, so it must not
// carry the prompt. A failed request's prompt is something the user already
// knows they sent.
func TestErrorStoreCarriesNoPrompt(t *testing.T) {
	s, dir := newErrorStore(t)
	s.AddError(Event{
		AccountID: "anthropic-oauth", Status: 500,
		Preview: `{"messages":[{"role":"user","content":"my bank password is hunter2"}]}`,
		Class:   ClassUpstreamError, Time: time.Now(),
	})

	b, err := os.ReadFile(ErrorLogPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "hunter2") || strings.Contains(string(b), "preview") {
		t.Fatalf("the error store carried a prompt preview: %s", b)
	}
}

// An upstream body routinely quotes the offending credential. This file's whole
// purpose is to leave the machine, so redaction is a correctness requirement and
// not a nicety.
func TestErrorStoreRedactsTheUpstreamBody(t *testing.T) {
	s, dir := newErrorStore(t)
	s.AddError(Event{
		AccountID: "copilot-oauth", Status: 401,
		Error: `{"message":"bad token ghu_ExampleTokenNotARealCredential0"}`,
		Class: ClassAuthRevoked, Time: time.Now(),
	})

	b, err := os.ReadFile(ErrorLogPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ghu_ExampleTokenNotARealCredential0") {
		t.Fatalf("a GitHub token reached the uploadable error store: %s", b)
	}
}

// Retention is a week of history, enforced by time. A size cap would let a burst
// of failures evict the error that caused them.
//
// Ages are kept clear of the exact boundary: prune computes its cutoff from a
// fresh time.Now(), so a record stamped exactly seven days ago is a coin flip.
// The boundary itself is covered by
// TestPruneErrorLogKeepsTheWindowAndDropsTheRest, which passes a fixed cutoff.
func TestErrorStorePrunesByAgeNotBySize(t *testing.T) {
	s, dir := newErrorStore(t)
	now := time.Now()

	s.AddError(Event{AccountID: "ancient", Status: 500, Class: ClassUpstreamError, Time: now.AddDate(0, 0, -20)})
	s.AddError(Event{AccountID: "stale", Status: 500, Class: ClassUpstreamError, Time: now.AddDate(0, 0, -8)})
	s.AddError(Event{AccountID: "recent", Status: 500, Class: ClassUpstreamError, Time: now})

	got := readErrorLines(t, ErrorLogPath(dir))
	if len(got) != 1 || got[0].AccountID != "recent" {
		t.Fatalf("age-based pruning kept the wrong records: %+v", got)
	}
}

// A prune that only happens when a new error arrives means a quiet week leaves a
// stale file on disk forever.
func TestErrorStorePrunesOnReadToo(t *testing.T) {
	s, dir := newErrorStore(t)
	s.AddError(Event{AccountID: "stale", Status: 500, Class: ClassUpstreamError, Time: time.Now().AddDate(0, 0, -30)})

	got, err := s.ReadErrors(time.Now().AddDate(0, 0, -7))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a read returned records past the window: %+v", got)
	}
	// And the file is repaired, not just filtered in memory.
	if after := readErrorLines(t, ErrorLogPath(dir)); len(after) != 0 {
		t.Fatalf("the file on disk still holds %d stale records: %+v", len(after), after)
	}
}

// The store holds provider error text, so it gets the same mode as secrets.enc
// rather than the default umask.
func TestErrorStoreIsOwnerOnly(t *testing.T) {
	s, dir := newErrorStore(t)
	s.AddError(Event{AccountID: "a", Status: 500, Class: ClassUpstreamError, Time: time.Now()})

	info, err := os.Stat(ErrorLogPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX permission bits; Chmod there only toggles read-only,
	// so Mode().Perm() reports 0666 whatever we ask for. Same guard as the
	// request-log rotation test.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("errors.jsonl is %o, want 600", perm)
		}
	}
}

// Retention has to be enforceable without a fresh error arriving, so it is
// reachable directly rather than only as a side effect of AddError.
func TestPruneErrorLogKeepsTheWindowAndDropsTheRest(t *testing.T) {
	dir := t.TempDir()
	path := ErrorLogPath(dir)
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var lines []string
	for i, days := range []int{20, 3, 9, 1, 7} {
		e := Event{AccountID: string(rune('a' + i)), Status: 500, Class: ClassUpstreamError,
			Time: base.AddDate(0, 0, -days)}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	kept, err := pruneErrorLog(path, ErrorCutoff(base, ErrorRetention()))
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 3 {
		t.Fatalf("kept %d, want 3 (ages 3, 1 and the 7-day boundary): %+v", len(kept), kept)
	}
	for _, e := range kept {
		if e.AccountID == "a" || e.AccountID == "c" {
			t.Fatalf("kept a record past the window: %+v", kept)
		}
	}
	if kept[2].AccountID != "e" {
		t.Fatalf("the record exactly on the boundary was dropped: %+v", kept)
	}
}

// A file written before this feature existed, or truncated by a crash, must not
// take the export down with it.
func TestReadErrorRecordsSkipsUnparseableLines(t *testing.T) {
	dir := t.TempDir()
	path := ErrorLogPath(dir)
	good, _ := json.Marshal(Event{AccountID: "ok", Status: 500, Class: ClassUpstreamError, Time: time.Now()})
	content := "{not json\n" + string(good) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ReadErrorRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AccountID != "ok" {
		t.Fatalf("a torn line broke the read: %+v", got)
	}
}

// The prune rewrite replaces the file rather than truncating it in place. A
// truncate-then-write leaves a window (a crash, a full disk) in which the kept
// history is gone; a reader that opened the log before the prune sees that
// window as the file emptying under it.
func TestPruneErrorLogReplacesTheFileInsteadOfTruncatingIt(t *testing.T) {
	dir := t.TempDir()
	path := ErrorLogPath(dir)
	base := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	var lines []string
	for _, days := range []int{20, 1} {
		b, err := json.Marshal(Event{AccountID: "a", Status: 500, Class: ClassUpstreamError, Time: base.AddDate(0, 0, -days)})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	before := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if _, err := pruneErrorLog(path, ErrorCutoff(base, ErrorRetention())); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != before {
		t.Fatalf("the log was rewritten in place: a reader open across the prune saw %q", got)
	}
}
