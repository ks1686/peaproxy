package gateway

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/economics"
	"github.com/ks1686/peaproxy/internal/router"
)

// RouteExplanation is why a model would route where it does, computed from the
// same state the request path uses.
//
// The point of this type is that it cannot drift. Every verdict comes from
// calling the predicate the request path calls, not from re-reading the config
// and re-deriving the answer. An explainer that duplicated routing's rules
// would be a second implementation of them, and it would report something the
// proxy does not do -- which is the failure this whole command exists to fix.
type RouteExplanation struct {
	// Model is what the caller asked for.
	Model string
	// Route is the resolved route name: a pea/* route, or the model itself
	// when it was named exactly.
	Route string
	// Automatic is true when the request goes through automatic selection.
	Automatic bool
	// Incomplete names what this explanation cannot know, so a partial answer
	// does not read as a whole one. It is empty when the answer is exact.
	Incomplete string
	// Candidates are the deployments in the order they would be tried.
	Candidates []CandidateExplanation
	// Note carries anything that would stop the request outright, with the
	// refusal the request path would produce.
	Note string
}

// CandidateExplanation is one deployment in an explanation.
type CandidateExplanation struct {
	AccountID string
	Adapter   string
	// Model is the concrete upstream id, empty on an automatic route before
	// ranking, where it is the id that would be selected.
	Model string
	// Eligible is false when a filter would remove this deployment: a
	// cooldown, an anonymous provider, a free-only rule it cannot prove, or a
	// spend ceiling already reached.
	Eligible bool
	// Reason says why, in a sentence the user can act on.
	Reason string
}

// ExplainRoute reports how a model would be routed, without dispatching it.
//
// It calls the same candidate selection, cooldown check and eligibility
// predicates the request path calls. Nothing here writes, reserves spend or
// contacts a provider, so it is safe to run against a live gateway.
//
// Two things cannot be known without a request in hand, and both are said so in
// Incomplete rather than guessed: the session a body would hash to, and the
// price ranking that orders the surviving deployments.
func (g *Gateway) ExplainRoute(ctx context.Context, model string) RouteExplanation {
	ex := RouteExplanation{Model: model, Route: model}
	if router.Automatic(model) {
		ex.Automatic = true
		return g.explainAutomatic(ctx, ex)
	}
	if session := router.SessionFrom(ctx); session != "" {
		ex.Incomplete = "session affinity is checked per request body; this explanation used no body"
	}

	cands, retry := g.candidates(model, router.SessionFrom(ctx))
	switch {
	case len(cands) > 0:
		for _, inst := range cands {
			ex.Candidates = append(ex.Candidates, CandidateExplanation{
				AccountID: inst.Provider.ID,
				Adapter:   inst.Adapter.ID(),
				Model:     inst.upstreamModel,
				Eligible:  true,
				Reason:    "serves this model and is not cooling down",
			})
		}
	case retry > 0:
		ex.Note = fmt.Sprintf("every account that serves %q is cooling down; retry in %s",
			model, retry.Round(time.Second))
		ex.Candidates = g.explainUnavailable(model)
	default:
		ex.Note = fmt.Sprintf("no account serves %q", model)
		ex.Candidates = g.explainUnavailable(model)
	}
	return ex
}

// explainAutomatic walks the same annotated-model list pickAutomatic does and
// applies the same static filters, so a deployment that a request would skip is
// named here with the filter that skipped it.
//
// Ranking is not reproduced. It depends on the request's session and on price
// comparison, neither of which exists without a body, so the surviving
// deployments are listed in catalog order and Incomplete says why that is not
// the order they would be tried in.
func (g *Gateway) explainAutomatic(ctx context.Context, ex RouteExplanation) RouteExplanation {
	if ex.Incomplete == "" {
		ex.Incomplete = "price ranking and session affinity need a request body; " +
			"the deployments below are the ones a request would consider, not the order it would try them in"
	}
	allowed := g.cfg.AutomaticRoutes.Models(ex.Route)

	g.mu.Lock()
	models := catalog.AllAnnotated(append([]catalog.Model(nil), g.models...), g.queryLocked())
	now := time.Now()
	cool := make(map[string]cooldownSlots, len(g.cool))
	for id, slots := range g.cool {
		active := cooldownSlots{wide: slots.wide}
		for m, c := range slots.models {
			if now.Before(c.Until) {
				if active.models == nil {
					active.models = map[string]Cooldown{}
				}
				active.models[m] = c
			}
		}
		if _, busy := active.anyActive(now); busy {
			cool[id] = active
		}
	}
	g.mu.Unlock()

	for _, m := range models {
		if len(allowed) > 0 && !contains(allowed, m.ID) {
			continue // not this route's model; not a verdict about this route
		}
		if !m.Routable {
			ex.add(m, false, "not routable: hidden, blocked, or unsupported on this account")
			continue
		}
		if !automaticKind(g, ex.Route, m) {
			ex.add(m, false, "outside this route's kind (pea/auto, pea/economy, pea/local, pea/free)")
			continue
		}
		if !g.cfg.AllowAnonymousProviders() && !g.runsOnThisMachine(m.AccountID, m.ID) && anonymousDeployment(g, m) {
			ex.add(m, false, "no credential attached; set optimization.allowAnonymousProviders to permit it")
			continue
		}
		if g.cfg.FreeOnly() && !freeOnlyAllows(g, m) {
			ex.add(m, false, "cannot prove a zero price, and freeOnly refuses rather than falls back"+
				freeProofDetail(g, m))
			continue
		}
		if g.cfg.SpendCeiling() > 0 && !deploymentProvenFree(g, m) {
			if blocked, reason := g.ceilingBlocks(chargedUsage()); blocked {
				ex.add(m, false, "spend ceiling: "+reason)
				continue
			}
		}
		if c, cooling := activeCooldown(cool[m.AccountID], m.ID, now); cooling {
			ex.add(m, false, fmt.Sprintf("cooling down after %s, %s left", c.Reason,
				time.Until(c.Until).Round(time.Second)))
			continue
		}
		ex.add(m, true, "eligible for this route"+freeProofDetail(g, m))
	}
	sort.Slice(ex.Candidates, func(i, j int) bool {
		if ex.Candidates[i].Eligible != ex.Candidates[j].Eligible {
			return ex.Candidates[i].Eligible
		}
		return ex.Candidates[i].AccountID+ex.Candidates[i].Model < ex.Candidates[j].AccountID+ex.Candidates[j].Model
	})
	if !anyEligible(ex.Candidates) {
		ex.Note = fmt.Sprintf("nothing is eligible for %q; a request would be refused", ex.Route)
	}
	return ex
}

