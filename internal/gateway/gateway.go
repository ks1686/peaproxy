package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/contextstore"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/localruntime"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/quota"
	"github.com/ks1686/peaproxy/internal/responsecache"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/streamguard"
	"github.com/ks1686/peaproxy/internal/usage"
)

const CooldownTTL = 30 * time.Second

// cooldownTTL is the skip window after a retryable failure. Tests may shorten it.
var cooldownTTL = CooldownTTL

// Gateway owns config, live adapters, catalog, and usage.
type Gateway struct {
	mu   sync.RWMutex
	cfg  config.Config
	path string
	reg  *adapter.Registry
	inst []instance
	// buildEpoch counts rebuilds, so a token saved by an adapter from an
	// earlier build can tell that the live adapters were made without it.
	buildEpoch uint64
	models     []catalog.Model

	// Artifacts holds context PeaProxy gathered earlier in the session. Nil
	// means no context store is attached, and every consumer must treat that as
	// "nothing to offer" rather than as empty.
	Artifacts *contextstore.Store

	// spendWindow supplies recorded spend for the spend ceiling. It is a field
	// so the ceiling can be exercised without a usage store.
	spendWindow spendWindow
	Usage       *usage.Store
	cool        map[string]cooldownSlots
	// recovering holds accounts whose whole-account cooldown expired and that
	// have not answered a request since. Each request takes the half-open probe
	// before using one, so a recovering account sees one request, not a herd.
	recovering    map[string]struct{}
	rr            uint64
	sticky        map[string]string
	affinity      map[string]affinityBind
	continuations map[string]continuationBind
	accountStats  map[string]router.AccountStat
	admission     *router.Gate
	responses     *responsecache.Cache
	flight        *responsecache.Flight
	health        []AdapterHealth
	quota         *quota.Store
	refreshSeq    uint64
	refreshCommit uint64
	// saved is the merge base: what cfg looked like at the last save (adopted
	// fields as merged from disk, the rest as they were in cfg). It is only
	// ever assigned clones, never a value that shares memory with cfg.
	saved          config.Config
	lastPersistErr string
	// lastReloadErr is the same idea for the config watcher: one line per
	// distinct failure, cleared by the next reload that works.
	lastReloadErr string
	// watchSig is the config file as New left it. The watcher goroutine starts
	// later — after the first catalog refresh, and whenever the runtime gets
	// to it — so a stat taken then can already include a write that memory
	// does not have. That write would become the baseline and never be adopted.
	watchSig    fileSig
	watchSigSet bool
	// saveMu serialises mutate+persist. It is taken before mu and is never
	// held across Refresh or any adapter call, so a PersistOAuth callback from
	// ListModels cannot deadlock.
	saveMu sync.Mutex
}

// Cooldown is a temporary skip of an account after a retryable failure.
type Cooldown struct {
	AccountID   string    `json:"accountId"`
	Until       time.Time `json:"until"`
	Reason      string    `json:"reason"`
	RemainingMs int64     `json:"remainingMs"`
	QuotaHint   string    `json:"quotaHint,omitempty"`
	Model       string    `json:"model,omitempty"`
}

// AdapterHealth is last ListModels/Validate status for the Health UI.
type AdapterHealth struct {
	AccountID    string               `json:"accountId"`
	Adapter      string               `json:"adapter"`
	Status       string               `json:"status"`
	Error        string               `json:"error,omitempty"`
	LatencyMS    int64                `json:"latencyMs"`
	Models       int                  `json:"models"`
	CheckedAt    time.Time            `json:"checkedAt"`
	Capabilities adapter.Capabilities `json:"capabilities"`
	QuotaHint    string               `json:"quotaHint,omitempty"`
}

type affinityBind struct {
	Account string
	Until   time.Time
}

type instance struct {
	Provider config.Provider
	Adapter  adapter.Adapter
	// upstreamModel is the concrete catalog id for an automatic-route candidate.
	upstreamModel string
}

func (inst instance) recordAttempt(lastAccount *string) {
	*lastAccount = inst.Provider.ID
}

// New builds adapters from cfg. Call Refresh after.
func New(cfg config.Config, path string, reg *adapter.Registry) (*Gateway, error) {
	if reg == nil {
		reg = adapters.DefaultRegistry()
	}
	g := &Gateway{cfg: cfg, saved: config.Clone(cfg), path: path, reg: reg, cool: map[string]cooldownSlots{}, sticky: map[string]string{}, affinity: map[string]affinityBind{}, continuations: map[string]continuationBind{}, accountStats: map[string]router.AccountStat{}, admission: &router.Gate{}, flight: &responsecache.Flight{}, quota: quota.NewStore()}
	g.publishAdmissionLimits(cfg)
	if path != "" {
		g.Usage = usage.Open(filepath.Join(filepath.Dir(path), "usage.json"))
		if cfg.RequestLog {
			dir := filepath.Dir(path)
			g.Usage.SetRequestLog(filepath.Join(dir, "requests.log"))
			// Same opt-in as the request log, separate file. Sharing one file
			// would put failures back in the ring that evicts them by traffic.
			g.Usage.SetErrorLog(usage.ErrorLogPath(dir))
		}
	} else {
		g.Usage = &usage.Store{}
	}
	// The spend ceiling reads from the same ledger everything else reports, so
	// it counts spend PeaProxy actually recorded. Leaving this nil would make
	// the ceiling see a permanent zero and never fire.
	g.SetUsage(g.Usage)
	// The artifact store is what carried context reads from. It was left nil,
	// and every context path treats nil as "skip", so carried context and
	// pea_search never ran in a real proxy while the docs described them as
	// on by default. Constructing it here is what makes the feature real.
	g.Artifacts = newArtifactStore(&cfg)
	if err := g.rebuild(); err != nil {
		return nil, err
	}
	if path != "" {
		if sig, ok := statSig(path); ok {
			g.watchSig = sig
			g.watchSigSet = true
		}
	}
	return g, nil
}

