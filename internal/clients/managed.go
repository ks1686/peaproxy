package clients

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/ks1686/peaproxy/internal/fslock"
)

// ErrConflict means the file changed after it was read.
var ErrConflict = errors.New("client config changed during connect")

// beforeWrite is a test hook that runs after the source is read and before it is compared again.
var beforeWrite = func(string) error { return nil }

// ErrUnknownClient means the name is not a managed integration.
var ErrUnknownClient = errors.New("unknown managed client")

// ErrGuidedSetup means the harness has no writable config. Callers show the preset snippet.
var ErrGuidedSetup = errors.New("guided setup")

// ErrInvalidInput means the base URL or model cannot be recorded safely.
var ErrInvalidInput = errors.New("invalid connect input")

// ErrUnexpectedShape means a key PeaProxy merges into holds a non-object value.
var ErrUnexpectedShape = errors.New("client config has unexpected shape")

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
	for _, name := range []string{"opencode", "pi", "continue", "codex", "claude-code"} {
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

// Connectable reports whether Layout can write this client's config, which is
// what decides if the admin API and the web UI offer a Connect button for it.
// The UI used to keep its own list of names, and Pi was missing from it.
func Connectable(name string) bool {
	return (Layout{}).path(name) != ""
}

func (l Layout) path(name string) string {
	switch name {
	case "opencode":
		return filepath.Join(l.Root, "opencode.json")
	case "pi":
		// Pi reads PI_CODING_AGENT_DIR when it is set, so that is where its
		// provider list has to land. Without it the layout root is the story.
		if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
			return filepath.Join(dir, "models.json")
		}
		return filepath.Join(l.Root, ".pi", "agent", "models.json")
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
	path := l.path(name)
	if path == "" {
		return ErrUnknownClient
	}
	if err := validateConnect(baseURL, model); err != nil {
		return err
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
	if bytes.Equal(updated, raw) {
		return nil
	}
	return writeAtomic(path, updated)
}

func validateConnect(baseURL, model string) error {
	u, err := url.Parse(baseURL)
	if unsafeText(baseURL) || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%w: base URL must be an http(s) URL without quotes, backslashes or control characters", ErrInvalidInput)
	}
	if len(model) > 256 || unsafeText(model) {
		return fmt.Errorf("%w: model must be at most 256 bytes without quotes, backslashes or control characters", ErrInvalidInput)
	}
	return nil
}

func unsafeText(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == '"' || c == '\\' {
			return true
		}
	}
	return false
}

func hash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func insertOwned(name string, raw []byte, baseURL, model string) ([]byte, error) {
	baseURL = gatewayURL(name, baseURL)
	switch name {
	case "codex":
		return insertCodex(raw, baseURL, model), nil
	case "continue":
		return insertContinue(raw, baseURL, model), nil
	case "pi":
		return insertPi(raw, baseURL)
	case "claude-code":
		return insertClaudeCode(raw, baseURL, model)
	default:
		return nil, ErrGuidedSetup
	}
}

// openAIWire are the clients that append their own path to the base URL, so
// theirs needs the /v1 and Claude Code's must not have it. --origin accepts
// either form and this decides which one each client gets.
var openAIWire = map[string]bool{"codex": true, "continue": true, "pi": true}

// gatewayURL is the address to write into a client config: the bare origin plus
// the suffix that client's wire needs.
func gatewayURL(name, origin string) string {
	base := NormalizeOrigin(origin)
	if base == "" {
		return ""
	}
	if openAIWire[name] {
		return base + "/v1"
	}
	return base
}

func removeOwned(name string, raw []byte) ([]byte, error) {
	switch name {
	case "codex":
		return []byte(removeCodex(string(raw))), nil
	case "continue":
		return []byte(removeContinue(string(raw))), nil
	case "pi":
		return removePi(raw)
	case "claude-code":
		return removeClaudeCode(raw)
	default:
		return removeJSON(raw)
	}
}

const ownedAPIKey = "peaproxy"

// legacyOwnedKey is the top-level key connect wrote before v2.0.10. Neither
// OpenCode nor Claude Code ever read it; disconnect still removes it.
const legacyOwnedKey = "peaproxy"

var piOverrides = []struct {
	provider string
	trimV1   bool
}{
	{"anthropic", true},
	{"openai", false},
}

var claudeCodeEnv = []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL"}