func (ex *RouteExplanation) add(m catalog.Model, eligible bool, reason string) {
	ex.Candidates = append(ex.Candidates, CandidateExplanation{
		AccountID: m.AccountID,
		// Provider is the adapter family name as the catalog records it. The
		// built adapter can differ after the registry applies its aliases, so
		// this is labelled a provider rather than claimed as an adapter id.
		Adapter:  m.Provider,
		Model:    m.ID,
		Eligible: eligible,
		Reason:   reason,
	})
}

// freeProofDetail says where a deployment's price came from when freeOnly is on.
//
// The distinction matters and nothing else surfaces it: a zero the user wrote
// in their own config is accepted as free on purpose -- they turned the safety
// on and stated the fact in the same breath -- while a zero from a published
// source is a measurement. Both keep the request, for different reasons, and a
// user whose asserted price is the only thing holding a free route together
// should be able to see that before the provider changes their terms.
func freeProofDetail(g *Gateway, m catalog.Model) string {
	if !g.cfg.FreeOnly() {
		return ""
	}
	p := priceForDeployment(g, m.AccountID, m.ID)
	switch {
	case !p.Verified:
		return " (no price is known for this deployment)"
	case p.Source == economics.PriceSourceConfig:
		return " (free by your own configured price, not a published one)"
	default:
		return fmt.Sprintf(" (free by published price from %s)", p.Source)
	}
}

func anyEligible(cs []CandidateExplanation) bool {
	for _, c := range cs {
		if c.Eligible {
			return true
		}
	}
	return false
}

// explainUnavailable lists the accounts that list an exactly-named model but
// are not in the candidate set, with the reason each is out. A candidate list
// that silently omits an account is the half of the answer a user needs most,
// because the question is always "why not that one".
func (g *Gateway) explainUnavailable(model string) []CandidateExplanation {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	q := g.queryLocked()
	ids := catalog.AccountsForModel(g.models, q, model)
	var out []CandidateExplanation
	for _, id := range ids {
		inst, found := g.instanceFor(id), false
		for _, cand := range g.inst {
			if cand.Provider.ID == id {
				inst, found = cand, true
				break
			}
		}
		reason := ""
		switch {
		case !found || inst.Adapter == nil:
			reason = "no built adapter"
		default:
			if c, ok := activeCooldown(g.cool[id], model, now); ok {
				reason = fmt.Sprintf("cooling down after %s, %s left", c.Reason, time.Until(c.Until).Round(time.Second))
			} else if g.cool[id].coolingOtherModel(model, now) {
				reason = "cooling down for another model only"
			} else {
				reason = "not selected by the current failover policy"
			}
		}
		adapterID := ""
		if found && inst.Adapter != nil {
			adapterID = inst.Adapter.ID()
		}
		out = append(out, CandidateExplanation{
			AccountID: id,
			Adapter:   adapterID,
			Model:     inst.upstreamModel,
			Eligible:  false,
			Reason:    reason,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// ExplainCandidate reports what an account's adapter declares it can do.
//
// This is the adapter's own claim, not what routing believes: a user's
// `capabilities:` override corrects it. The override is what /admin/health
// reflects, so a disagreement between the two is itself the finding.
func (g *Gateway) ExplainCandidate(id string) (adapter.Capabilities, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, inst := range g.inst {
		if inst.Provider.ID == id && inst.Adapter != nil {
			return inst.Adapter.Capabilities(), true
		}
	}
	return adapter.Capabilities{}, false
}