func (g *Gateway) rebuild() error {
	var inst []instance
	var first error
	epoch := g.buildEpoch + 1
	for _, p := range g.cfg.Providers {
		if p.Disabled {
			continue
		}
		opts := adapter.Options{
			ID:        p.ID,
			BaseURL:   p.BaseURL,
			APIKey:    p.ResolveKey(),
			SessionID: p.SessionID,
			WorkDir:   p.WorkDir,
			Tier:      catalog.Tier(p.Tier),
		}
		accountID := p.ID
		adapterName := p.Adapter
		store := g.quota
		if store != nil {
			opts.ObserveHeaders = func(h http.Header) {
				store.Observe(accountID, adapterName, h)
			}
		}
		if p.OAuth != nil {
			opts.OAuth = p.OAuth.Runtime()
		}
		id := p.ID
		// A failed save must not fail the request that refreshed the token:
		// SaveOAuth has already updated cfg in memory, persist has logged the
		// error, and the next successful save writes the token.
		opts.PersistOAuth = func(tok oauth.Token) error {
			_ = g.saveOAuthFrom(id, tok, epoch)
			return nil
		}
		adp, err := g.reg.Open(p.Adapter, opts)
		if err != nil {
			if first == nil {
				first = fmt.Errorf("%s: %w", p.ID, err)
			}
			continue
		}
		inst = append(inst, instance{Provider: p, Adapter: adp})
	}
	g.inst = inst
	g.buildEpoch = epoch
	g.refreshSeq++
	g.refreshCommit = g.refreshSeq
	if len(inst) == 0 && first != nil {
		return first
	}
	return nil
}

// ConfigPath is the YAML file used for Save.
func (g *Gateway) ConfigPath() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.path
}

// Config returns a copy of the current config.
// Config returns a copy whose slices do not share the gateway's backing arrays.
// RemoveProvider used to filter with g.cfg.Providers[:0], so a caller that held
// the returned config across a save could marshal a shifted provider list.
func (g *Gateway) Config() config.Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	cfg := g.cfg
	cfg.Providers = append([]config.Provider(nil), g.cfg.Providers...)
	return cfg
}

// live returns the running config under the read lock. Request paths read it
// this way rather than touching g.cfg: the config watcher replaces g.cfg under
// g.mu while requests are in flight. Never call it with g.mu held.
func (g *Gateway) live() config.Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg
}

// SetConfigPath updates the save target.
func (g *Gateway) SetConfigPath(path string) {
	g.mu.Lock()
	g.path = path
	g.mu.Unlock()
}

// SetConfig replaces the in-memory config and the merge base. It does not
// write the YAML file.
func (g *Gateway) SetConfig(cfg config.Config) {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	g.mu.Lock()
	g.cfg = cfg
	g.saved = config.Clone(cfg)
	g.mu.Unlock()
	g.publishAdmissionLimits(cfg)
}

// Query is the current listing query.
func (g *Gateway) Query() catalog.Query {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.queryLocked()
}

// effectiveCapabilities is what the routing actually knows about an endpoint: the
// adapter's own declaration, corrected by whatever the user said about this
// specific endpoint in their config.
//
// The correction is the point. An OpenAI-compatible endpoint is a wire shape, not
// a promise -- plenty of them accept a `tools` array and quietly ignore it --
// and PeaProxy has no way to observe the difference. The user does, about their
// own machine, so their answer wins over the adapter's assumption.
func effectiveCapabilities(inst instance) adapter.Capabilities {
	if inst.Adapter == nil {
		return adapter.Capabilities{}
	}
	caps := inst.Adapter.Capabilities()
	c := inst.Provider.Capabilities
	if c.Tools != nil {
		caps.Tools = *c.Tools
	}
	if c.VisionIn != nil {
		caps.VisionIn = *c.VisionIn
	}
	if c.ImageOut != nil {
		caps.ImageOut = *c.ImageOut
	}
	if c.Embeddings != nil {
		caps.Embeddings = *c.Embeddings
	}
	return caps
}

func (g *Gateway) queryLocked() catalog.Query {
	return catalog.Query{
		HideProviders: g.cfg.Hide.Providers,
		HideModels:    g.cfg.Hide.Models,
		ExposeModels:  g.cfg.Expose.Models,
		ForClients:    true,
		BlockRouting:  g.cfg.Hide.BlockRouting,
		Pin:           g.cfg.Catalog.Pin,
		Rename:        g.cfg.Catalog.Rename,
	}
}

// Refresh pulls live ListModels from every adapter.
func (g *Gateway) Refresh(ctx context.Context) {
	g.mu.Lock()
	g.refreshSeq++
	seq := g.refreshSeq
	inst := append([]instance(nil), g.inst...)
	g.mu.Unlock()
	var all []catalog.Model
	health := make([]AdapterHealth, 0, len(inst))
	for _, inst := range inst {
		start := time.Now()
		models, err := inst.Adapter.ListModels(ctx)
		h := AdapterHealth{
			AccountID:    inst.Provider.ID,
			Adapter:      inst.Provider.Adapter,
			LatencyMS:    time.Since(start).Milliseconds(),
			CheckedAt:    time.Now().UTC(),
			Capabilities: effectiveCapabilities(inst),
		}
		if err != nil {
			g.observeLatency(inst.Provider.ID, time.Since(start), err)
			h.Status = "error"
			h.Error = healthErrorText(err)
			all = append(all, catalog.Model{
				ID:        inst.Provider.ID + ":unavailable",
				Provider:  inst.Provider.Adapter,
				AccountID: inst.Provider.ID,
				Tier:      catalog.Tier(inst.Provider.Tier),
				Status:    "auth_error",
				Routable:  false,
				Exposed:   false,
			})
			health = append(health, h)
			continue
		}
		h.Status = "ok"
		h.Models = len(models)
		if inst.Provider.Adapter == "ollama" {
			snap := localruntime.ProbeLoopback(ctx, inst.Provider.BaseURL, nil)
			if snap.State == localruntime.StateLoading {
				for i := range models {
					models[i].Status = string(localruntime.StateLoading)
				}
			}
		}
		g.observeLatency(inst.Provider.ID, time.Since(start), nil)
		health = append(health, h)
		all = append(all, models...)
	}
	okAccounts := map[string]bool{}
	seen := map[string]map[string]struct{}{}
	for _, m := range all {
		if strings.HasSuffix(m.ID, ":unavailable") {
			continue
		}
		okAccounts[m.AccountID] = true
		if seen[m.AccountID] == nil {
			seen[m.AccountID] = map[string]struct{}{}
		}
		seen[m.AccountID][m.ID] = struct{}{}
	}
	g.mu.Lock()
	if seq < g.refreshCommit {
		g.mu.Unlock()
		return
	}
	g.refreshCommit = seq
	if g.responses != nil {
		for _, old := range g.models {
			if !okAccounts[old.AccountID] {
				continue
			}
			if _, ok := seen[old.AccountID][old.ID]; ok {
				continue
			}
			g.responses.InvalidateAccountModel(old.AccountID, old.ID)
		}
	}
	g.models = all
	g.health = health
	g.mu.Unlock()
	g.probeQuota(ctx, inst)
}

