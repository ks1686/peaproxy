package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/promptcache"
	"github.com/ks1686/peaproxy/internal/requestmeta"
	"github.com/ks1686/peaproxy/internal/responsecache"
	"github.com/ks1686/peaproxy/internal/router"
)

func (g *Gateway) promptBody(raw []byte, adapterName string) []byte {
	mode, err := promptcache.NormalizeMode(g.cfg.RequestEngine.PromptCache)
	if err != nil {
		return raw
	}
	out, err := promptcache.Apply(raw, mode, promptcache.ProfileForAdapter(adapterName))
	if err != nil || out == nil {
		return raw
	}
	return out
}

func (g *Gateway) routeResolved(ctx context.Context, raw []byte, model string) (string, []instance, string, error) {
	if !router.Automatic(model) {
		cands, session, err := g.route(ctx, raw, model)
		return model, cands, session, err
	}
	if !g.cfg.AutomaticRoutes.Enabled {
		return "", nil, "", fmt.Errorf("automatic route %q is disabled", model)
	}
	return g.pickAutomatic(ctx, raw, model)
}

func (g *Gateway) pickAutomatic(ctx context.Context, raw []byte, routeName string) (string, []instance, string, error) {
	req, _ := requestmeta.FromContext(ctx)
	req.Requirements = mergeRequirements(req.Requirements, requestmeta.RequirementsFromBody(req.Wire, raw))
	session := router.SessionFrom(ctx)
	if session == "" {
		session = router.SessionFromBody(raw)
	}
	if session == "" {
		session = req.SessionID
	}
	if bind, ok := g.continuationByID(previousResponseID(raw)); ok {
		inst := g.instanceFor(bind.Account)
		if inst.Adapter != nil {
			return bind.Model, []instance{inst}, session, nil
		}
	}
	g.mu.Lock()
	now := time.Now()
	models := catalog.AllAnnotated(append([]catalog.Model(nil), g.models...), g.queryLocked())
	pinned, _ := g.liveAffinityLocked(session, routeName, now)
	cool := make(map[string]Cooldown, len(g.cool))
	for id, c := range g.cool {
		if now.Before(c.Until) {
			cool[id] = c
		}
	}
	g.mu.Unlock()
	allowed := g.cfg.AutomaticRoutes.Models(routeName)
	var ranked []instance
	var rankedModel []string
	for _, m := range models {
		if !m.Routable {
			continue
		}
		if skipAutomaticModality(req.Wire, m) {
			continue
		}
		if len(allowed) > 0 && !contains(allowed, m.ID) {
			continue
		}
		if !automaticKind(g, routeName, m) {
			continue
		}
		if c, ok := cool[m.AccountID]; ok && (c.Model == "" || c.Model == m.ID) {
			continue
		}
		inst := g.instanceFor(m.AccountID)
		if inst.Adapter == nil {
			continue
		}
		if !eligibleForAutomaticRoute(evidenceFor(inst), req.Requirements) {
			continue
		}
		inst.upstreamModel = m.ID
		ranked = append(ranked, inst)
		rankedModel = append(rankedModel, m.ID)
	}
	if len(ranked) == 0 {
		return "", nil, "", fmt.Errorf("automatic route %q has no eligible model", routeName)
	}
	pick := 0
	if routeName == router.RouteEconomy {
		pick = cheapest(g, rankedModel)
	}
	if pinned != "" {
		for i, inst := range ranked {
			if inst.Provider.ID == pinned {
				pick = i
				break
			}
		}
	}
	if pick != 0 {
		ranked = append(append([]instance{}, ranked[pick:]...), ranked[:pick]...)
		rankedModel = append(append([]string{}, rankedModel[pick:]...), rankedModel[:pick]...)
	}
	return rankedModel[0], ranked, session, nil
}

func skipAutomaticModality(wire requestmeta.Wire, m catalog.Model) bool {
	switch wire {
	case "", requestmeta.WireChat, requestmeta.WireMessages, requestmeta.WireResponses:
		return catalog.HasModality(m, "embeddings") || catalog.HasModality(m, "image_out")
	default:
		return false
	}
}

func (g *Gateway) instanceFor(account string) instance {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, inst := range g.inst {
		if inst.Provider.ID == account {
			return inst
		}
	}
	return instance{}
}