func insertClaudeCode(raw []byte, baseURL, model string) ([]byte, error) {
	body := bytes.TrimSpace(stripJSONC(raw))
	if len(body) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) {
		return nil, errors.New("client config is not json")
	}
	body, err := deleteJSONKey(body, legacyOwnedKey)
	if err != nil {
		return nil, err
	}
	env, err := objectOrEmpty(body, "env", "claude-code settings: env")
	if err != nil {
		return nil, err
	}
	values := map[string]string{
		"ANTHROPIC_BASE_URL": baseURL,
		"ANTHROPIC_API_KEY":  ownedAPIKey,
		"ANTHROPIC_MODEL":    model,
	}
	for _, key := range claudeCodeEnv {
		if values[key] == "" {
			if env, err = deleteJSONKey(env, key); err != nil {
				return nil, err
			}
			continue
		}
		if env, err = upsertJSONKey(env, key, values[key]); err != nil {
			return nil, err
		}
	}
	next, err := upsertJSONKey(body, "env", json.RawMessage(env))
	if err != nil {
		return nil, err
	}
	return formatJSON(raw, next), nil
}

func removeClaudeCode(raw []byte) ([]byte, error) {
	body := bytes.TrimSpace(stripJSONC(raw))
	if len(body) == 0 {
		return raw, nil
	}
	if !json.Valid(body) {
		return nil, errors.New("client config is not json")
	}
	_, legacy := getJSONKey(body, legacyOwnedKey)
	env, ok := getJSONKey(body, "env")
	key, _ := getJSONKey(env, "ANTHROPIC_API_KEY")
	owned := ok && string(key) == `"`+ownedAPIKey+`"`
	if !legacy && !owned {
		return raw, nil
	}
	body, err := deleteJSONKey(body, legacyOwnedKey)
	if err != nil {
		return nil, err
	}
	if owned {
		for _, k := range claudeCodeEnv {
			if env, err = deleteJSONKey(env, k); err != nil {
				return nil, err
			}
		}
		if string(env) == "{}" {
			body, err = deleteJSONKey(body, "env")
		} else {
			body, err = upsertJSONKey(body, "env", json.RawMessage(env))
		}
		if err != nil {
			return nil, err
		}
	}
	return formatJSON(raw, body), nil
}

func insertPi(raw []byte, baseURL string) ([]byte, error) {
	body := bytes.TrimSpace(stripJSONC(raw))
	if len(body) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) {
		return nil, errors.New("client config is not json")
	}
	providers, err := objectOrEmpty(body, "providers", "pi models: providers")
	if err != nil {
		return nil, err
	}
	for _, o := range piOverrides {
		entry, err := objectOrEmpty(providers, o.provider, "pi models: providers."+o.provider)
		if err != nil {
			return nil, err
		}
		url := baseURL
		if o.trimV1 {
			url = strings.TrimSuffix(strings.TrimRight(url, "/"), "/v1")
		}
		if entry, err = upsertJSONKey(entry, "baseUrl", url); err != nil {
			return nil, err
		}
		if entry, err = upsertJSONKey(entry, "apiKey", ownedAPIKey); err != nil {
			return nil, err
		}
		if providers, err = upsertJSONKey(providers, o.provider, json.RawMessage(entry)); err != nil {
			return nil, err
		}
	}
	next, err := upsertJSONKey(body, "providers", json.RawMessage(providers))
	if err != nil {
		return nil, err
	}
	return formatJSON(raw, next), nil
}

func removePi(raw []byte) ([]byte, error) {
	body := bytes.TrimSpace(stripJSONC(raw))
	if len(body) == 0 {
		return raw, nil
	}
	if !json.Valid(body) {
		return nil, errors.New("client config is not json")
	}
	providers, ok := getJSONKey(body, "providers")
	if !ok {
		return raw, nil
	}
	changed := false
	for _, o := range piOverrides {
		entry, ok := getJSONKey(providers, o.provider)
		if !ok {
			continue
		}
		key, _ := getJSONKey(entry, "apiKey")
		if string(key) != `"`+ownedAPIKey+`"` {
			continue
		}
		changed = true
		var err error
		if entry, err = deleteJSONKey(entry, "baseUrl"); err != nil {
			return nil, err
		}
		if entry, err = deleteJSONKey(entry, "apiKey"); err != nil {
			return nil, err
		}
		if string(entry) == "{}" {
			providers, err = deleteJSONKey(providers, o.provider)
		} else {
			providers, err = upsertJSONKey(providers, o.provider, json.RawMessage(entry))
		}
		if err != nil {
			return nil, err
		}
	}
	// Account providers imported by importPi are PeaProxy-owned end to end, so
	// disconnect drops the whole block, but only when it still carries the owned
	// key. A provider a user edited into a different shape is left alone.
	for _, name := range ownedPiProviders(providers) {
		entry, _ := getJSONKey(providers, name)
		key, _ := getJSONKey(entry, "apiKey")
		if string(key) != `"`+ownedAPIKey+`"` {
			continue
		}
		changed = true
		var err error
		if providers, err = deleteJSONKey(providers, name); err != nil {
			return nil, err
		}
	}
	if !changed {
		return raw, nil
	}
	next, err := upsertJSONKey(body, "providers", json.RawMessage(providers))
	if err != nil {
		return nil, err
	}
	return formatJSON(raw, next), nil
}