// Models is the last refreshed full catalog (including hidden).
func (g *Gateway) Models() []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]catalog.Model, len(g.models))
	copy(out, g.models)
	return out
}

// Listed applies hide/expose for /v1/models, then adds stable route names.
// protocolRefused reports whether the provider said this model cannot serve
// this protocol, as opposed to the request being wrong.
//
// The marker is narrow on purpose: a plain 400 is a malformed request and must
// never remove a model from the catalog.
// errModelRefusesChat is what a client gets once the provider has refused this
// model on the chat protocol. It names the cause, because "400" from a gateway
// that worked a moment ago is not a useful answer.
var errModelRefusesChat = errors.New("this provider does not serve that model on the chat protocol; it has been removed from /v1/models")

// ModelRefusesChat reports the sentinel returned when every candidate has
// refused the model on this protocol.
func ModelRefusesChat(err error) bool {
	return errors.Is(err, errModelRefusesChat)
}

func protocolRefused(err error) bool {
	var he adapter.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
		return false
	}
	b := strings.ToLower(string(he.Body))
	return strings.Contains(b, "modelprotocolunsupported") ||
		strings.Contains(b, "does not support this protocol") ||
		strings.Contains(b, "unsupported_api_for_model")
}

// markModelNotChat records that this provider refused to serve the model on the
// chat wire. The model stays in the catalog for the admin, carrying the reason;
// it stops being advertised to clients and stops being a routing candidate.
//
// Believing the provider is the point. Guessing from the model name would need a
// table, and would be wrong for every model released after it was written.
func (g *Gateway) markModelNotChat(provider, model string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := range g.models {
		if g.models[i].ID != model {
			continue
		}
		// Provider on a row is the adapter family (openai_compat), not the
		// account; the account is AccountID.
		if provider != "" && g.models[i].AccountID != provider {
			continue
		}
		g.models[i].Status = "not_chat: provider refuses this model on the chat protocol"
	}
}

// notChatModel reports whether model has already been refused for this provider.
func (g *Gateway) notChatModel(provider, model string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for i := range g.models {
		if g.models[i].ID == model && g.models[i].AccountID == provider {
			return strings.HasPrefix(g.models[i].Status, "not_chat")
		}
	}
	return false
}

// clientIndexLocked is the picker and the resolve table for the models
// clients are shown. Caller holds g.mu.
func (g *Gateway) clientIndexLocked() catalog.Index {
	q := g.queryLocked()
	rows := catalog.ApplyRoutes(catalog.List(g.models, q), g.models, q, g.cfg.Routes)
	return catalog.Build(rows)
}

func (g *Gateway) Listed(filter catalog.Filter) []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	q.Filter = filter
	rows := catalog.ApplyRoutes(catalog.List(g.models, q), g.models, q, g.cfg.Routes)
	return catalog.Build(rows).Clients()
}

// Annotated is the UI catalog (hidden rows included).
func (g *Gateway) Annotated(filter catalog.Filter) []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	q.Filter = filter
	q.ForClients = false
	rows := catalog.ApplyRoutes(catalog.AllAnnotated(g.models, q), g.models, q, g.cfg.Routes)
	out := append(rows, catalog.Build(rows).Added()...)
	g.stampImageOutReadyLocked(out)
	g.stampEmbeddingsReadyLocked(out)
	return out
}

// resolveBody maps a stable route name onto its live catalog id and rewrites
// the top-level model field without reordering other keys. A name that is not
// a route is returned unchanged. A route whose target is not routable errors
// before any upstream call.
func (g *Gateway) resolveBody(raw []byte) (string, []byte, error) {
	peek := jsonx.PeekBody(raw)
	clientModel := strings.TrimSpace(peek.Model)
	g.mu.RLock()
	target, ok := g.cfg.Routes[clientModel]
	models := g.models
	q := g.queryLocked()
	g.mu.RUnlock()
	if !ok {
		return clientModel, raw, nil
	}
	target = strings.TrimSpace(target)
	if _, found := catalog.FindRoutable(models, q, target); !found {
		return "", nil, fmt.Errorf("route %q targets %q which is not in the live catalog", clientModel, target)
	}
	quoted, err := json.Marshal(target)
	if err != nil {
		return "", nil, err
	}
	return target, jsonx.SetTopLevelRaw(raw, "model", quoted), nil
}

func (g *Gateway) stampImageOutReadyLocked(models []catalog.Model) {
	ready := make(map[string]bool, len(g.inst))
	for _, inst := range g.inst {
		_, ok := inst.Adapter.(adapter.ImageGenerator)
		ready[inst.Provider.ID] = ok && effectiveCapabilities(inst).ImageOut
	}
	for i := range models {
		models[i].ImageOutReady = catalog.HasModality(models[i], "image_out") && ready[models[i].AccountID]
	}
}

func (g *Gateway) stampEmbeddingsReadyLocked(models []catalog.Model) {
	ready := make(map[string]bool, len(g.inst))
	for _, inst := range g.inst {
		_, ok := inst.Adapter.(adapter.Embedder)
		ready[inst.Provider.ID] = ok && effectiveCapabilities(inst).Embeddings
	}
	for i := range models {
		models[i].EmbeddingsReady = catalog.HasModality(models[i], "embeddings") && ready[models[i].AccountID]
	}
}

// Chat proxies a non-stream OpenAI chat.completions body with policy-based failover.
func noteStream(guard *streamguard.Guard, callErr error) error {
	return noteStreamFinish(guard, callErr, false)
}

// noteStreamDone is noteStream for an attempt whose bytes reached the guard
// directly (they are all written when the adapter returns), so a clean stream
// with no event at all fails the attempt.
func noteStreamDone(guard *streamguard.Guard, callErr error) error {
	return noteStreamFinish(guard, callErr, true)
}

func noteStreamFinish(guard *streamguard.Guard, callErr error, complete bool) error {
	if guard == nil {
		return callErr
	}
	if callErr == nil {
		finish := guard.Finish
		if complete {
			finish = guard.Complete
		}
		if err := finish(); err != nil {
			return err
		}
	}
	if perr := guard.Err(); perr != nil && (callErr == nil || errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded)) {
		return perr
	}
	return callErr
}

// failoverOn is the condition under which a failed attempt moves on to the next
// account rather than going back to the client: a retryable error, a transient
// 502/503/504, or a prelude the stream guard rejected. The Claude and Responses
// paths used a narrower one of the first two alone, so a 502 was handed to the
// client with another account still unused -- even though the guard had held the
// bytes back precisely so failover was possible (#71).
//
// A slow prelude is deliberately not here: it is handled before this check, and
// moves on without starting a cooldown.
func failoverOn(err error) bool {
	return retryable(err) || router.Transient(err) || preludeFailover(err)
}

