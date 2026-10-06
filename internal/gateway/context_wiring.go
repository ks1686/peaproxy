package gateway

import (
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
	return g.contextOptimize(g.promptBody(raw, inst.Provider.Adapter), inst, session)
}
