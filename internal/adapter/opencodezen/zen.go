// Package opencodezen is a named OpenCode Zen adapter (CPA declined #6018).
package opencodezen

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/openai_compat"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/jsonx"
)

const (
	Name           = "opencode_zen"
	DefaultBaseURL = "https://opencode.ai/zen/v1"
	// UserAgent matches the official client (opencode/<release>). Zen free
	// models reject any other caller with 403 FreeTierError.
	UserAgent = "opencode/1.18.33"
	// freeTools is the shell+read pair the Zen free-tier gate requires when the
	// caller did not send tools. Official OpenCode always declares its own tools.
	freeTools = `[{"type":"function","function":{"name":"shell","description":"Run a shell command","parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}},{"type":"function","function":{"name":"read","description":"Read a file","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}]`
)

// Adapter wraps openai_compat against OpenCode Zen and tags free/privacy models.
type Adapter struct {
	inner adapter.Adapter
	id    string
}

// New points at Zen. Prefer an API key from opencode.ai. Empty Bearer + x-session-id
// is a community path and may violate ToS — we still send sessionId if configured.
func New(opts adapter.Options) (adapter.Adapter, error) {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Tier == "" {
		opts.Tier = catalog.TierFree
	}
	session := strings.TrimSpace(opts.SessionID)
	if session == "" {
		session = newOpenCodeSessionID()
	}
	requestID := newOpenCodeRequestID()
	if opts.ExtraHeaders == nil {
		opts.ExtraHeaders = map[string]string{}
	}
	if opts.ExtraHeaders["User-Agent"] == "" {
		opts.ExtraHeaders["User-Agent"] = UserAgent
	}
	if opts.ExtraHeaders["x-opencode-client"] == "" {
		opts.ExtraHeaders["x-opencode-client"] = "cli"
	}
	if opts.ExtraHeaders["x-opencode-session"] == "" {
		opts.ExtraHeaders["x-opencode-session"] = session
	}
	if opts.ExtraHeaders["x-opencode-request"] == "" {
		opts.ExtraHeaders["x-opencode-request"] = requestID
	}
	inner, err := openai_compat.New(opts)
	if err != nil {
		return nil, err
	}
	if named, ok := inner.(*openai_compat.Adapter); ok {
		named.SetProviderName(Name)
	}
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{inner: inner, id: id}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	c := a.inner.Capabilities()
	c.APIKey = true
	return c
}

func (a *Adapter) Validate(ctx context.Context) error { return a.inner.Validate(ctx) }

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	models, err := a.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].Provider = Name
		models[i].AccountID = a.id
		models[i].Tier = catalog.TierFree
		if !looksFree(models[i].ID) && !looksFree(models[i].DisplayName) {
			models[i].Tier = catalog.TierPaid
		}
		models[i].PrivacyNote = privacyNote(models[i].ID)
	}
	return models, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	if !looksFree(requestModel(req)) {
		return a.inner.Chat(ctx, req)
	}
	raw, err := prepareFreeChat(req)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	inner, ok := a.inner.(*openai_compat.Adapter)
	if !ok {
		req.Raw = raw
		return a.inner.Chat(ctx, req)
	}
	return inner.Complete(ctx, requestModel(req), raw)
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	if looksFree(requestModel(req)) {
		raw, err := prepareFreeChat(req)
		if err != nil {
			return err
		}
		req.Raw = raw
	}
	return a.inner.ChatStream(ctx, req, w)
}

func (a *Adapter) GenerateImage(ctx context.Context, req adapter.ImageRequest) (adapter.ImageResponse, error) {
	return adapter.GenerateImageFrom(a.inner, ctx, req)
}

func (a *Adapter) EditImage(ctx context.Context, req adapter.ImageRequest) (adapter.ImageResponse, error) {
	return adapter.EditImageFrom(a.inner, ctx, req)
}

func (a *Adapter) CreateEmbeddings(ctx context.Context, req adapter.EmbeddingRequest) (adapter.EmbeddingResponse, error) {
	return adapter.EmbedFrom(a.inner, ctx, req)
}

func requestModel(req adapter.ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return jsonx.PeekBody(req.Raw).Model
}

// prepareFreeChat matches the Zen free-tier gate: stream must be true, and the
// body must declare shell and read when the caller sent no tools.
func prepareFreeChat(req adapter.ChatRequest) ([]byte, error) {
	raw := req.Raw
	if len(raw) == 0 {
		var err error
		raw, err = json.Marshal(map[string]any{
			"model":    req.Model,
			"messages": req.Messages,
		})
		if err != nil {
			return nil, err
		}
	}
	raw = jsonx.SetStream(raw, true)
	return ensureFreeTools(raw), nil
}

func ensureFreeTools(raw []byte) []byte {
	var probe struct {
		Tools json.RawMessage `json:"tools"`
	}
	if json.Unmarshal(raw, &probe) == nil {
		trimmed := bytes.TrimSpace(probe.Tools)
		if len(trimmed) > 2 && trimmed[0] == '[' {
			return raw
		}
	}
	return jsonx.SetTopLevelRaw(raw, "tools", []byte(freeTools))
}

const openCodeAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func newOpenCodeSessionID() string {
	return "ses_" + newOpenCodeSuffix()
}

func newOpenCodeRequestID() string {
	return newOpenCodeSuffix()
}

func newOpenCodeSuffix() string {
	var buf [20]byte
	_, _ = rand.Read(buf[:])
	hexPart := make([]byte, 12)
	const hexdigits = "0123456789abcdef"
	for i := 0; i < 6; i++ {
		hexPart[i*2] = hexdigits[buf[i]>>4]
		hexPart[i*2+1] = hexdigits[buf[i]&0x0f]
	}
	rest := make([]byte, 14)
	for i := range rest {
		rest[i] = openCodeAlphabet[int(buf[6+i])%len(openCodeAlphabet)]
	}
	return string(hexPart) + string(rest)
}

func looksFree(id string) bool {
	lower := strings.ToLower(id)
	if strings.HasSuffix(lower, "-free") || strings.Contains(lower, "-free-") || strings.Contains(lower, " free") {
		return true
	}
	for _, n := range []string{"big pickle", "big-pickle", "space bunny", "jev 1.13"} {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func privacyNote(id string) string {
	lower := strings.ToLower(id)
	for _, n := range []string{"nemotron", "big pickle", "big-pickle", "mimo", "muse"} {
		if strings.Contains(lower, n) {
			return "OpenCode Zen free model may use prompts for training. Review https://opencode.ai/docs/zen/ before sending private code."
		}
	}
	return ""
}
