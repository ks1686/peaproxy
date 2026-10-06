package gateway

import (
	"bytes"
	"encoding/json"
	"github.com/ks1686/peaproxy/internal/contextopt"
	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/requestmeta"
)

// ContextOptimize prepares a request for context optimization and returns the
// bytes to send upstream.
//
// Two paths exist, and the choice between them is not a preference:
//
//   - When the client already uses tools and the provider supports them, the
//     proxy retrieval tool is offered. The model decides what it needs, so
//     PeaProxy only pays for what is actually read.
//   - Otherwise, context is gathered up front and added as labelled reference
//     material. No tool call ever exists, so a client that cannot run tools is
//     never handed one it cannot answer.
//
// Either way the caller's own request survives intact, and anything PeaProxy
// cannot do safely leaves the bytes exactly as they arrived.
func (g *Gateway) contextOptimize(raw []byte, inst instance, session string) []byte {
	if !g.contextOptimizationEnabled() || session == "" || g.Artifacts == nil {
		return raw
	}

	req := requestmeta.RequirementsFromBody(requestmeta.WireChat, raw)
	injectedTool := false
	if req.Tools && inst.Adapter != nil && inst.Adapter.Capabilities().Tools {
		if out, ok := contextopt.Inject(raw, contextopt.Plan{
			Enabled:       true,
			ClientTools:   true,
			ProviderTools: true,
			// Detected from the body rather than passed in, so no call site can
			// forget it. A streaming request that PeaProxy could only answer by
			// buffering would be answered late or not at all.
			Streaming: streamRequested(raw),
		}); ok {
			raw, injectedTool = out, true
		}
	}
	if injectedTool {
		// The tool path already covers this request; adding speculative
		// context on top would pay twice for the same material.
		return raw
	}

	out, ok := contextopt.Prefetch{
		Store:   g.Artifacts,
		Session: session,
	}.Apply(raw)
	if !ok {
		return raw
	}
	return out
}

// ContextOptimizationEnabled reports whether carried context runs at all. It
// is exported so the admin surface can show the resolved value rather than the
// raw nullable one.
func (g *Gateway) ContextOptimizationEnabled() bool { return g.contextOptimizationEnabled() }

// contextOptimizationEnabled reports whether context optimization runs at all.
//
// It follows the v3 optimization policy by default, so a user who configured
// nothing gets it, and a specific switch exists for anyone who wants carried
// context off without losing the other savings behaviours.
func (g *Gateway) contextOptimizationEnabled() bool {
	if !g.cfg.OptimizationEnabled() {
		return false
	}
	if v := g.cfg.Optimization.ContextOptimization; v != nil {
		return *v
	}
	return true
}

// Artifacts is the session store. It is nil until a store is attached, and
// every caller must treat nil as "no context available".
var _ = contextstore.New

// bodyFor is the request body to actually send upstream: the caller's body with
// prompt-cache directives applied and carried context attached.
//
// Prompt-cache edits come first because they are free and preserve the prefix;
// context optimization follows, since attaching reference material changes the
// body the cache would otherwise have keyed on.
func (g *Gateway) bodyFor(raw []byte, inst instance, session string) []byte {
	out, _ := g.bodyForScoped(raw, inst, session)
	return out
}

// bodyForScoped is bodyFor plus whether the bytes actually changed.
//
// The distinction matters for caching: an unchanged body genuinely is the same
// request from any session and may be coalesced, while a body carrying this
// session's artifacts is not the same request as an identical body from another
// session. Reporting "did it change" beats inferring it from settings, which got
// the exclusion wrong in both directions.
func (g *Gateway) bodyForScoped(raw []byte, inst instance, session string) ([]byte, bool) {
	base := g.promptBody(raw, inst.Provider.Adapter)
	out := g.contextOptimize(base, inst, session)
	return out, !bytes.Equal(out, base)
}

// streamRequested reports whether the caller asked for an incremental response.
func streamRequested(raw []byte) bool {
	var doc struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false
	}
	return doc.Stream
}

// sessionScoped reports whether this request's upstream form depends on the
// session rather than only on the caller's bytes.
//
// Context optimization injects that session's stored artifacts, and the proxy
// tool appends retrieved passages, both without changing the caller's own body.
// The response cache is keyed on the caller's body, so such a request must not
// participate: a shared entry would leak one session's context to another.
// sessionSpecificBody reports whether the bytes PeaProxy will send upstream
// differ from the caller's bytes for this request.
//
// Testing the produced body rather than the settings keeps the exclusion as
// narrow as the behaviour that requires it: a session with nothing to carry
// still caches normally, and only a request that genuinely carries session
// context is kept out of every cache.
func (g *Gateway) sessionSpecificBody(raw []byte, session string) bool {
	if !g.sessionScoped(session) {
		return false
	}
	_, changed := g.bodyForScoped(raw, instance{}, session)
	return changed
}

func (g *Gateway) sessionScoped(session string) bool {
	return session != "" && g.contextOptimizationEnabled()
}
