// Package adapter defines the provider contract.
//
//	Clients → HTTP → Router → Adapter → upstream
//
// Implementations must ListModels from the live provider — never a baked-in allowlist.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
)

// ErrNotImplemented marks spike-phase stubs (OAuth adapters, streaming, etc.).
var ErrNotImplemented = errors.New("not implemented (spike TODO)")

// ErrAuthRequired means the adapter needs login before ListModels/Chat.
var ErrAuthRequired = errors.New("authentication required")

// ErrImageModelRequired means POST /v1/images/generations omitted model.
var ErrImageModelRequired = errors.New("image generation requires a model")

// ErrModelNotImageOut means the requested model is not tagged image_out.
var ErrModelNotImageOut = errors.New("model does not support image generation")

// ErrImageOutUnsupported means the routed adapter cannot proxy image-out
// (chat-only or subscription OAuth — use an API-key OpenAI-compat path).
var ErrImageOutUnsupported = errors.New("image generation is not supported by this adapter; use an API-key OpenAI, Google, xAI, or OpenAI-compat account")

// ErrEmbeddingModelRequired means POST /v1/embeddings omitted model.
var ErrEmbeddingModelRequired = errors.New("embeddings requires a model")

// ErrModelNotEmbeddings means the requested model is not tagged embeddings.
var ErrModelNotEmbeddings = errors.New("model does not support embeddings")

// ErrEmbeddingsUnsupported means the routed adapter cannot proxy embeddings
// (chat-only or subscription OAuth — use an API-key OpenAI-compat path).
var ErrEmbeddingsUnsupported = errors.New("embeddings is not supported by this adapter; use an API-key OpenAI, Google, xAI, or OpenAI-compat account")

// Capabilities is advertised per adapter from live data where possible.
type Capabilities struct {
	Chat       bool `json:"chat"`
	Stream     bool `json:"stream"`
	VisionIn   bool `json:"visionIn"`
	ImageOut   bool `json:"imageOut"`
	Embeddings bool `json:"embeddings"`
	Tools      bool `json:"tools"`
	ListModels bool `json:"listModels"`
	OAuth      bool `json:"oauth"`
	APIKey     bool `json:"apiKey"`
	Local      bool `json:"local"`
	NeedsAuth  bool `json:"needsAuth"`
}

// ChatRequest is a provider-neutral chat call. Wire format translation lives in the server.
type ChatRequest struct {
	Model    string
	Messages []Message
	Stream   bool
	// Raw is the original client body for adapters that pass through OpenAI-compat JSON.
	Raw []byte
	// ThinkingBudget is an opt-in Anthropic budget from a -thinking-N suffix.
	// Zero leaves the body alone. Non-Claude adapters ignore it.
	ThinkingBudget int
}

// Message is a single chat turn.
type Message struct {
	Role    string
	Content string
}

// ChatResponse is a non-streaming completion.
type ChatResponse struct {
	ID       string
	Model    string
	Content  string
	Raw      []byte
	CacheHit bool
}

// AuthSession is an in-progress OAuth login (browser redirect, device code, …).
type AuthSession struct {
	Provider  string
	LoginURL  string
	State     string
	ExpiresIn int
	// UserCode and VerificationURL are set for device-code flows (Codex).
	UserCode        string
	VerificationURL string
}

// Adapter is the unit the router failsover across.
type Adapter interface {
	ID() string
	ListModels(ctx context.Context) ([]catalog.Model, error)
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	ChatStream(ctx context.Context, req ChatRequest, w io.Writer) error
	Validate(ctx context.Context) error
	Capabilities() Capabilities
}

// Authenticator is optional. OAuth adapters implement it; API-key/local adapters do not.
type Authenticator interface {
	AuthStart(ctx context.Context) (AuthSession, error)
	AuthComplete(ctx context.Context, session AuthSession, code string) error
}

// Factory builds an adapter from config fields.
type Factory func(opts Options) (Adapter, error)

// DeliveryCertainty says whether an upstream call is known to have reached
// the provider. Unspecified means a completed HTTP response was observed.
type DeliveryCertainty uint8

