package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/economics"
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
	cool := make(map[string]cooldownSlots, len(g.cool))
	for id, slots := range g.cool {
		// Copy the slots that can still bite. Keeping the whole struct would
		// share the models map with the live gateway, and the ranking below
		// reads it after the lock is dropped.
		active := cooldownSlots{wide: slots.wide}
		for model, c := range slots.models {
			if now.Before(c.Until) {
				if active.models == nil {
					active.models = map[string]Cooldown{}
				}
				active.models[model] = c
			}
		}
		if _, busy := active.anyActive(now); busy {
			cool[id] = active
		}
	}
	g.mu.Unlock()
	allowed := g.cfg.AutomaticRoutes.Models(routeName)
	var ranked []instance
	var rankedModel []string
	// Tracked separately so a freeOnly refusal can say why rather than
	// reporting the generic "no eligible model".
	var freeBlocked, freeSeen bool
	var ceilingBlocked bool
	var ceilingReason string
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
		// freeOnly is a refusal to spend, not a preference. It applies to every
		// route: a deployment that cannot show a zero price is disqualified
		// rather than treated as a fallback.
		if g.cfg.FreeOnly() {
			if freeOnlyAllows(g, m) {
				freeSeen = true
			} else {
				freeBlocked = true
				continue
			}
		}
		// The spend ceiling is a refusal to spend, so it is checked after
		// free-only: a deployment already proven free costs nothing and cannot
		// breach a ceiling. Anything else is gated, and a refusal is reported
		// rather than silently dropped from the candidates.
		if g.cfg.SpendCeiling() > 0 && !deploymentProvenFree(g, m) {
			if blocked, reason := g.ceilingBlocks(chargedUsage()); blocked {
				ceilingBlocked, ceilingReason = true, reason
				continue
			}
		}
		// Judge this account against this model. Collapsing the slots into one
		// entry first lost cooldowns: anyActive returns a single slot, so a
		// model cooling for an hour ranked as available whenever an unrelated
		// model on the same account was cooling with a sooner expiry.
		if _, cooling := activeCooldown(cool[m.AccountID], m.ID, now); cooling {
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
		if ceilingBlocked {
			return "", nil, "", errors.New(ceilingReason)
		}
		if freeBlocked && !freeSeen {
			return "", nil, "", fmt.Errorf(
				"automatic route %q has no free model: optimization.freeOnly is on and no deployment has a verified zero price",
				routeName)
		}
		return "", nil, "", fmt.Errorf("automatic route %q has no eligible model", routeName)
	}
	pick := 0
	if routeName == router.RouteEconomy {
		pick = cheapest(g, ranked)
	}
	if pinned != "" {
		for i, inst := range ranked {
			if inst.Provider.ID == pinned {
				pick = i
				break
			}
		}
	} else {
		// With no pin, a warm prompt cache may win where price could not
		// separate the candidates. It runs after the pinned check so an
		// explicit provider choice is never overridden by a saving.
		if move := g.preferWarm(ranked[pick:], g.warmSet()); move != 0 {
			pick += move
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

// automaticKind reports whether a deployment qualifies for a route. A price
// belongs to a deployment -- an account, an endpoint and a model -- so the
// lookup is scoped to the account. Looking a quote up by model id alone made
// two accounts exposing the same model share one price, which let a free
// deployment make a paid one qualify for pea/free (#D5).
func automaticKind(g *Gateway, routeName string, m catalog.Model) bool {
	switch routeName {
	case router.RouteLocal:
		// pea/local never leaves this machine. cloudFallback applies to
		// automatic routing generally; honouring it here would return a cloud
		// account for a route whose name promises the opposite.
		return router.LocalOnlyRouteAllows(m)
	case router.RouteFree:
		return priceForDeployment(g, m.AccountID, m.ID).Free()
	case router.RouteEconomy:
		p := priceForDeployment(g, m.AccountID, m.ID)
		return p.Input != nil || p.Output != nil
	default:
		return true
	}
}

// assertedPrice marks a quote the user configured rather than one a provider
// published.
//
// The quote is honoured exactly as before -- a user who states that a
// deployment is free is believed, because they may know something the catalog
// does not. What changes is that the origin is recorded, so economics can tell
// a measured free price from an asserted one and apply a stricter rule to the
// second. A price is marked Verified either way because it is the user's
// explicit statement, not a guess; Source is what distinguishes them.
func assertedPrice(q config.PriceQuote) catalog.Price {
	return catalog.Price{
		Input:    q.Input,
		Output:   q.Output,
		Currency: "USD",
		Source:   PriceSourceConfig,
		Verified: q.Verified,
	}
}

// PriceSourceConfig marks a price that came from user configuration rather than
// from a provider's published pricing.
const PriceSourceConfig = "config"

// freeOnlyAllows reports whether a deployment may be used when the user has
// forbidden spending.
//
// The bar is a verified zero price and nothing else. "Nobody has looked" is not
// free, and a nonzero price is the opposite of free, so both fail here without
// needing a judgement call.
//
// A price the user configured counts. They asserted it, and asserting that your
// own account is free is exactly the knowledge the catalog lacks -- a promo
// credit or a contracted rate appears nowhere in ListModels. The provenance
// survives so a surface promising safety can still tell an assertion from a
// published figure.
func freeOnlyAllows(g *Gateway, m catalog.Model) bool {
	return deploymentProvenFree(g, m)
}

// deploymentProvenFree reports whether a deployment's price is verified and
// zero.
//
// Both free-only routing and the spend ceiling need this same judgement, and
// naming it separately keeps it honest: neither is allowed to treat an unpriced
// deployment as free, and neither should have to infer that from a helper named
// after one of its callers.
func deploymentProvenFree(g *Gateway, m catalog.Model) bool {
	p := priceForDeployment(g, m.AccountID, m.ID)
	return p.Verified && p.Currency == economics.LedgerCurrency && p.Free()
}

// chargedUsage marks a request as one that may cost money, for the checks that
// only care whether a call is free rather than what it will cost.
//
// The token counts are left unknown on purpose: PeaProxy cannot state the
// output length before the model has answered, and a partial estimate used as
// a complete one is exactly the error this project exists to avoid.
func chargedUsage() economics.Usage { return economics.Usage{Unknown: true} }

// priceForDeployment returns the verified quote for one deployment. An explicit
// user quote may be keyed by "account/model" and wins over the catalog row for
// that same account; a quote keyed by bare model id stays available for configs
// that predate deployment-scoped quotes.
func priceForDeployment(g *Gateway, account, model string) catalog.Price {
	if account != "" {
		if quote, ok := g.cfg.AutomaticRoutes.Prices[account+"/"+model]; ok {
			return assertedPrice(quote)
		}
	}
	if quote, ok := g.cfg.AutomaticRoutes.Prices[model]; ok {
		return assertedPrice(quote)
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, row := range g.models {
		if row.ID != model || !row.Price.Verified {
			continue
		}
		if account != "" && row.AccountID != account {
			continue
		}
		return row.Price
	}
	return catalog.Price{}
}

// cheapest returns the index of the least expensive deployment. It compares
// deployments, not model ids: two accounts serving the same id can be quoted
// differently, and comparing ids would rank them by whichever row came first.
func cheapest(g *Gateway, ranked []instance) int {
	best := -1
	for i, inst := range ranked {
		price := priceForDeployment(g, inst.Provider.ID, inst.upstreamModel)
		if price.Input == nil && price.Output == nil {
			continue
		}
		if best < 0 || catalog.Cheaper(price, priceForDeployment(g, ranked[best].Provider.ID, ranked[best].upstreamModel)) {
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
