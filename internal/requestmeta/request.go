// Package requestmeta carries bounded request metadata through the proxy path.
// It never stores request bodies or credentials.
package requestmeta

import "context"

// Wire identifies the client-facing API contract for a request.
type Wire string

const (
	WireChat       Wire = "chat"
	WireMessages   Wire = "messages"
	WireResponses  Wire = "responses"
	WireEmbeddings Wire = "embeddings"
	WireImages     Wire = "images"
)

// Requirements are facts observed from the incoming request. They do not
// claim that any particular provider supports a requested capability.
type Requirements struct {
	Tools         bool
	ParallelTools bool
	StrictSchema  bool
	Vision        bool
	Continuation  bool
}

// Request is non-secret metadata shared from the HTTP boundary to gateway code.
type Request struct {
	ID           string
	SessionID    string
	Model        string
	Wire         Wire
	Requirements Requirements
	// AnthropicBeta is the client's anthropic-beta header, normalized by
	// NormalizeAnthropicBeta. Anthropic adapters merge it into their own list.
	AnthropicBeta string
}

type contextKey struct{}

// WithRequest attaches a value copy so callers cannot mutate stored metadata.
func WithRequest(ctx context.Context, req Request) context.Context {
	return context.WithValue(ctx, contextKey{}, req)
}

// FromContext returns request metadata when the server attached it.
func FromContext(ctx context.Context) (Request, bool) {
	req, ok := ctx.Value(contextKey{}).(Request)
	return req, ok
}