const (
	DeliveryUnspecified DeliveryCertainty = iota
	DeliveryUncertain
	DeliveryCompleted
)

// FailureScope is the narrowest cooldown justified by an observation.
type FailureScope string

const (
	ScopeUnspecified FailureScope = ""
	ScopeAccount     FailureScope = "account"
	ScopeModel       FailureScope = "model"
	ScopeEndpoint    FailureScope = "endpoint"
)

// HTTPError is an upstream HTTP failure. 429 is treated as retryable by the router.
// Body is for callers that already surface upstream errors. Sanitized omits it.
type HTTPError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
	Scope      FailureScope
	Delivery   DeliveryCertainty
}

func (e HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("upstream HTTP %d", e.Status)
	}
	return fmt.Sprintf("upstream HTTP %d: %s", e.Status, e.Body)
}

// Sanitized is safe for telemetry. It never includes the upstream body.
func (e HTTPError) Sanitized() string {
	return fmt.Sprintf("upstream HTTP %d", e.Status)
}

// NewHTTPError records a completed HTTP response, including Retry-After when present.
func NewHTTPError(resp *http.Response, body string) HTTPError {
	if resp == nil {
		return HTTPError{Body: body, Delivery: DeliveryUncertain, Scope: ScopeAccount}
	}
	e := HTTPError{Status: resp.StatusCode, Body: body, Delivery: DeliveryCompleted, Scope: ScopeAccount}
	e.RetryAfter = retryAfter(resp.Header, time.Now())
	switch stringsScope(resp.Header.Get("X-Ratelimit-Scope")) {
	case ScopeModel, ScopeEndpoint, ScopeAccount:
		e.Scope = stringsScope(resp.Header.Get("X-Ratelimit-Scope"))
	}
	return e
}

func stringsScope(v string) FailureScope {
	switch FailureScope(v) {
	case ScopeModel, ScopeEndpoint, ScopeAccount:
		return FailureScope(v)
	default:
		return ScopeUnspecified
	}
}

func retryAfter(h http.Header, now time.Time) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	const maxWait = time.Hour
	if sec, err := strconv.Atoi(v); err == nil {
		if sec < 0 {
			return 0
		}
		d := time.Duration(sec) * time.Second
		if d > maxWait {
			return maxWait
		}
		return d
	}
	when, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	d := when.Sub(now)
	if d < 0 {
		return 0
	}
	if d > maxWait {
		return maxWait
	}
	return d
}

// Options is the generic constructor input for adapters.
type Options struct {
	ID           string
	BaseURL      string
	APIKey       string
	SessionID    string
	Tier         catalog.Tier
	ExtraHeaders map[string]string
	OAuth        oauth.Token
	PersistOAuth func(oauth.Token) error
	// OAuthFlow is "device" for Codex device-code login; empty is loopback PKCE.
	OAuthFlow string
	// SkipLoopback builds the login URL without binding a callback port (CLI --print-url).
	SkipLoopback bool
	// ObserveHeaders is invoked with a clone of each upstream response header map
	// (chat, embeddings, images, ListModels, …). It must return quickly.
	ObserveHeaders func(http.Header)
}

// ImageRequest is a provider-neutral images call. ContentType is set for
// multipart /images/edits and empty for JSON.
type ImageRequest struct {
	Model       string
	Prompt      string
	Raw         []byte
	ContentType string
}

// ImageResponse is a non-streaming images result.
type ImageResponse struct {
	Created int64
	Model   string
	Raw     []byte
	URLs    []string
	B64     []string
}

// ImageGenerator is optional. API-key OpenAI-compat adapters that can POST
// /images/generations implement it. Subscription OAuth adapters must not.
type ImageGenerator interface {
	GenerateImage(ctx context.Context, req ImageRequest) (ImageResponse, error)
}

// ImageEditor is optional. The same image_out adapters proxy /images/edits.
type ImageEditor interface {
	EditImage(ctx context.Context, req ImageRequest) (ImageResponse, error)
}

