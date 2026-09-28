package clients

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrConflict means the file changed after it was read.
var ErrConflict = errors.New("client config changed during connect")

// beforeWrite is a test hook that runs after the source is read and before it is compared again.
var beforeWrite = func(string) error { return nil }

// ErrUnknownClient means the name is not a managed integration.
var ErrUnknownClient = errors.New("unknown managed client")

// ErrGuidedSetup means the harness has no writable config. Callers show the preset snippet.
var ErrGuidedSetup = errors.New("guided setup")

// Layout resolves config paths under root instead of the real home directory.
type Layout struct {
	Root string
}

// ManagedClient is a harness whose config PeaProxy can edit transactionally.
type ManagedClient struct {
	Name string
	Path string
}

// Detect lists managed clients that have a config file under the layout.
func (l Layout) Detect() []ManagedClient {
	var out []ManagedClient
	for _, name := range []string{"opencode", "continue", "codex", "claude-code"} {
		path := l.path(name)
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			out = append(out, ManagedClient{Name: name, Path: path})
		}
	}
	return out
}

func (l Layout) path(name string) string {
	switch name {
	case "opencode":
		return filepath.Join(l.Root, "opencode.json")
	case "continue":
		return filepath.Join(l.Root, ".continue", "config.yaml")
	case "codex":
		return filepath.Join(l.Root, ".codex", "config.toml")
	case "claude-code":
		return filepath.Join(l.Root, ".claude", "settings.json")
	default:
		return ""
	}
}

// Connect records a PeaProxy provider without removing unrelated keys.
func (l Layout) Connect(name, baseURL, model string) error {
	if name == "pi" {
		return ErrGuidedSetup
	}
	path := l.path(name)
	if path == "" {
		return ErrUnknownClient
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	fingerprint := hash(raw)
	updated, err := insertOwned(name, raw, baseURL, model)
	if err != nil {
		return err
	}
	if err := beforeWrite(path); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if hash(current) != fingerprint {
		return ErrConflict
	}
	return writeAtomic(path, updated)
}

// Disconnect removes only PeaProxy-owned keys.
func (l Layout) Disconnect(name string) error {
	path := l.path(name)
	if path == "" {
		return ErrUnknownClient
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated, err := removeOwned(name, raw)
	if err != nil {
		return err
	}
	return writeAtomic(path, updated)
}

func hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func insertOwned(name string, raw []byte, baseURL, model string) ([]byte, error) {
	switch name {
	case "codex":
		return insertCodex(raw, baseURL, model), nil
	case "continue":
		return insertContinue(raw, baseURL, model), nil
	default:
		return insertJSON(raw, baseURL, model)
	}
}

func removeOwned(name string, raw []byte) ([]byte, error) {
	switch name {
	case "codex":
		return []byte(removeCodex(string(raw))), nil
	case "continue":
		return []byte(removeContinue(string(raw))), nil
	default:
		return removeJSON(raw)
	}
}

func insertJSON(raw []byte, baseURL, model string) ([]byte, error) {
	body := bytes.TrimSpace(stripJSONC(raw))
	if len(body) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) {
		return nil, errors.New("client config is not json")
	}
	next, err := upsertJSONKey(body, "peaproxy", map[string]any{"baseURL": baseURL, "model": model})
	if err != nil {
		return nil, err
	}
	return append(commentPrefix(raw), append(next, '\n')...), nil
}

func commentPrefix(raw []byte) []byte {
	var comments []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			comments = append(comments, line)
		}
	}
	if len(comments) == 0 {
		return nil
	}
	return []byte(strings.Join(comments, "\n") + "\n")
}

func removeJSON(raw []byte) ([]byte, error) {
	body := bytes.TrimSpace(stripJSONC(raw))
	if len(body) == 0 {
		return raw, nil
	}
	if !json.Valid(body) {
		return nil, errors.New("client config is not json")
	}
	next, err := deleteJSONKey(body, "peaproxy")
	if err != nil {
		return nil, err
	}
	return append(commentPrefix(raw), append(next, '\n')...), nil
}

func stripJSONC(raw []byte) []byte {
	lines := strings.Split(string(raw), "\n")
	var kept []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "//") {
			continue
		}
		kept = append(kept, line)
	}
	return []byte(strings.Join(kept, "\n"))
}

func insertCodex(raw []byte, baseURL, model string) []byte {
	text := removeCodex(string(raw))
	block := "\n[model_providers.peaproxy]\nname = \"PeaProxy\"\nbase_url = " + quote(baseURL) + "\nmodel = " + quote(model) + "\n"
	return []byte(strings.TrimRight(text, "\n") + block)
}

func removeCodex(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	skip := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			skip = trim == "[model_providers.peaproxy]"
		}
		if skip {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func insertContinue(raw []byte, baseURL, model string) []byte {
	text := strings.TrimRight(removeContinue(string(raw)), "\n")
	block := "  # peaproxy-owned-start\n  - name: PeaProxy\n    provider: openai\n    model: " + quote(model) + "\n    apiBase: " + quote(baseURL) + "\n    apiKey: peaproxy\n  # peaproxy-owned-end\n"
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "models:" {
			out := append([]string{}, lines[:i+1]...)
			out = append(out, strings.TrimRight(block, "\n"))
			out = append(out, lines[i+1:]...)
			return []byte(strings.Join(out, "\n") + "\n")
		}
	}
	if text != "" {
		text += "\n"
	}
	return []byte(text + "models:\n" + block)
}

func removeContinue(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	skip := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "# peaproxy-owned-start" {
			skip = true
			continue
		}
		if skip {
			if trim == "# peaproxy-owned-end" {
				skip = false
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func writeAtomic(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