func preludeFailover(err error) bool {
	return errors.Is(err, streamguard.ErrPrelude) || errors.Is(err, streamguard.ErrTimeout)
}

// slowPrelude is a first event that took too long. It moves on to the next
// account but is not an account failure, so it never starts a cooldown.
func slowPrelude(err error) bool {
	return errors.Is(err, streamguard.ErrTimeout)
}

func retryable(err error) bool {
	return router.Retryable(err)
}

// replayableAfter reports whether a side-effecting call may be sent to a
// different account after this error.
//
// 502 and 504 are excluded, and this is the whole point of the helper. Both come
// from a proxy in front of the provider that stopped waiting -- which is
// precisely *after* the provider may have generated and billed an image.
// Replaying buys a duplicate bill and returns whichever finished first. A 500 is
// not replayed either (router.Retryable has never claimed it), and a 429 is:
// the provider refused, so nothing was generated and moving on is free (#67).
//
// Embeddings are idempotent and deliberately do not use this.
func replayableAfter(err error) bool {
	var he adapter.HTTPError
	if errors.As(err, &he) && (he.Status == http.StatusBadGateway || he.Status == http.StatusGatewayTimeout) {
		return false
	}
	return retryable(err) || router.Transient(err)
}

func cooldownErr(last error) error {
	return router.CooldownError{RetryAfter: cooldownFor(last), Err: last}
}

// transientCooldownTTL bounds the skip after a transport failure (502, 504,
// edge connect error). It says nothing about the account, so a full
// cooldown would only lock out the next request, which usually succeeds.
const transientCooldownTTL = 5 * time.Second

// cooldownFor is how long an account is skipped after err: the upstream's
// own reset hint when it sent one, else a short or normal window.
// EntitlementCooldownTTL is how long an account stays out of rotation after the
// provider says it may not be used at all.
//
// An exhausted free tier or a quota that has run out does not recover in thirty
// seconds. Cooling for that long means the account is retried, fails the same
// way, and cools again -- and where it is the only account serving a model, the
// user sees the failure every half minute instead of once with a usable message.
const EntitlementCooldownTTL = 15 * time.Minute

func cooldownFor(err error) time.Duration {
	var he adapter.HTTPError
	if errors.As(err, &he) && he.RetryAfter > 0 {
		return he.RetryAfter
	}
	if router.Classify(err) == router.FailoverEntitlement {
		return EntitlementCooldownTTL
	}
	if router.Transient(err) {
		return min(cooldownTTL, transientCooldownTTL)
	}
	return cooldownTTL
}

func (g *Gateway) rememberSuccess(session, model, account string) {
	if g.admission != nil {
		g.admission.EndHalfOpen(account)
	}
	g.mu.Lock()
	delete(g.recovering, account)
	g.noteStatLocked(account, nil, 0)
	g.mu.Unlock()
	g.rememberSticky(model, account)
	g.bindAffinity(session, model, account)
}

// probePassed returns a stream commit hook that puts account back in rotation.
// A committed stream has proved the account works. Waiting for the stream to
// end held the half-open probe for the whole response, so for a single-account
// model every concurrent request failed "recovering from cooldown" for as long
// as one long stream ran, up to the probe's 30s expiry.
func (g *Gateway) probePassed(account string) func() {
	return func() {
		if g.admission != nil {
			g.admission.EndHalfOpen(account)
		}
		g.mu.Lock()
		delete(g.recovering, account)
		g.mu.Unlock()
	}
}

// admit reserves a capacity slot on an account. The limit itself is published
// when the gateway is built or rebuilt, not here: writing it per request raced
// every reader inside Acquire (#74).
func (g *Gateway) admit(ctx context.Context, account string) (func(), error) {
	if g == nil || g.admission == nil {
		return func() {}, nil
	}
	g.mu.RLock()
	_, recovering := g.recovering[account]
	g.mu.RUnlock()
	if recovering && !g.admission.TryHalfOpen(account) {
		return nil, errProbeBusy
	}
	release, err := g.admission.Acquire(ctx, account)
	if err != nil {
		g.admission.EndHalfOpen(account)
		return nil, err
	}
	// The attempt is over when its slot is released, whatever the outcome. A
	// half-open probe it held ends with it; a cancelled or client-fault attempt
	// neither succeeds nor cools the account, and without a configured limit
	// the release is a no-op, so the probe would otherwise sit for its full
	// expiry and keep a recovering account out of rotation.
	return func() {
		release()
		g.admission.EndHalfOpen(account)
	}, nil
}

// publishAdmissionLimits hands the configured limits to the gate. Called from
// New and from SetConfig, so every rebuild takes effect and nothing writes the
// gate while it is running.
func (g *Gateway) publishAdmissionLimits(cfg config.Config) {
	if g.admission == nil {
		return
	}
	// Queue depth and wait stay the gate's own defaults; only maxInFlight is
	// configured.
	g.admission.SetLimits(cfg.RequestEngine.MaxInFlight, 0, 0)
}

// admitted runs f with a capacity slot held for the account, releasing it when f
// returns. Every upstream attempt goes through here, so a path that forgets to
// take a slot is one edit rather than nine -- which is how streams, Claude,
// Responses, images and embeddings came to run unbounded while maxInFlight
// looked like it was working (#74).
func admitted[T any](g *Gateway, ctx context.Context, account, model string, body []byte, f func(context.Context) (T, error)) (T, error) {
	var zero T
	// Both refusals below happen before f runs, so nothing was sent upstream.
	// Marking them lets the ledger record an outcome without counting it as
	// spend, which would otherwise read as an unmeasured call and make the
	// ceiling refuse everything for a reason it cannot fix.
	release, err := g.admit(ctx, account)
	if err != nil {
		return zero, usage.MarkNotDispatched(err)
	}
	defer release()
	if err := g.holdSpend(ctx, account, model, body); err != nil {
		return zero, usage.MarkNotDispatched(err)
	}
	return f(ctx)
}

// admittedSlot is admitted without the spend hold.
//
// Image calls take it. Images are not billed per token, so a hold computed from
// the request's tokens would be a figure with no relationship to what the call
// costs -- and a call that a ceiling refuses for an estimate nobody published
// would be refused for the wrong reason. The capacity slot is the part that
// applies: an image generation is a long, expensive upstream call, and leaving
// it unbounded meant maxInFlight bounded every other path while quietly not
// bounding this one.
func admittedSlot[T any](g *Gateway, ctx context.Context, account string, f func(context.Context) (T, error)) (T, error) {
	var zero T
	release, err := g.admit(ctx, account)
	if err != nil {
		return zero, usage.MarkNotDispatched(err)
	}
	defer release()
	return f(ctx)
}