// ParseImageResponse extracts URLs and b64 payloads from an OpenAI-shaped
// images.generations body without reserializing it.
func ParseImageResponse(raw []byte, model string) ImageResponse {
	var parsed struct {
		Created int64 `json:"created"`
		Data    []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &parsed)
	out := ImageResponse{Created: parsed.Created, Model: model, Raw: raw}
	for _, d := range parsed.Data {
		if d.URL != "" {
			out.URLs = append(out.URLs, d.URL)
		}
		if d.B64JSON != "" {
			out.B64 = append(out.B64, d.B64JSON)
		}
	}
	return out
}

// GenerateImageFrom forwards to inner when it implements ImageGenerator.
func GenerateImageFrom(inner Adapter, ctx context.Context, req ImageRequest) (ImageResponse, error) {
	gen, ok := inner.(ImageGenerator)
	if !ok {
		return ImageResponse{}, ErrImageOutUnsupported
	}
	return gen.GenerateImage(ctx, req)
}

// EditImageFrom forwards to inner when it implements ImageEditor.
func EditImageFrom(inner Adapter, ctx context.Context, req ImageRequest) (ImageResponse, error) {
	ed, ok := inner.(ImageEditor)
	if !ok {
		return ImageResponse{}, ErrImageOutUnsupported
	}
	return ed.EditImage(ctx, req)
}

// EmbeddingRequest is a provider-neutral embeddings call.
type EmbeddingRequest struct {
	Model string
	Input string
	// Raw is the original client body for adapters that pass through OpenAI-compat JSON.
	Raw []byte
}

// EmbeddingResponse is a non-streaming embeddings result.
type EmbeddingResponse struct {
	Model      string
	Raw        []byte
	Count      int
	Dimensions int
	CacheHit   bool
}

// Embedder is optional. API-key OpenAI-compat adapters that can POST
// /embeddings implement it. Subscription OAuth adapters must not.
type Embedder interface {
	CreateEmbeddings(ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error)
}

// ParseEmbeddingResponse extracts count/dimensions from an OpenAI-shaped
// embeddings body without reserializing it.
func ParseEmbeddingResponse(raw []byte, model string) EmbeddingResponse {
	var parsed struct {
		Model string `json:"model"`
		Data  []struct {
			Embedding json.RawMessage `json:"embedding"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &parsed)
	out := EmbeddingResponse{Model: model, Raw: raw, Count: len(parsed.Data)}
	if parsed.Model != "" {
		out.Model = parsed.Model
	}
	if len(parsed.Data) == 0 {
		return out
	}
	var floats []float64
	if err := json.Unmarshal(parsed.Data[0].Embedding, &floats); err == nil {
		out.Dimensions = len(floats)
	}
	return out
}

// EmbedFrom forwards to inner when it implements Embedder.
func EmbedFrom(inner Adapter, ctx context.Context, req EmbeddingRequest) (EmbeddingResponse, error) {
	emb, ok := inner.(Embedder)
	if !ok {
		return EmbeddingResponse{}, ErrEmbeddingsUnsupported
	}
	return emb.CreateEmbeddings(ctx, req)
}

// NativeMessages is implemented by adapters that speak Anthropic /v1/messages natively.
type NativeMessages interface {
	Messages(ctx context.Context, raw []byte) ([]byte, error)
	MessagesStream(ctx context.Context, raw []byte, w io.Writer) error
}

// NativeResponses is implemented by adapters that speak OpenAI /v1/responses natively
// (Codex OAuth). Other adapters get Responses→chat translation at the gateway.
type NativeResponses interface {
	Responses(ctx context.Context, raw []byte) ([]byte, error)
	ResponsesStream(ctx context.Context, raw []byte, w io.Writer) error
}

// Registry looks up adapter factories by name.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// Register adds a factory. Later Register calls with the same name replace the previous one.
func (r *Registry) Register(name string, f Factory) {
	r.factories[name] = f
}

// Open constructs an adapter by factory name.
func (r *Registry) Open(name string, opts Options) (Adapter, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, errors.New("unknown adapter: " + name)
	}
	return f(opts)
}

// Names returns registered factory names.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	return out
}