// objectOrEmpty returns body[key] when it is an object, {} when it is missing
// or null, and ErrUnexpectedShape otherwise so user data is never replaced.
func objectOrEmpty(body []byte, key, what string) ([]byte, error) {
	value, ok := getJSONKey(body, key)
	switch {
	case !ok || string(value) == "null":
		return []byte("{}"), nil
	case bytes.HasPrefix(value, []byte("{")):
		return value, nil
	default:
		return nil, fmt.Errorf("%s is not an object: %w", what, ErrUnexpectedShape)
	}
}

// formatJSON finishes a JSON client config: the writers that produced next
// already kept the file's own layout, so all that is left is its
// trailing-newline choice and CRLF line endings, plus hoisting its comments.
//
// It deliberately does not re-indent. Re-indenting the whole document is what
// used to reflow the parts of a file PeaProxy has no business reformatting, a
// compact array included, and none of it came back the way it went in.
func formatJSON(raw, next []byte) []byte {
	out := next
	if _, multiline := detectIndent(stripJSONC(raw)); !multiline || bytes.HasSuffix(raw, []byte("\n")) {
		out = append(out, '\n')
	}
	// Untouched values keep the file's own line endings, so fold whatever
	// endings are in out down to \n first and then put the file's back, rather
	// than doubling the \r on a line that already had one.
	out = bytes.ReplaceAll(out, []byte("\r\n"), []byte("\n"))
	if bytes.Contains(raw, []byte("\r\n")) {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return append(commentPrefix(raw), out...)
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
	if _, ok := getJSONKey(body, legacyOwnedKey); !ok {
		return raw, nil
	}
	next, err := deleteJSONKey(body, legacyOwnedKey)
	if err != nil {
		return nil, err
	}
	return formatJSON(raw, next), nil
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
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
	}
	trailing := strings.HasSuffix(text, eol) || strings.HasSuffix(text, "\n")
	body := strings.TrimRight(text, "\r\n")
	block := "[model_providers.peaproxy]" + eol +
		"name = \"PeaProxy\"" + eol +
		"base_url = " + quote(baseURL) + eol +
		"model = " + quote(model) + eol
	if body == "" {
		return []byte(block)
	}
	// A blank line before the block, so it reads as its own table, and no
	// trailing newline added to a file that had none.
	out := body + eol + eol + block
	if !trailing {
		out = strings.TrimSuffix(out, eol)
	}
	return []byte(out)
}

func removeCodex(text string) string {
	// The block may sit at the end of the file, where the split leaves an empty
	// final element that gets skipped along with it. The file's own
	// trailing-newline choice is restored afterwards so a round trip gives back
	// what it started with.
	trailing := strings.HasSuffix(text, "\n")
	lines := strings.Split(text, "\n")
	var out []string
	skip := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			skip = trim == "[model_providers.peaproxy]"
			if skip {
				// Take the blank line the insertion added with it, so a
				// connect/disconnect round trip is byte for byte.
				if n := len(out); n > 0 && strings.TrimSpace(out[n-1]) == "" {
					out = out[:n-1]
				}
			}
		}
		if skip {
			continue
		}
		out = append(out, line)
	}
	joined := strings.Join(out, "\n")
	if trailing && !strings.HasSuffix(joined, "\n") {
		joined += "\n"
	}
	return joined
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

// quote encodes s as a JSON string, which is also a valid TOML basic string
// and YAML double-quoted scalar.
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// writeAtomic replaces a harness config file. No lock: the file is the
// harness's, and the server holds its own lock around every change it makes to
// one; all that is left is two peaproxy processes racing, which a unique temp
// name per write settles.
func writeAtomic(path string, raw []byte) error {
	return fslock.WriteAtomic(path, raw, 0o600)
}