// admissionRejection reports whether err means the account had no capacity
// rather than something being wrong with it. Such an account is skipped, never
// cooled down: one burst must not take an account out of rotation (#74).
func admissionRejection(err error) bool {
	return errors.Is(err, router.ErrAdmissionQueue) || errors.Is(err, router.ErrAdmissionWait) || errors.Is(err, errProbeBusy)
}

// errProbeBusy is an attempt on an account recovering from cooldown while
// another request holds its half-open probe. Nothing was sent; the request
// moves on like any other admission refusal.
var errProbeBusy = errors.New("account is recovering from cooldown and another request is probing it")

func (g *Gateway) bindAffinity(session, model, account string) {
	if session == "" || model == "" || account == "" || !g.live().AffinityEnabled() {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.affinity == nil {
		g.affinity = map[string]affinityBind{}
	}
	g.affinity[session+"\x00"+model] = affinityBind{Account: account, Until: time.Now().Add(g.cfg.AffinityTTL())}
}

func (g *Gateway) liveAffinityLocked(session, model string, now time.Time) (string, bool) {
	if session == "" || !g.cfg.AffinityEnabled() {
		return "", false
	}
	b, ok := g.affinity[session+"\x00"+model]
	if !ok || !now.Before(b.Until) {
		if ok {
			delete(g.affinity, session+"\x00"+model)
		}
		return "", false
	}
	return b.Account, true
}

func (g *Gateway) rememberSticky(model, account string) {
	if model == "" || account == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sticky[model] = account
}

func (g *Gateway) route(ctx context.Context, raw []byte, model string) ([]instance, string, error) {
	session := router.SessionFrom(ctx)
	if session == "" {
		session = router.SessionFromBody(raw)
	}
	cands, retry := g.candidates(model, session)
	if len(cands) == 0 {
		if retry > 0 {
			return nil, session, router.CooldownError{RetryAfter: retry, Err: g.cooldownCause(model)}
		}
		return nil, session, router.ErrNoAccount
	}
	return cands, session, nil
}

// cooldownCause names why each account that serves model is unavailable, so a
// proxy-side cooldown is not mistaken for a provider usage limit. An active
// cooldown covering model gives its reason and time left. An account with no
// active cooldown entry is named as recovering, e.g. one just out of cooldown
// while another request holds its half-open probe. A cooldown scoped to
// another model is skipped.
func (g *Gateway) cooldownCause(model string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	var parts []string
	for _, id := range catalog.AccountsForModel(g.models, g.queryLocked(), model) {
		slots := g.cool[id]
		c, ok := activeCooldown(slots, model, now)
		if !ok {
			// Cooling a different model is no reason to hold up this request.
			// Anything else listed for it but unavailable is recovering: the
			// entry expired and a half-open probe has not cleared it yet.
			if slots.coolingOtherModel(model, now) {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s recovering from cooldown, retry shortly", id))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s after %s, %ds left", id, c.Reason, int(time.Until(c.Until).Round(time.Second).Seconds())))
	}
	if len(parts) == 0 {
		return nil
	}
	sort.Strings(parts)
	return errors.New(strings.Join(parts, "; "))
}

// markCooldown cools account id after err. sole says the request had no other
// candidate account to move to.
func (g *Gateway) markCooldown(id, model string, err error, sole bool) {
	if g.admission != nil {
		g.admission.EndHalfOpen(id)
	}
	class := router.Classify(err)
	reason := class.String()
	if class == router.FailoverNone {
		reason = "failover"
	}
	wait := cooldownFor(err)
	var scopedModel string
	var he adapter.HTTPError
	if errors.As(err, &he) {
		reason = fmt.Sprintf("HTTP %d (%s)", he.Status, reason)
		if he.Scope == adapter.ScopeModel {
			scopedModel = model
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.noteStatLocked(id, err, 0)
	// A transport failure says nothing about the account, and a cooldown only
	// helps by steering the next request elsewhere. With no other candidate
	// there is nowhere to steer it, so one TLS or edge blip used to fail every
	// concurrent request ("after failover, Ns left") and then the recovery
	// probe. An upstream Retry-After is still honoured.
	//
	// Sole is the request's candidate list, not the accounts listing this
	// model id: an automatic route mixes models, so each candidate is often
	// the only account for its own id while the route has others to try.
	if sole && router.Transient(err) && he.RetryAfter <= 0 {
		return
	}
	slots := g.cool[id]
	slots.set(scopedModel, Cooldown{AccountID: id, Model: scopedModel, Until: time.Now().Add(wait), Reason: reason})
	g.cool[id] = slots
}

// soleCandidate reports whether every candidate is the same account, so a
// failure leaves the request nowhere else to go.
func soleCandidate(cands []instance) bool {
	if len(cands) == 0 {
		return false
	}
	for _, c := range cands[1:] {
		if c.Provider.ID != cands[0].Provider.ID {
			return false
		}
	}
	return true
}

func (g *Gateway) observeLatency(account string, latency time.Duration, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.noteStatLocked(account, err, latency)
}

func (g *Gateway) noteStatLocked(account string, err error, latency time.Duration) {
	if account == "" {
		return
	}
	if g.accountStats == nil {
		g.accountStats = map[string]router.AccountStat{}
	}
	stat := g.accountStats[account]
	stat.Known = true
	if err != nil {
		stat.Errors++
	}
	if latency > 0 {
		if stat.Latency == 0 {
			stat.Latency = latency
		} else {
			stat.Latency = (stat.Latency*3 + latency) / 4
		}
	}
	g.accountStats[account] = stat
}

// Cooldowns returns active account cooldowns for the Health UI.
func (g *Gateway) Cooldowns() []Cooldown {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	var out []Cooldown
	for id, slots := range g.cool {
		slots.prune(now)
		g.cool[id] = slots
		// Every active slot is listed, so a person debugging a model that will
		// not route can see which one and why.
		for _, c := range slots.all(now) {
			c.RemainingMs = c.Until.Sub(now).Milliseconds()
			if c.RemainingMs < 0 {
				c.RemainingMs = 0
			}
			c.QuotaHint = g.quotaHintFor(id)
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	if out == nil {
		out = []Cooldown{}
	}
	return out
}

// healthErrorText is what Health shows. An auth failure keeps the upstream
// body out of the UI; the operator needs to sign in again, not the raw
// invalid_grant JSON. Other errors stay redacted.
func healthErrorText(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(err.Error())
	if router.Classify(err) == router.FailoverAuth || strings.Contains(lower, "invalid_grant") || strings.Contains(lower, "refresh token") {
		return "sign in again; the saved refresh token was rejected"
	}
	return usage.Redact(err.Error())
}

// AdapterHealth returns last probe/ListModels status, overlaying active cooldowns.
func (g *Gateway) AdapterHealth() []AdapterHealth {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	out := make([]AdapterHealth, len(g.health))
	copy(out, g.health)
	for i := range out {
		if _, ok := g.cool[out[i].AccountID].anyActive(now); ok && out[i].Status == "ok" {
			out[i].Status = "cooldown"
		}
		out[i].QuotaHint = g.quotaHintFor(out[i].AccountID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	if out == nil {
		out = []AdapterHealth{}
	}
	return out
}

// Probe re-runs Validate on each adapter and stores the result for Health.
func (g *Gateway) Probe(ctx context.Context) []AdapterHealth {
	g.mu.RLock()
	inst := append([]instance(nil), g.inst...)
	prev := make(map[string]AdapterHealth, len(g.health))
	for _, h := range g.health {
		prev[h.AccountID] = h
	}
	g.mu.RUnlock()
	health := make([]AdapterHealth, 0, len(inst))
	for _, inst := range inst {
		start := time.Now()
		err := inst.Adapter.Validate(ctx)
		h := AdapterHealth{
			AccountID:    inst.Provider.ID,
			Adapter:      inst.Provider.Adapter,
			LatencyMS:    time.Since(start).Milliseconds(),
			CheckedAt:    time.Now().UTC(),
			Capabilities: effectiveCapabilities(inst),
		}
		if old, ok := prev[inst.Provider.ID]; ok {
			h.Models = old.Models
		}
		if err != nil {
			h.Status = "error"
			h.Error = healthErrorText(err)
		} else {
			h.Status = "ok"
		}
		health = append(health, h)
	}
	g.mu.Lock()
	g.health = health
	g.mu.Unlock()
	g.probeQuota(ctx, inst)
	return g.AdapterHealth()
}

// Quota is one remaining snapshot per account. Unknown remaining is omitted.
func (g *Gateway) Quota() []quota.Snapshot {
	g.mu.RLock()
	inst := append([]instance(nil), g.inst...)
	g.mu.RUnlock()
	accts := make([]quota.Account, 0, len(inst))
	for _, inst := range inst {
		accts = append(accts, quota.Account{ID: inst.Provider.ID, Adapter: inst.Provider.Adapter})
	}
	if g.quota == nil {
		return quota.NewStore().Views(accts)
	}
	return g.quota.Views(accts)
}

// QuotaSnapshot is the latest stored remaining for one account, if any.
func (g *Gateway) QuotaSnapshot(accountID string) (quota.Snapshot, bool) {
	if g.quota == nil || accountID == "" {
		return quota.Snapshot{}, false
	}
	return g.quota.Get(accountID)
}

func (g *Gateway) quotaHintFor(accountID string) string {
	snap, ok := g.QuotaSnapshot(accountID)
	if !ok {
		return ""
	}
	return snap.Compact()
}

func (g *Gateway) probeQuota(ctx context.Context, inst []instance) {
	if g.quota == nil {
		return
	}
	client := &http.Client{Timeout: 8 * time.Second}
	for _, inst := range inst {
		keyURL, ok := quota.ProbeURL(inst.Provider.Adapter, inst.Provider.BaseURL)
		if !ok {
			continue
		}
		snap, err := quota.ProbeOpenRouter(ctx, client, inst.Provider.ID, inst.Provider.ResolveKey(), keyURL)
		if err != nil {
			continue
		}
		g.quota.ApplyProbe(snap)
	}
}

// omitZenAlias drops OpenCode Zen when another account lists the same model.
// Zen bills those ids as its own credits, so a 402 from Zen was surfacing as
// "insufficient funds" on ChatGPT, Claude, and Gemini subscriptions.
func omitZenAlias(matched []instance) []instance {
	zen := false
	other := false
	for _, inst := range matched {
		if inst.Provider.Adapter == "opencode_zen" {
			zen = true
		} else {
			other = true
		}
	}
	if !zen || !other {
		return matched
	}
	out := make([]instance, 0, len(matched)-1)
	for _, inst := range matched {
		if inst.Provider.Adapter == "opencode_zen" {
			continue
		}
		out = append(out, inst)
	}
	return out
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func (g *Gateway) candidates(model, session string) ([]instance, time.Duration) {
	return g.candidatesFor(model, session, false)
}

// candidatesFor lists the accounts a request for model may use. peek is for
// read-only callers (ExplainRoute): it neither prunes cooldowns nor takes a
// half-open probe, and treats a recovering account as eligible unless another
// request holds its probe.
func (g *Gateway) candidatesFor(model, session string, peek bool) ([]instance, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	recovering := g.recovering
	if peek {
		recovering = maps.Clone(g.recovering)
	}
	for id, slots := range g.cool {
		if peek {
			// prune deletes from the slot's map, which a read-only caller
			// must not touch.
			slots.models = maps.Clone(slots.models)
		}
		slots.prune(now)
		if !peek {
			g.cool[id] = slots
		}
		if slots.wideActive() || len(slots.models) > 0 {
			continue
		}
		if !peek {
			delete(g.cool, id)
			if g.recovering == nil {
				g.recovering = map[string]struct{}{}
			}
			recovering = g.recovering
		} else if recovering == nil {
			recovering = map[string]struct{}{}
		}
		recovering[id] = struct{}{}
	}
	q := g.queryLocked()
	ids := catalog.AccountsForModel(g.models, q, model)
	// An id nobody lists must not be fanned out. A typo would otherwise spend
	// quota on every account.
	if len(ids) == 0 {
		return nil, 0
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var matched []instance
	for _, inst := range g.inst {
		if _, ok := want[inst.Provider.ID]; ok {
			matched = append(matched, inst)
		}
	}
	matched = omitZenAlias(matched)
	if len(matched) == 0 {
		return nil, 0
	}
	hot := make([]instance, 0, len(matched))
	var until time.Time
	for _, inst := range matched {
		if c, ok := activeCooldown(g.cool[inst.Provider.ID], model, now); ok {
			if until.IsZero() || c.Until.Before(until) {
				until = c.Until
			}
			continue
		}
		// The probe itself is taken in admit, when an attempt on the account
		// actually starts. Taking it here held it for accounts a request listed
		// but never tried, which shut every other request out of a healthy
		// account until the probe expired.
		if _, rec := recovering[inst.Provider.ID]; rec && g.admission != nil && g.admission.Probing(inst.Provider.ID) {
			if until.IsZero() {
				until = now.Add(time.Second)
			}
			continue
		}
		hot = append(hot, inst)
	}
	if len(hot) == 0 {
		if until.IsZero() {
			return nil, 0
		}
		return nil, time.Until(until)
	}
	hotIDs := make([]string, len(hot))
	byID := make(map[string]instance, len(hot))
	for i, inst := range hot {
		hotIDs[i] = inst.Provider.ID
		byID[inst.Provider.ID] = inst
	}
	var ordered []string
	if acct, ok := g.liveAffinityLocked(session, model, now); ok && containsID(hotIDs, acct) {
		ordered = router.Order(router.PolicySticky, hotIDs, nil, acct)
	} else {
		ordered = router.Order(router.Policy(g.cfg.Failover.Policy), hotIDs, &g.rr, g.sticky[model])
	}
	out := make([]instance, 0, len(ordered))
	for _, id := range ordered {
		out = append(out, byID[id])
	}
	if router.NormalizePolicy(router.Policy(g.cfg.Failover.Policy)) == router.PolicyAdaptive {
		ids := make([]string, len(out))
		by := map[string]instance{}
		for i, inst := range out {
			ids[i] = inst.Provider.ID
			by[inst.Provider.ID] = inst
		}
		ordered = router.AdaptiveOrder(ids, g.accountStats)
		out = out[:0]
		for _, id := range ordered {
			out = append(out, by[id])
		}
	}
	return filterExactLocal(model, g.models, out), 0
}

// persist merges mine into the config file and adopts what other processes
// changed there. The caller holds saveMu (not mu) and passes a config.Clone
// of cfg taken under mu. Providers, Hide, Expose, Catalog and Routes are
// adopted from the merge into cfg; the other fields are overlaid by flags and
// env, so saved keeps the running values and those overlays never win a later
// merge against disk. When adopted providers differ, persist calls rebuild()
// to reinitialise adapters and returns rebuilt=true so the caller can run
// Refresh() after releasing saveMu; persist never calls Refresh itself.
func (g *Gateway) persist(mine config.Config) (rebuilt bool, err error) {
	g.mu.RLock()
	path, base := g.path, g.saved
	g.mu.RUnlock()
	if path == "" {
		g.mu.Lock()
		g.saved = mine
		g.mu.Unlock()
		return false, nil
	}
	merged, err := config.SaveMerged(path, base, mine)
	g.notePersistErr(path, err)
	if err != nil {
		return false, err
	}
	adopt := config.Clone(merged)
	g.mu.Lock()
	defer g.mu.Unlock()
	next := mine
	next.Providers, next.Hide, next.Expose, next.Catalog, next.Routes = merged.Providers, merged.Hide, merged.Expose, merged.Catalog, merged.Routes
	g.cfg.Providers, g.cfg.Hide, g.cfg.Expose, g.cfg.Catalog, g.cfg.Routes = adopt.Providers, adopt.Hide, adopt.Expose, adopt.Catalog, adopt.Routes
	g.saved = next
	if !reflect.DeepEqual(merged.Providers, mine.Providers) {
		kept := make(map[string]bool, len(merged.Providers))
		for _, p := range merged.Providers {
			kept[p.ID] = true
		}
		for _, p := range mine.Providers {
			if !kept[p.ID] {
				g.forgetAccountLocked(p.ID)
			}
		}
		_ = g.rebuild()
		return true, nil
	}
	return false, nil
}

// forgetAccountLocked drops continuation bindings and cached responses of a
// removed account. The caller holds mu.
func (g *Gateway) forgetAccountLocked(id string) {
	delete(g.recovering, id)
	for responseID, bind := range g.continuations {
		if bind.Account == id {
			delete(g.continuations, responseID)
		}
	}
	if g.responses != nil {
		g.responses.InvalidatePrefix(id + "\x00")
	}
}

// tempName matches the random part os.CreateTemp puts in a temp file name.
var tempName = regexp.MustCompile(`\.[0-9]+\.tmp\b`)

// notePersistErr logs a failed save once per distinct error; a successful
// save (nil) clears it so the next failure is logged again. Errors that
// differ only in a temp file name count as the same error.
func (g *Gateway) notePersistErr(path string, err error) {
	msg := ""
	if err != nil {
		msg = tempName.ReplaceAllString(err.Error(), ".*.tmp")
	}
	g.mu.Lock()
	changed := msg != g.lastPersistErr
	g.lastPersistErr = msg
	g.mu.Unlock()
	if err != nil && changed {
		log.Printf("peaproxy: config save failed (%s): %v", path, err)
	}
}

// SetRequestLog toggles the opt-in redacted JSONL log and persists config.
func (g *Gateway) SetRequestLog(on bool) error {
	g.saveMu.Lock()
	g.mu.Lock()
	g.cfg.RequestLog = on
	path := g.path
	mine := config.Clone(g.cfg)
	g.mu.Unlock()
	if g.Usage != nil {
		if on && path != "" {
			dir := filepath.Dir(path)
			g.Usage.SetRequestLog(filepath.Join(dir, "requests.log"))
			g.Usage.SetErrorLog(usage.ErrorLogPath(dir))
		} else {
			g.Usage.SetRequestLog("")
			g.Usage.SetErrorLog("")
		}
	}
	return g.persistAndRefresh(mine)
}

// AddProvider appends an account, rebuilds, saves, and refreshes.
func (g *Gateway) AddProvider(ctx context.Context, p config.Provider) error {
	g.saveMu.Lock()
	g.mu.Lock()
	for _, e := range g.cfg.Providers {
		if e.ID == p.ID {
			g.mu.Unlock()
			g.saveMu.Unlock()
			return fmt.Errorf("account %q already exists", p.ID)
		}
	}
	if _, err := g.reg.Open(p.Adapter, adapter.Options{
		ID:        p.ID,
		BaseURL:   p.BaseURL,
		APIKey:    p.ResolveKey(),
		SessionID: p.SessionID,
		Tier:      catalog.Tier(p.Tier),
	}); err != nil {
		g.mu.Unlock()
		g.saveMu.Unlock()
		return err
	}
	g.cfg.Providers = append(g.cfg.Providers, p)
	if err := g.rebuild(); err != nil {
		g.mu.Unlock()
		g.saveMu.Unlock()
		return err
	}
	mine := config.Clone(g.cfg)
	g.mu.Unlock()
	_, err := g.persist(mine)
	g.saveMu.Unlock()
	if err != nil {
		return err
	}
	g.Refresh(ctx)
	return nil
}

// RemoveProvider deletes an account.
func (g *Gateway) RemoveProvider(ctx context.Context, id string) error {
	g.saveMu.Lock()
	g.mu.Lock()
	kept := make([]config.Provider, 0, len(g.cfg.Providers))
	for _, p := range g.cfg.Providers {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	g.cfg.Providers = kept
	g.forgetAccountLocked(id)
	_ = g.rebuild()
	mine := config.Clone(g.cfg)
	g.mu.Unlock()
	_, err := g.persist(mine)
	g.saveMu.Unlock()
	if err != nil {
		return err
	}
	g.Refresh(ctx)
	return nil
}

// ToggleHide listing-only hide for a provider or model id.
func (g *Gateway) ToggleHide(kind, id string, hidden bool) error {
	g.saveMu.Lock()
	g.mu.Lock()
	switch kind {
	case "provider":
		g.cfg.Hide.Providers = setHidden(g.cfg.Hide.Providers, id, hidden)
	case "model":
		g.cfg.Hide.Models = setHidden(g.cfg.Hide.Models, id, hidden)
	default:
		g.mu.Unlock()
		g.saveMu.Unlock()
		return fmt.Errorf("kind must be provider or model")
	}
	mine := config.Clone(g.cfg)
	g.mu.Unlock()
	return g.persistAndRefresh(mine)
}

// persistAndRefresh persists mine, releases saveMu (which the caller holds),
// and refreshes in the background when adopted providers changed the adapters.
func (g *Gateway) persistAndRefresh(mine config.Config) error {
	rebuilt, err := g.persist(mine)
	g.saveMu.Unlock()
	if rebuilt {
		go g.Refresh(context.Background())
	}
	return err
}

// SetCatalogOverlay pins and/or renames a live model id. Empty displayName clears the overlay.
func (g *Gateway) SetCatalogOverlay(id string, displayName *string, pinned *bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	g.saveMu.Lock()
	g.mu.Lock()
	if pinned != nil {
		g.cfg.Catalog.Pin = setHidden(g.cfg.Catalog.Pin, id, *pinned)
	}
	if displayName != nil {
		name := strings.TrimSpace(*displayName)
		// Copy on write: Config() callers hold the old map.
		rename := maps.Clone(g.cfg.Catalog.Rename)
		if rename == nil {
			rename = map[string]string{}
		}
		if name == "" {
			delete(rename, id)
		} else {
			rename[id] = name
		}
		if len(rename) == 0 {
			rename = nil
		}
		g.cfg.Catalog.Rename = rename
	}
	mine := config.Clone(g.cfg)
	g.mu.Unlock()
	return g.persistAndRefresh(mine)
}

func setHidden(list []string, id string, hidden bool) []string {
	out := make([]string, 0, len(list)+1)
	for _, x := range list {
		if x != id {
			out = append(out, x)
		}
	}
	if hidden {
		out = append(out, id)
	}
	return out
}

// Save persists config, merged with what other processes wrote.

func (g *Gateway) Save() error {
	g.saveMu.Lock()
	g.mu.RLock()
	if g.path == "" {
		g.mu.RUnlock()
		g.saveMu.Unlock()
		return errors.New("no config path")
	}
	mine := config.Clone(g.cfg)
	g.mu.RUnlock()
	return g.persistAndRefresh(mine)
}

// Instances returns provider metadata for the UI.
func (g *Gateway) Instances() []config.Provider {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]config.Provider, len(g.cfg.Providers))
	copy(out, g.cfg.Providers)
	for i := range out {
		out[i].APIKey = ""
		if g.cfg.Providers[i].APIKey != "" || g.cfg.Providers[i].APIKeyEnv != "" {
			out[i].APIKey = "configured"
		}
		if out[i].OAuth != nil {
			email := out[i].OAuth.Email
			has := out[i].OAuth.AccessToken != ""
			out[i].OAuth = &config.OAuthToken{Email: email}
			if has {
				out[i].OAuth.AccessToken = "configured"
			}
		}
	}
	return out
}

// AdapterByID returns a live adapter instance.
func (g *Gateway) AdapterByID(id string) (adapter.Adapter, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, inst := range g.inst {
		if inst.Provider.ID == id {
			return inst.Adapter, true
		}
	}
	return nil, false
}

// SaveOAuth persists refreshed subscription tokens via the secret store. The
// in-memory token is updated even when the save fails.
func (g *Gateway) SaveOAuth(id string, tok oauth.Token) error {
	return g.saveOAuthFrom(id, tok, 0)
}

// saveOAuthFrom is SaveOAuth for a token committed by an adapter from build
// epoch (0: not from an adapter). When the adapters have been rebuilt since,
// the live one for id was made from the config before this token reached it,
// so it still holds the refresh token this refresh just spent (a rotating
// provider answers its next refresh with invalid_grant). The adapters are
// rebuilt again from the config, which now has the new token.
func (g *Gateway) saveOAuthFrom(id string, tok oauth.Token, epoch uint64) error {
	ct := config.OAuthFromRuntime(tok)
	g.saveMu.Lock()
	g.mu.Lock()
	for i := range g.cfg.Providers {
		if g.cfg.Providers[i].ID == id {
			g.cfg.Providers[i].OAuth = &ct
			break
		}
	}
	mine := config.Clone(g.cfg)
	g.mu.Unlock()
	rebuilt, err := g.persist(mine)
	if !rebuilt && epoch != 0 {
		g.mu.Lock()
		if g.buildEpoch != epoch {
			_ = g.rebuild()
			rebuilt = true
		}
		g.mu.Unlock()
	}
	g.saveMu.Unlock()
	if rebuilt {
		go g.Refresh(context.Background())
	}
	return err
}

// newArtifactStore builds the session-scoped artifact store the configuration
// asks for.
//
// It is constructed unconditionally rather than behind an enable flag: nil is
// what made every context path silently skip, so a store that is always present
// is the safer default. Context optimization still has to be switched on before
// anything is stored or searched.
//
// Disk persistence is not implemented in the store, so this is memory-only.
// optimization.persistentContext remains unset-honoured but not yet honoured;
// that gap is documented rather than faked here.
func newArtifactStore(_ *config.Config) *contextstore.Store {
	return contextstore.New(contextstore.Options{})
}
