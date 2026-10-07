package config

import (
	"maps"
	"slices"
)

// Clone returns a deep copy of c: no pointer, slice or map is shared with c.
// nil and empty collections are preserved as they are. TestCloneCoversEveryField
// fails when a new reference-typed field is added without being handled here.
func Clone(c Config) Config {
	out := c
	out.Hide.Providers = slices.Clone(c.Hide.Providers)
	out.Hide.Models = slices.Clone(c.Hide.Models)
	out.Expose.Models = slices.Clone(c.Expose.Models)
	out.Catalog.Pin = slices.Clone(c.Catalog.Pin)
	out.Catalog.Rename = maps.Clone(c.Catalog.Rename)
	out.Failover.SessionAffinity = clonePtr(c.Failover.SessionAffinity)
	out.AutomaticRoutes.Auto = slices.Clone(c.AutomaticRoutes.Auto)
	out.AutomaticRoutes.Economy = slices.Clone(c.AutomaticRoutes.Economy)
	out.AutomaticRoutes.Local = slices.Clone(c.AutomaticRoutes.Local)
	out.AutomaticRoutes.Free = slices.Clone(c.AutomaticRoutes.Free)
	if c.AutomaticRoutes.Prices != nil {
		out.AutomaticRoutes.Prices = make(map[string]PriceQuote, len(c.AutomaticRoutes.Prices))
		for k, q := range c.AutomaticRoutes.Prices {
			q.Input = clonePtr(q.Input)
			q.Output = clonePtr(q.Output)
			q.CacheRead = clonePtr(q.CacheRead)
			q.CacheWrite = clonePtr(q.CacheWrite)
			out.AutomaticRoutes.Prices[k] = q
		}
	}
	out.Routes = maps.Clone(c.Routes)
	out.Optimization = cloneOptimization(c.Optimization)
	out.Providers = cloneProviders(c.Providers)
	return out
}

// cloneOptimization deep-copies the nullable settings. Sharing a *bool between
// a running config and its clone would let one process's reload change another
// one's view of what the user asked for.
func cloneOptimization(p OptimizationPrefs) OptimizationPrefs {
	out := p
	out.Automatic = clonePtr(p.Automatic)
	out.PromptCache = clonePtr(p.PromptCache)
	out.LocalAssistant = clonePtr(p.LocalAssistant)
	out.PersistentContext = clonePtr(p.PersistentContext)
	out.FreeOnly = clonePtr(p.FreeOnly)
	out.AllowAnonymousProviders = clonePtr(p.AllowAnonymousProviders)
	out.ContextOptimization = clonePtr(p.ContextOptimization)
	return out
}

func cloneProviders(ps []Provider) []Provider {
	if ps == nil {
		return nil
	}
	out := make([]Provider, len(ps))
	for i, p := range ps {
		out[i] = cloneProvider(p)
	}
	return out
}

func cloneProvider(p Provider) Provider {
	if p.OAuth != nil {
		tok := *p.OAuth
		tok.Extra = maps.Clone(p.OAuth.Extra)
		p.OAuth = &tok
	}
	// Capability overrides are pointers like any other nullable setting.
	// Sharing them between a running config and its clone would let a reload
	// rewrite the user's statement about their own endpoint in place.
	p.Capabilities = ProviderCapabilities{
		Tools:      clonePtr(p.Capabilities.Tools),
		VisionIn:   clonePtr(p.Capabilities.VisionIn),
		ImageOut:   clonePtr(p.Capabilities.ImageOut),
		Embeddings: clonePtr(p.Capabilities.Embeddings),
	}
	return p
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
