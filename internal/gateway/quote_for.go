package gateway

import (
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/economics"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/translate"
)

// QuoteFor returns the verified quote for the deployment that served one call.
//
// The spend ledger needs this to price a call whose provider published token
// counts but no cost, which is most providers. An unusable quote is returned
// rather than an error: a call that cannot be priced is left unmeasured, and the
// caller refuses to spend in that case instead of inventing a figure.
//
// An exact model names its deployment outright -- the upstream id is the id the
// client asked for -- so the quote is looked up the same way routing looks one
// up.
//
// An automatic route names a route, not a deployment, so the account's catalog
// rows are narrowed to the ones that route may use. One candidate is still an
// unambiguous answer to "which deployment ran", because routing could only have
// chosen that one. Several candidates, and the answer is unknown: the ledger
// does not name a deployment that may not have been the one that answered.
func (g *Gateway) QuoteFor(account, clientModel string) economics.Quote {
	if account == "" {
		return economics.Quote{}
	}
	if !router.Automatic(clientModel) {
		// A -thinking-N suffix is the client's opt-in, not a different
		// deployment: routing strips it and calls the base id upstream. Looking
		// the quote up under the suffixed name missed a price that exists, so
		// every thinking call read as unmeasured -- and an unmeasured call fails
		// the ceiling closed, refusing a spend PeaProxy could have priced.
		//
		// The suffix is stripped the same way prepare strips it, so the ledger
		// prices the model that actually ran.
		base, _ := translate.SplitThinkingSuffix(clientModel)
		return priceForDeployment(g, account, base).Quote()
	}
	models := g.routeCandidatesFor(account, clientModel)
	if len(models) != 1 {
		return economics.Quote{}
	}
	return priceForDeployment(g, account, models[0]).Quote()
}

// routeCandidatesFor lists the distinct model ids an account could have served
// for one automatic route.
//
// It reads only rows that can actually answer: a configured deployment price is
// a row, and a catalog row for that account is a row. Duplicate ids collapse,
// because the same model listed twice is still one deployment to price.
func (g *Gateway) routeCandidatesFor(account, route string) []string {
	allowed := g.cfg.AutomaticRoutes.Models(route)
	eligible := func(model string) bool {
		return len(allowed) == 0 || contains(allowed, model)
	}
	seen := map[string]bool{}
	var out []string
	add := func(model string) {
		if model == "" || seen[model] || !eligible(model) {
			return
		}
		seen[model] = true
		out = append(out, model)
	}
	// A configured price keyed by account/model describes a deployment the user
	// stated, whether or not a live listing currently shows it.
	for key, quote := range g.cfg.AutomaticRoutes.Prices {
		if quote.Input == nil && quote.Output == nil {
			continue
		}
		if id, ok := splitDeploymentKey(key); ok && id.account == account {
			add(id.model)
		}
	}
	g.mu.RLock()
	rows := append([]catalog.Model(nil), g.models...)
	g.mu.RUnlock()
	for _, row := range rows {
		if row.AccountID == account {
			add(row.ID)
		}
	}
	return out
}

// deploymentKey is an "account/model" price key split into its parts.
type deploymentKey struct {
	account string
	model   string
}

// splitDeploymentKey parses the deployment-scoped form of a price key. A bare
// model id is not deployment-scoped: it names no account, so it cannot say which
// deployment a call ran on and is left to the routing lookups.
func splitDeploymentKey(key string) (deploymentKey, bool) {
	for i := 0; i < len(key); i++ {
		if key[i] != '/' {
			continue
		}
		account, model := key[:i], key[i+1:]
		if account == "" || model == "" {
			return deploymentKey{}, false
		}
		return deploymentKey{account: account, model: model}, true
	}
	return deploymentKey{}, false
}
