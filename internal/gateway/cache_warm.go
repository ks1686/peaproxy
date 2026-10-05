package gateway

import (
	"time"

	"github.com/ks1686/peaproxy/internal/economics"
	"github.com/ks1686/peaproxy/internal/localassistant"
)

// cacheWarmWindow is how recent a cache read has to be to speak about the next
// turn. Provider caches are documented as living for minutes, and an hour-old
// read says little about a prefix that has grown since.
const cacheWarmWindow = 10 * time.Minute

// preferWarm returns the index of the deployment automatic routing should try
// next, given that ranked[0] is what the price logic chose.
//
// Warmth may only decide what price left open. No provider API reports cache
// state, so a recent cache read is evidence that this deployment caches and that
// the prefix was warm when it ran -- a prior about the next turn, not a fact
// about it. And a cache read usually costs a fraction of a fresh input token
// rather than nothing, so warmth must never override a genuinely cheaper
// deployment.
//
// When both candidates are priced and the warm one is no dearer, there is
// nothing left to separate them and warmth is the remaining signal. If either
// side is unpriced, warmth declines to decide: PeaProxy cannot call the warm
// candidate cheaper, and must not pretend an unknown price is a high one.
func (g *Gateway) preferWarm(ranked []instance, warm map[string]bool) int {
	if len(ranked) < 2 || len(warm) == 0 {
		return 0
	}
	current := priceForDeployment(g, ranked[0].Provider.ID, ranked[0].upstreamModel).Quote()
	for i := 1; i < len(ranked); i++ {
		cand := ranked[i]
		if cand.upstreamModel == "" || !warm[cand.Provider.ID+"\x00"+cand.upstreamModel] {
			continue
		}
		if warmthDecides(priceForDeployment(g, cand.Provider.ID, cand.upstreamModel).Quote(), current) {
			return i
		}
	}
	return 0
}

// warmthDecides reports whether price leaves the comparison open, leaving warmth
// as the deciding signal.
func warmthDecides(warm, current economics.Quote) bool {
	if warm.Input == nil || warm.Output == nil || current.Input == nil || current.Output == nil {
		return false
	}
	return *warm.Input <= *current.Input
}

// warmSet is the set of deployments that recently served a cache read.
func (g *Gateway) warmSet() map[string]bool {
	if g.Usage == nil {
		return nil
	}
	return g.Usage.CacheWarmSince(time.Now().Add(-cacheWarmWindow))
}

// LocalAssistant returns PeaProxy's own local helper, or nil when the feature
// is off, not opted in, or points somewhere that is not this machine.
//
// A nil result is ordinary and callers must carry on without it. This is the
// only place the configuration reaches the assistant, so an unusable setting
// degrades to "no helper" rather than to a request being sent somewhere.
func (g *Gateway) LocalAssistant() *localassistant.Assistant {
	prefs, ok := g.cfg.LocalAssistantConfig()
	if !ok {
		return nil
	}
	a, err := localassistant.New(localassistant.Config{
		Endpoint: prefs.Endpoint,
		Enabled:  true,
		Model:    prefs.Model,
	})
	if err != nil {
		return nil
	}
	return a
}
