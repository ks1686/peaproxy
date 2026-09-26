// Package usage records per-request stats for the Showcase, Health, and Request Log pages.
package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	capEvents       = 200
	defaultMaxLog   = 1 << 20
	maxPreview      = 240
	maxTailRead     = 256 << 10
	keepAfterRotate = 80
)

// Event is one completed (or failed) proxy call. Secrets must never be stored.
type Event struct {
	Time             time.Time `json:"time"`
	AccountID        string    `json:"accountId"`
	Model            string    `json:"model"`
	Protocol         string    `json:"protocol"`
	Path             string    `json:"path,omitempty"`
	Stream           bool      `json:"stream"`
	PromptTokens     int       `json:"promptTokens,omitempty"`
	CompletionTokens int       `json:"completionTokens,omitempty"`
	Status           int       `json:"status"`
	Error            string    `json:"error,omitempty"`
	Preview          string    `json:"preview,omitempty"`
	DurationMS       int64     `json:"durationMs,omitempty"`
}

// AccountRollup is a per-account summary.
type AccountRollup struct {
	AccountID string `json:"accountId"`
	Calls     int    `json:"calls"`
	Errors    int    `json:"errors"`
	Tokens    int    `json:"tokens"`
}

type diskFile struct {
	Events []Event `json:"events"`
}

// Store is a ring buffer persisted as JSON when Path is set.
type Store struct {
	mu         sync.Mutex
	events     []Event
	path       string
	requestLog string
	maxLog     int64
}

// Open loads events from path if the file exists.
func Open(path string) *Store {
	s := &Store{path: path, maxLog: defaultMaxLog}
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var disk diskFile
	if json.Unmarshal(b, &disk) == nil {
		s.events = disk.Events
		if len(s.events) > capEvents {
			s.events = s.events[len(s.events)-capEvents:]
		}
	}
	return s
}

// SetRequestLog enables an append-only redacted JSONL log.
func (s *Store) SetRequestLog(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requestLog = path
}

// SetMaxLogBytes caps the opt-in JSONL file (tests + rotation). Zero restores default.
func (s *Store) SetMaxLogBytes(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n <= 0 {
		s.maxLog = defaultMaxLog
		return
	}
	s.maxLog = n
}

// RequestLogPath is the JSONL inspector file, if enabled.
func (s *Store) RequestLogPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requestLog
}

// Add appends an event, dropping the oldest when full, and persists.
func (s *Store) Add(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Preview = clip(Redact(e.Preview), maxPreview)
	e.Error = clip(Redact(e.Error), maxPreview)
	e.AccountID = clip(e.AccountID, 120)
	e.Model = clip(e.Model, 200)
	e.Protocol = clip(e.Protocol, 40)
	e.Path = clip(e.Path, 80)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) >= capEvents {
		s.events = append(s.events[1:], e)
	} else {
		s.events = append(s.events, e)
	}
	s.flushLocked()
	s.appendLogLocked(e)
}

func (s *Store) flushLocked() {
	if s.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	b, err := json.MarshalIndent(diskFile{Events: s.events}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.path, b, 0o600)
}

func (s *Store) appendLogLocked(e Event) {
	if s.requestLog == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.requestLog), 0o700)
	f, err := os.OpenFile(s.requestLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		_ = f.Close()
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
	_ = os.Chmod(s.requestLog, 0o600)
	info, err := os.Stat(s.requestLog)
	if err != nil {
		return
	}
	max := s.maxLog
	if max <= 0 {
		max = defaultMaxLog
	}
	if info.Size() > max {
		s.rotateLogLocked()
	}
}

func (s *Store) rotateLogLocked() {
	events := s.readLogLocked(keepAfterRotate * 4)
	if len(events) == 0 {
		_ = os.Truncate(s.requestLog, 0)
		return
	}
	keep := keepAfterRotate
	if s.maxLog > 0 {
		fit := int(s.maxLog / 256)
		if fit < 1 {
			fit = 1
		}
		if fit < keep {
			keep = fit
		}
	}
	if len(events) > keep {
		events = events[len(events)-keep:]
	}
	tmp := s.requestLog + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, s.requestLog)
	_ = os.Chmod(s.requestLog, 0o600)
}