func automaticKind(g *Gateway, routeName string, m catalog.Model) bool {
	switch routeName {
	case router.RouteLocal:
		if m.Tier == catalog.TierLocal {
			return true
		}
		return g.cfg.AutomaticRoutes.CloudFallback
	case router.RouteFree:
		return g.priceFor(m.ID).Free()
	case router.RouteEconomy:
		return g.priceFor(m.ID).Input != nil || g.priceFor(m.ID).Output != nil
	default:
		return true
	}
}

func (g *Gateway) priceFor(model string) catalog.Price {
	if quote, ok := g.cfg.AutomaticRoutes.Prices[model]; ok {
		return catalog.Price{Input: quote.Input, Output: quote.Output, Verified: quote.Verified}
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, row := range g.models {
		if row.ID == model && row.Price.Verified {
			return row.Price
		}
	}
	return catalog.Price{}
}

func cheapest(g *Gateway, models []string) int {
	best := -1
	for i, id := range models {
		price := g.priceFor(id)
		if price.Input == nil && price.Output == nil {
			continue
		}
		if best < 0 || catalog.Cheaper(price, g.priceFor(models[best])) {
			best = i
		}
	}
	if best < 0 {
		return 0
	}
	return best
}

func evidenceFor(inst instance) catalog.CapabilityEvidence {
	var evidence catalog.CapabilityEvidence
	if inst.Adapter == nil {
		return evidence
	}
	caps := inst.Adapter.Capabilities()
	if caps.Tools {
		evidence.Tools = catalog.SupportYes
	}
	if caps.VisionIn {
		evidence.Vision = catalog.SupportYes
	}
	return catalog.FillUnknown(promptcache.ProfileForAdapter(inst.Provider.Adapter), evidence)
}

func filterExactLocal(model string, models []catalog.Model, cands []instance) []instance {
	local := map[string]bool{}
	for _, m := range models {
		if m.ID != model || m.Tier != catalog.TierLocal {
			continue
		}
		local[m.AccountID] = true
	}
	if len(local) == 0 {
		return cands
	}
	out := make([]instance, 0, len(cands))
	for _, cand := range cands {
		if local[cand.Provider.ID] {
			out = append(out, cand)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

func mergeRequirements(a, b requestmeta.Requirements) requestmeta.Requirements {
	a.Tools = a.Tools || b.Tools
	a.ParallelTools = a.ParallelTools || b.ParallelTools
	a.StrictSchema = a.StrictSchema || b.StrictSchema
	a.Vision = a.Vision || b.Vision
	a.Continuation = a.Continuation || b.Continuation
	return a
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func (g *Gateway) cachedChat(account, endpoint, model string, raw []byte) (adapter.ChatResponse, bool) {
	if !g.cfg.RequestEngine.CacheResponses || !responsecache.Eligible("chat", raw, true) {
		return adapter.ChatResponse{}, false
	}
	body, ok := g.responseStore().Get(responsecache.Key(account, endpoint, model, "chat", raw), time.Now())
	if !ok {
		return adapter.ChatResponse{}, false
	}
	return adapter.ChatResponse{Raw: body, CacheHit: true}, true
}

func (g *Gateway) storeChat(account, endpoint, model string, raw, response []byte) {
	if !g.cfg.RequestEngine.CacheResponses || !responsecache.Eligible("chat", raw, true) {
		return
	}
	g.responseStore().Put(responsecache.Key(account, endpoint, model, "chat", raw), response, time.Now())
}

func (g *Gateway) cachedEmbeddings(account, endpoint, model string, raw []byte) (adapter.EmbeddingResponse, bool) {
	if !g.cfg.RequestEngine.CacheEmbeddings || !responsecache.Eligible("embeddings", raw, true) {
		return adapter.EmbeddingResponse{}, false
	}
	body, ok := g.responseStore().Get(responsecache.Key(account, endpoint, model, "embeddings", raw), time.Now())
	if !ok {
		return adapter.EmbeddingResponse{}, false
	}
	return adapter.EmbeddingResponse{Raw: body, CacheHit: true}, true
}

func (g *Gateway) storeEmbeddings(account, endpoint, model string, raw, response []byte) {
	if !g.cfg.RequestEngine.CacheEmbeddings || !responsecache.Eligible("embeddings", raw, true) {
		return
	}
	g.responseStore().Put(responsecache.Key(account, endpoint, model, "embeddings", raw), response, time.Now())
}

func (g *Gateway) responseStore() *responsecache.Cache {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.responses == nil {
		g.responses = responsecache.New(0, 0, 0)
	}
	return g.responses
}