func (s *Store) readLogLocked(n int) []Event {
	if s.requestLog == "" || n <= 0 {
		return nil
	}
	f, err := os.Open(s.requestLog)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if info.Size() > maxTailRead {
		start = info.Size() - maxTailRead
	}
	if _, err := f.Seek(start, 0); err != nil {
		return nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var lines []string
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first && start > 0 {
			first = false
			continue // likely a partial line
		}
		first = false
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}
	out := make([]Event, 0, len(lines))
	for _, line := range lines {
		var e Event
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		e.Preview = clip(Redact(e.Preview), maxPreview)
		e.Error = clip(Redact(e.Error), maxPreview)
		out = append(out, e)
	}
	return out
}

// Recent returns newest-first copies of the in-memory ring (usage.json).
func (s *Store) Recent() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return newestFirst(s.events)
}

// Tail returns newest-first inspector events from requests.log when enabled.
func (s *Store) Tail(n int) []Event {
	if n <= 0 {
		n = 50
	}
	if n > 500 {
		n = 500
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.requestLog == "" {
		return []Event{}
	}
	events := s.readLogLocked(n)
	return newestFirst(events)
}

func newestFirst(events []Event) []Event {
	out := make([]Event, len(events))
	for i := range events {
		out[len(events)-1-i] = events[i]
	}
	return out
}

// ByAccount rolls up counts.
func (s *Store) ByAccount() []AccountRollup {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := map[string]int{}
	var out []AccountRollup
	for _, e := range s.events {
		i, ok := idx[e.AccountID]
		if !ok {
			i = len(out)
			idx[e.AccountID] = i
			out = append(out, AccountRollup{AccountID: e.AccountID})
		}
		out[i].Calls++
		out[i].Tokens += e.PromptTokens + e.CompletionTokens
		if e.Status >= 400 || e.Error != "" {
			out[i].Errors++
		}
	}
	return out
}

// Path is the usage.json file, if any.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

var (
	bearerRE   = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-+/=]+`)
	headerRE   = regexp.MustCompile(`(?i)((?:x-api-key|api[_-]?key|authorization|access[_-]?token|refresh[_-]?token|id[_-]?token|session[_-]?id)\s*[:=]\s*)(\S+)`)
	jsonKeyRE  = regexp.MustCompile(`(?i)("(?:access_token|refresh_token|id_token|api_key|apiKey|authorization|password|secret|dca_token|session_id)"\s*:\s*")[^"]*(")`)
	skRE       = regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9_-]{8,}`)
	jwtRE      = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`)
	pemRE      = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	secretKeys = []string{"bearer ", "sk-", "x-api-key", "api_key", "access_token", "refresh_token", "id_token", "private key"}
)

// Redact strips bearer tokens, API keys, JWTs, and other secrets in-place.
func Redact(s string) string {
	if s == "" {
		return s
	}
	out := pemRE.ReplaceAllString(s, "[redacted]")
	out = jsonKeyRE.ReplaceAllString(out, `${1}[redacted]$2`)
	out = bearerRE.ReplaceAllString(out, `${1}[redacted]`)
	out = headerRE.ReplaceAllString(out, `${1}[redacted]`)
	out = skRE.ReplaceAllString(out, "[redacted]")
	out = jwtRE.ReplaceAllString(out, "[redacted]")
	lower := strings.ToLower(out)
	for _, key := range secretKeys {
		if strings.Contains(lower, key) && !strings.Contains(lower, "[redacted]") {
			return "[redacted]"
		}
	}
	return out
}

func clip(s string, n int) string {
	if n <= 0 {
		return s
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}
