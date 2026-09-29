package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/localruntime"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/quota"
	"github.com/ks1686/peaproxy/internal/responsecache"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/streamguard"
	"github.com/ks1686/peaproxy/internal/translate"
	"github.com/ks1686/peaproxy/internal/usage"
)

const CooldownTTL = 30 * time.Second

// cooldownTTL is the skip window after a retryable failure. Tests may shorten it.
var cooldownTTL = CooldownTTL

// Gateway owns config, live adapters, catalog, and usage.
type Gateway struct {
	mu            sync.RWMutex
	cfg           config.Config
	path          string
	reg           *adapter.Registry
	inst          []instance
	models        []catalog.Model
	Usage         *usage.Store
	cool          map[string]Cooldown
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
	g := &Gateway{cfg: cfg, path: path, reg: reg, cool: map[string]Cooldown{}, sticky: map[string]string{}, affinity: map[string]affinityBind{}, continuations: map[string]continuationBind{}, accountStats: map[string]router.AccountStat{}, admission: &router.Gate{}, flight: &responsecache.Flight{}, quota: quota.NewStore()}
	if path != "" {
		g.Usage = usage.Open(filepath.Join(filepath.Dir(path), "usage.json"))
		if cfg.RequestLog {
			g.Usage.SetRequestLog(filepath.Join(filepath.Dir(path), "requests.log"))
		}
	} else {
		g.Usage = &usage.Store{}
	}
	if err := g.rebuild(); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *Gateway) rebuild() error {
	var inst []instance
	var first error
	for _, p := range g.cfg.Providers {
		if p.Disabled {
			continue
		}
		opts := adapter.Options{
			ID:        p.ID,
			BaseURL:   p.BaseURL,
			APIKey:    p.ResolveKey(),
			SessionID: p.SessionID,
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
		opts.PersistOAuth = func(tok oauth.Token) error {
			return g.SaveOAuth(id, tok)
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
	g.refreshSeq++
	g.refreshCommit = g.refreshSeq
	if len(inst) == 0 && first != nil {
		return first
	}
	return nil
}

// ConfigPath is the YAML file used for Save.
func (g *Gateway) ConfigPath() string { return g.path }

// Config returns a copy of the current config.
func (g *Gateway) Config() config.Config {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg
}

// SetConfigPath updates the save target.
func (g *Gateway) SetConfigPath(path string) { g.path = path }

// SetConfig replaces the in-memory config. It does not write the YAML file.
func (g *Gateway) SetConfig(cfg config.Config) {
	g.mu.Lock()
	g.cfg = cfg
	g.mu.Unlock()
}

// Query is the current listing query.
func (g *Gateway) Query() catalog.Query {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.queryLocked()
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
			Capabilities: inst.Adapter.Capabilities(),
		}
		if err != nil {
			g.observeLatency(inst.Provider.ID, time.Since(start), err)
			h.Status = "error"
			h.Error = usage.Redact(err.Error())
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
func (g *Gateway) Listed(filter catalog.Filter) []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	q.Filter = filter
	return catalog.ApplyRoutes(catalog.List(g.models, q), g.models, q, g.cfg.Routes)
}

// Annotated is the UI catalog (hidden rows included).
func (g *Gateway) Annotated(filter catalog.Filter) []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	q.Filter = filter
	q.ForClients = false
	out := catalog.ApplyRoutes(catalog.AllAnnotated(g.models, q), g.models, q, g.cfg.Routes)
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
		ready[inst.Provider.ID] = ok && inst.Adapter.Capabilities().ImageOut
	}
	for i := range models {
		models[i].ImageOutReady = catalog.HasModality(models[i], "image_out") && ready[models[i].AccountID]
	}
}

func (g *Gateway) stampEmbeddingsReadyLocked(models []catalog.Model) {
	ready := make(map[string]bool, len(g.inst))
	for _, inst := range g.inst {
		_, ok := inst.Adapter.(adapter.Embedder)
		ready[inst.Provider.ID] = ok && inst.Adapter.Capabilities().Embeddings
	}
	for i := range models {
		models[i].EmbeddingsReady = catalog.HasModality(models[i], "embeddings") && ready[models[i].AccountID]
	}
}

// Chat proxies a non-stream OpenAI chat.completions body with policy-based failover.
func (g *Gateway) Chat(ctx context.Context, raw []byte) (adapter.ChatResponse, string, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return adapter.ChatResponse{}, "", err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return adapter.ChatResponse{}, "", err
	}
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if hit, ok := g.cachedChat(inst.Provider.ID, inst.Provider.BaseURL, model, raw); ok {
			hit.Raw = echoClientModel(hit.Raw, client, model)
			if client != model {
				hit.Model = client
			}
			return hit, inst.Provider.ID, nil
		}
	}
	var last error
	var lastAccount string
	budgetAttempts := newAttemptCoordinator(g.cfg.RequestMaxAttempts())
	coalesce := g.cfg.RequestEngine.CacheResponses && responsecache.Eligible("chat", raw, true)
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		var resp adapter.ChatResponse
		var callErr error
		var attempted atomic.Bool
		callUpstream := func(runCtx context.Context) (adapter.ChatResponse, error) {
			var once adapter.ChatResponse
			var onceErr error
			for attempt := 0; attempt < 2; attempt++ {
				if err := budgetAttempts.take(); err != nil {
					return adapter.ChatResponse{}, errAttemptBudgetExhausted
				}
				release, aerr := g.admit(runCtx, inst.Provider.ID)
				if aerr != nil {
					return adapter.ChatResponse{}, aerr
				}
				attempted.Store(true)
				once, onceErr = inst.Adapter.Chat(runCtx, chatReq(inst, model, g.promptBody(raw, inst.Provider.Adapter), false, budget))
				release()
				if onceErr == nil || !router.Transient(onceErr) || attempt == 1 {
					break
				}
			}
			return once, onceErr
		}
		if coalesce && g.flight != nil {
			var body []byte
			body, callErr = g.flight.Do(ctx, responsecache.Key(inst.Provider.ID, inst.Provider.BaseURL, model, "chat", raw), func(runCtx context.Context) ([]byte, error) {
				once, err := callUpstream(runCtx)
				if err != nil {
					return nil, err
				}
				return once.Raw, nil
			})
			if callErr == nil {
				resp.Raw = body
			}
		} else {
			resp, callErr = callUpstream(ctx)
		}
		if attempted.Load() || (coalesce && !errors.Is(callErr, errAttemptBudgetExhausted)) {
			inst.recordAttempt(&lastAccount)
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			resp.Raw = echoClientModel(resp.Raw, client, model)
			if client != model {
				resp.Model = client
			}
			g.storeChat(lastAccount, inst.Provider.BaseURL, model, raw, resp.Raw)
			return resp, lastAccount, nil
		}
		last = callErr
		if retryable(callErr) || router.Transient(callErr) {
			g.markCooldown(inst.Provider.ID, model, callErr)
			continue
		}
		return adapter.ChatResponse{}, lastAccount, callErr
	}
	return adapter.ChatResponse{}, lastAccount, cooldownErr(last)
}

// streamPrelude keeps the configured first-event limit, and lengthens it only
// while a local model is still loading. The request deadline is unchanged.
func (g *Gateway) streamPrelude(model string) time.Duration {
	base := g.cfg.StreamPreludeTimeout()
	const coldStart = 30 * time.Second
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, m := range g.models {
		if m.ID == model && m.Tier == catalog.TierLocal && m.Status == string(localruntime.StateLoading) && base < coldStart {
			return coldStart
		}
	}
	return base
}

func (g *Gateway) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, g.cfg.RequestDeadline())
}

// ChatStream proxies SSE with failover before any bytes are written.
func (g *Gateway) ChatStream(ctx context.Context, raw []byte, w io.Writer) (string, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return "", err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return "", err
	}
	cw := &countWriter{w: w}
	guard := streamguard.New(cw, 0, g.streamPrelude(model))
	var last error
	var lastAccount string
	var slowSkipped bool
	budgetAttempts := newAttemptCoordinator(g.cfg.RequestMaxAttempts())
	for i, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		dest := newRouteRewriter(guard, model, client)
		var callErr error
		for attempt := 0; attempt < 2; attempt++ {
			if err := budgetAttempts.take(); err != nil {
				return lastAccount, errAttemptBudgetExhausted
			}
			guard.Reset()
			if budgetAttempts.final(i, len(cands)-1) {
				guard.Unbounded()
			}
			before := cw.n
			attemptCtx, stop := guard.Bound(ctx)
			inst.recordAttempt(&lastAccount)
			callErr = noteStream(guard, inst.Adapter.ChatStream(attemptCtx, chatReq(inst, model, raw, true, budget), dest))
			stop()
			if callErr == nil || cw.n > before || (!router.Transient(callErr) && !errors.Is(callErr, streamguard.ErrPrelude)) || attempt == 1 {
				break
			}
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			return lastAccount, nil
		}
		last = callErr
		if cw.n > 0 {
			return lastAccount, callErr
		}
		if slowPrelude(callErr) {
			slowSkipped = true
			continue
		}
		if retryable(callErr) || router.Transient(callErr) || preludeFailover(callErr) {
			g.markCooldown(inst.Provider.ID, model, callErr)
			continue
		}
		return lastAccount, callErr
	}
	// A slow account was skipped without a cooldown, so not every account is cooling.
	if slowSkipped {
		return lastAccount, last
	}
	return lastAccount, cooldownErr(last)
}

// EditImage proxies POST /v1/images/edits for the same image_out accounts
// that serve generations. Multipart bodies are forwarded with their content type.
func (g *Gateway) EditImage(ctx context.Context, raw []byte, contentType string) (adapter.ImageResponse, string, error) {
	model := ImageEditModel(raw, contentType)
	body := raw
	client := model
	if !strings.Contains(strings.ToLower(contentType), "multipart/") {
		var err error
		model, client, body, _, err = g.prepare(raw)
		if err != nil {
			return adapter.ImageResponse{}, "", err
		}
	} else if target, ok := g.routeTarget(model); ok && target != model {
		if rewritten, ct, ok := rewriteMultipartModel(raw, contentType, model, target); ok {
			body, contentType, model = rewritten, ct, target
		}
	}
	if model == "" {
		return adapter.ImageResponse{}, "", adapter.ErrImageModelRequired
	}
	if !g.supportsImageOut(model) {
		return adapter.ImageResponse{}, "", adapter.ErrModelNotImageOut
	}
	model, cands, session, err := g.routeResolved(ctx, body, model)
	if err != nil {
		return adapter.ImageResponse{}, "", err
	}
	var last error
	var lastAccount string
	tried := false
	for _, inst := range cands {
		if !strings.Contains(strings.ToLower(contentType), "multipart/") {
			model, body = inst.applyModel(model, body)
		}
		ed, ok := inst.Adapter.(adapter.ImageEditor)
		if !ok || !inst.Adapter.Capabilities().ImageOut {
			last = adapter.ErrImageOutUnsupported
			continue
		}
		tried = true
		inst.recordAttempt(&lastAccount)
		resp, callErr := ed.EditImage(ctx, adapter.ImageRequest{Model: model, Raw: body, ContentType: contentType})
		if stop := ambiguousDelivery(callErr); stop != nil {
			return adapter.ImageResponse{}, lastAccount, stop
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			if !strings.Contains(strings.ToLower(contentType), "multipart/") {
				resp.Raw = echoClientModel(resp.Raw, client, model)
			}
			return resp, lastAccount, nil
		}
		last = callErr
		if retryable(callErr) || router.Transient(callErr) {
			g.markCooldown(inst.Provider.ID, model, callErr)
			continue
		}
		return adapter.ImageResponse{}, lastAccount, callErr
	}
	if !tried {
		return adapter.ImageResponse{}, lastAccount, adapter.ErrImageOutUnsupported
	}
	if retryable(last) || router.Transient(last) {
		return adapter.ImageResponse{}, lastAccount, cooldownErr(last)
	}
	return adapter.ImageResponse{}, lastAccount, last
}

func (g *Gateway) routeTarget(name string) (string, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	target, ok := g.cfg.Routes[strings.TrimSpace(name)]
	if !ok {
		return "", false
	}
	target = strings.TrimSpace(target)
	if _, found := catalog.FindRoutable(g.models, g.queryLocked(), target); !found {
		return "", false
	}
	return target, true
}

// GenerateImage proxies OpenAI-compatible POST /v1/images/generations.
// Upstream is only called when the model is tagged image_out and the adapter
// implements ImageGenerator with Capabilities.ImageOut.
func (g *Gateway) GenerateImage(ctx context.Context, raw []byte) (adapter.ImageResponse, string, error) {
	model, client, raw, budget, err := g.prepare(raw)
	_ = budget
	if err != nil {
		return adapter.ImageResponse{}, "", err
	}
	if model == "" {
		return adapter.ImageResponse{}, "", adapter.ErrImageModelRequired
	}
	if !g.supportsImageOut(model) {
		return adapter.ImageResponse{}, "", adapter.ErrModelNotImageOut
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return adapter.ImageResponse{}, "", err
	}
	var last error
	var lastAccount string
	tried := false
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		gen, ok := inst.Adapter.(adapter.ImageGenerator)
		if !ok || !inst.Adapter.Capabilities().ImageOut {
			last = adapter.ErrImageOutUnsupported
			continue
		}
		tried = true
		inst.recordAttempt(&lastAccount)
		resp, callErr := gen.GenerateImage(ctx, adapter.ImageRequest{Model: model, Raw: raw})
		if stop := ambiguousDelivery(callErr); stop != nil {
			return adapter.ImageResponse{}, lastAccount, stop
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			resp.Raw = echoClientModel(resp.Raw, client, model)
			return resp, lastAccount, nil
		}
		last = callErr
		if retryable(callErr) || router.Transient(callErr) {
			g.markCooldown(inst.Provider.ID, model, callErr)
			continue
		}
		return adapter.ImageResponse{}, lastAccount, callErr
	}
	if !tried {
		return adapter.ImageResponse{}, lastAccount, adapter.ErrImageOutUnsupported
	}
	if retryable(last) {
		return adapter.ImageResponse{}, lastAccount, cooldownErr(last)
	}
	return adapter.ImageResponse{}, lastAccount, last
}

func (g *Gateway) supportsImageOut(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	if m, ok := catalog.FindRoutable(g.models, q, id); ok {
		return catalog.HasModality(m, "image_out")
	}
	return slices.Contains(catalog.InferModalities(id), "image_out")
}

// CreateEmbeddings proxies OpenAI-compatible POST /v1/embeddings.
// Upstream is only called when the model is tagged embeddings and the adapter
// implements Embedder with Capabilities.Embeddings.
func (g *Gateway) CreateEmbeddings(ctx context.Context, raw []byte) (adapter.EmbeddingResponse, string, error) {
	model, client, raw, budget, err := g.prepare(raw)
	_ = budget
	if err != nil {
		return adapter.EmbeddingResponse{}, "", err
	}
	if model == "" {
		return adapter.EmbeddingResponse{}, "", adapter.ErrEmbeddingModelRequired
	}
	if !g.supportsEmbeddings(model) {
		return adapter.EmbeddingResponse{}, "", adapter.ErrModelNotEmbeddings
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return adapter.EmbeddingResponse{}, "", err
	}
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if hit, ok := g.cachedEmbeddings(inst.Provider.ID, inst.Provider.BaseURL, model, raw); ok {
			hit.Raw = echoClientModel(hit.Raw, client, model)
			return hit, inst.Provider.ID, nil
		}
	}
	var last error
	var lastAccount string
	tried := false
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		emb, ok := inst.Adapter.(adapter.Embedder)
		if !ok || !inst.Adapter.Capabilities().Embeddings {
			last = adapter.ErrEmbeddingsUnsupported
			continue
		}
		tried = true
		inst.recordAttempt(&lastAccount)
		var resp adapter.EmbeddingResponse
		var callErr error
		callUpstream := func(runCtx context.Context) (adapter.EmbeddingResponse, error) {
			var once adapter.EmbeddingResponse
			var onceErr error
			for attempt := 0; attempt < 2; attempt++ {
				once, onceErr = emb.CreateEmbeddings(runCtx, adapter.EmbeddingRequest{Model: model, Raw: raw})
				if onceErr == nil || !router.Transient(onceErr) || attempt == 1 {
					break
				}
			}
			return once, onceErr
		}
		if g.flight != nil && g.cfg.RequestEngine.CacheEmbeddings && responsecache.Eligible("embeddings", raw, true) {
			var body []byte
			body, callErr = g.flight.Do(ctx, responsecache.Key(inst.Provider.ID, inst.Provider.BaseURL, model, "embeddings", raw), func(runCtx context.Context) ([]byte, error) {
				once, err := callUpstream(runCtx)
				if err != nil {
					return nil, err
				}
				return once.Raw, nil
			})
			if callErr == nil {
				resp.Raw = body
			}
		} else {
			resp, callErr = callUpstream(ctx)
		}
		if callErr == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			resp.Raw = echoClientModel(resp.Raw, client, model)
			g.storeEmbeddings(lastAccount, inst.Provider.BaseURL, model, raw, resp.Raw)
			return resp, lastAccount, nil
		}
		last = callErr
		if retryable(callErr) || router.Transient(callErr) {
			g.markCooldown(inst.Provider.ID, model, callErr)
			continue
		}
		return adapter.EmbeddingResponse{}, lastAccount, callErr
	}
	if !tried {
		return adapter.EmbeddingResponse{}, lastAccount, adapter.ErrEmbeddingsUnsupported
	}
	if retryable(last) {
		return adapter.EmbeddingResponse{}, lastAccount, cooldownErr(last)
	}
	return adapter.EmbeddingResponse{}, lastAccount, last
}

func (g *Gateway) supportsEmbeddings(id string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	if m, ok := catalog.FindRoutable(g.models, q, id); ok {
		return catalog.HasModality(m, "embeddings")
	}
	return slices.Contains(catalog.InferModalities(id), "embeddings")
}

// Responses uses native Responses when available, otherwise OpenAI chat translation.
func (g *Gateway) Responses(ctx context.Context, raw []byte) ([]byte, string, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return nil, "", err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return nil, "", err
	}
	cands = continuationFirst(cands, g.continuationAccount(previousResponseID(raw), model))
	if len(cands) == 0 {
		return nil, "", router.ErrNoAccount
	}
	oaBody, oaReq, xerr := translate.ResponsesToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, false)
		oaReq.Stream = false
	}
	var last error
	var lastAccount string
	budgetAttempts := newAttemptCoordinator(g.cfg.RequestMaxAttempts())
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		if nr, ok := inst.Adapter.(adapter.NativeResponses); ok {
			if err := budgetAttempts.take(); err != nil {
				return nil, lastAccount, errAttemptBudgetExhausted
			}
			inst.recordAttempt(&lastAccount)
			out, err := nr.Responses(ctx, jsonx.SetStream(raw, false))
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				g.bindContinuation(responseID(out), model, lastAccount)
				return echoClientModel(out, client, model), lastAccount, nil
			}
			last = err
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, model, err)
				continue
			}
			return nil, lastAccount, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		var resp adapter.ChatResponse
		var callErr error
		for attempt := 0; attempt < 2; attempt++ {
			if err := budgetAttempts.take(); err != nil {
				return nil, lastAccount, errAttemptBudgetExhausted
			}
			inst.recordAttempt(&lastAccount)
			resp, callErr = inst.Adapter.Chat(ctx, chatReq(inst, oaReq.Model, oaReq.Raw, false, budget))
			if callErr == nil || !router.Transient(callErr) || attempt == 1 {
				break
			}
		}
		if callErr != nil {
			last = callErr
			if retryable(callErr) || router.Transient(callErr) {
				g.markCooldown(inst.Provider.ID, model, callErr)
				continue
			}
			return nil, lastAccount, callErr
		}
		if len(resp.Raw) > 0 {
			out, ferr := translate.FromOpenAIChat(resp.Raw, client)
			if ferr == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				return echoClientModel(out, client, model), lastAccount, nil
			}
		}
		out, err := translate.FromChatContent(resp.ID, client, resp.Content)
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
		}
		return echoClientModel(out, client, model), lastAccount, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if retryable(last) {
		return nil, lastAccount, cooldownErr(last)
	}
	return nil, lastAccount, last
}

// ResponsesStream writes Responses SSE (native pass-through or converted OpenAI stream).
func (g *Gateway) ResponsesStream(ctx context.Context, raw []byte, w io.Writer) (string, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return "", err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return "", err
	}
	oaBody, oaReq, xerr := translate.ResponsesToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, true)
		oaReq.Stream = true
	}
	cw := &countWriter{w: w}
	guard := streamguard.New(cw, 0, g.streamPrelude(model))
	var last error
	var lastAccount string
	var slowSkipped bool
	budgetAttempts := newAttemptCoordinator(g.cfg.RequestMaxAttempts())
	reachable := lastReachable[adapter.NativeResponses](cands, xerr == nil)
	for i, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		dest := newRouteRewriter(guard, model, client)
		if nr, ok := inst.Adapter.(adapter.NativeResponses); ok {
			if err := budgetAttempts.take(); err != nil {
				return lastAccount, errAttemptBudgetExhausted
			}
			guard.Reset()
			if budgetAttempts.final(i, reachable) {
				guard.Unbounded()
			}
			attemptCtx, stop := guard.Bound(ctx)
			inst.recordAttempt(&lastAccount)
			err := noteStream(guard, nr.ResponsesStream(attemptCtx, jsonx.SetStream(raw, true), dest))
			stop()
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				return lastAccount, nil
			}
			last = err
			if cw.n > 0 {
				return lastAccount, err
			}
			if slowPrelude(err) {
				slowSkipped = true
				continue
			}
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, model, err)
				continue
			}
			return lastAccount, err
		}
		if xerr != nil {
			if !slowPrelude(last) {
				last = xerr
			}
			continue
		}
		if err := budgetAttempts.take(); err != nil {
			return lastAccount, errAttemptBudgetExhausted
		}
		guard.Reset()
		if budgetAttempts.final(i, reachable) {
			guard.Unbounded()
		}
		attemptCtx, stop := guard.Bound(ctx)
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			errCh <- translate.OpenAISSEToResponses(pr, guard, model)
			_ = pr.Close()
		}()
		inst.recordAttempt(&lastAccount)
		err := noteStream(guard, inst.Adapter.ChatStream(attemptCtx, chatReq(inst, oaReq.Model, oaReq.Raw, true, budget), pw))
		stop()
		// A failed call reaches the translator as a read error, not a clean EOF
		// (nil closes normally), so it never completes the partial turn.
		_ = pw.CloseWithError(err)
		convErr := <-errCh
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			return lastAccount, convErr
		}
		last = err
		if cw.n > 0 {
			return lastAccount, err
		}
		if slowPrelude(err) {
			slowSkipped = true
			continue
		}
		if retryable(err) {
			g.markCooldown(inst.Provider.ID, model, err)
			continue
		}
		return lastAccount, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if retryable(last) && !slowSkipped {
		return lastAccount, cooldownErr(last)
	}
	return lastAccount, last
}

// ClaudeChat uses native Messages when available, otherwise OpenAI translation.
func (g *Gateway) ClaudeChat(ctx context.Context, raw []byte) ([]byte, string, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return nil, "", err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return nil, "", err
	}
	oaBody, oaReq, xerr := translate.ToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = oaBody
		oaReq.Stream = false
	}
	var last error
	var lastAccount string
	budgetAttempts := newAttemptCoordinator(g.cfg.RequestMaxAttempts())
	for _, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		if nm, ok := inst.Adapter.(adapter.NativeMessages); ok {
			if err := budgetAttempts.take(); err != nil {
				return nil, lastAccount, errAttemptBudgetExhausted
			}
			inst.recordAttempt(&lastAccount)
			out, err := nm.Messages(ctx, jsonx.SetStream(claudeRaw(g.promptBody(raw, inst.Provider.Adapter), budget), false))
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				return echoClientModel(out, client, model), lastAccount, nil
			}
			last = err
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, model, err)
				continue
			}
			return nil, lastAccount, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		if err := budgetAttempts.take(); err != nil {
			return nil, lastAccount, errAttemptBudgetExhausted
		}
		inst.recordAttempt(&lastAccount)
		resp, err := inst.Adapter.Chat(ctx, chatReq(inst, oaReq.Model, oaReq.Raw, false, budget))
		if err != nil {
			last = err
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, model, err)
				continue
			}
			return nil, lastAccount, err
		}
		oaRaw := resp.Raw
		if len(oaRaw) == 0 {
			oaRaw, err = json.Marshal(openAIShim{
				ID:    "peaproxy",
				Model: oaReq.Model,
				Choices: []openAIChoice{{
					Message:      openAIMsg{Role: "assistant", Content: resp.Content},
					FinishReason: "stop",
				}},
			})
			if err != nil {
				return nil, lastAccount, err
			}
		}
		out, err := translate.FromOpenAI(oaRaw, client)
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
		}
		return echoClientModel(out, client, model), lastAccount, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if retryable(last) {
		return nil, lastAccount, cooldownErr(last)
	}
	return nil, lastAccount, last
}

// ClaudeChatStream writes true Anthropic SSE (native pass-through or converted OpenAI stream).
func (g *Gateway) ClaudeChatStream(ctx context.Context, raw []byte, w io.Writer) (string, error) {
	ctx, cancel := g.requestContext(ctx)
	defer cancel()
	model, client, raw, budget, err := g.prepare(raw)
	if err != nil {
		return "", err
	}
	model, cands, session, err := g.routeResolved(ctx, raw, model)
	if err != nil {
		return "", err
	}
	oaBody, oaReq, xerr := translate.ToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, true)
		oaReq.Stream = true
	}
	cw := &countWriter{w: w}
	guard := streamguard.New(cw, 0, g.streamPrelude(model))
	var last error
	var lastAccount string
	var slowSkipped bool
	budgetAttempts := newAttemptCoordinator(g.cfg.RequestMaxAttempts())
	reachable := lastReachable[adapter.NativeMessages](cands, xerr == nil)
	for i, inst := range cands {
		model, raw := inst.applyModel(model, raw)
		if len(oaReq.Raw) > 0 {
			oaReq.Model, oaReq.Raw = inst.applyModel(model, oaReq.Raw)
		}
		dest := newRouteRewriter(guard, model, client)
		if nm, ok := inst.Adapter.(adapter.NativeMessages); ok {
			if err := budgetAttempts.take(); err != nil {
				return lastAccount, errAttemptBudgetExhausted
			}
			guard.Reset()
			if budgetAttempts.final(i, reachable) {
				guard.Unbounded()
			}
			attemptCtx, stop := guard.Bound(ctx)
			inst.recordAttempt(&lastAccount)
			err := noteStream(guard, nm.MessagesStream(attemptCtx, jsonx.SetStream(claudeRaw(g.promptBody(raw, inst.Provider.Adapter), budget), true), dest))
			stop()
			if err == nil {
				g.rememberSuccess(session, model, lastAccount)
				g.rememberSuccess(session, client, lastAccount)
				return lastAccount, nil
			}
			last = err
			if cw.n > 0 {
				return lastAccount, err
			}
			if slowPrelude(err) {
				slowSkipped = true
				continue
			}
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, model, err)
				continue
			}
			return lastAccount, err
		}
		if xerr != nil {
			if !slowPrelude(last) {
				last = xerr
			}
			continue
		}
		if err := budgetAttempts.take(); err != nil {
			return lastAccount, errAttemptBudgetExhausted
		}
		guard.Reset()
		if budgetAttempts.final(i, reachable) {
			guard.Unbounded()
		}
		attemptCtx, stop := guard.Bound(ctx)
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			errCh <- translate.OpenAISSEToClaude(pr, guard, model)
			_ = pr.Close()
		}()
		inst.recordAttempt(&lastAccount)
		err := noteStream(guard, inst.Adapter.ChatStream(attemptCtx, chatReq(inst, oaReq.Model, oaReq.Raw, true, budget), pw))
		stop()
		// A failed call reaches the translator as a read error, not a clean EOF
		// (nil closes normally), so it never completes the partial turn.
		_ = pw.CloseWithError(err)
		convErr := <-errCh
		if err == nil {
			g.rememberSuccess(session, model, lastAccount)
			g.rememberSuccess(session, client, lastAccount)
			return lastAccount, convErr
		}
		last = err
		if cw.n > 0 {
			return lastAccount, err
		}
		if slowPrelude(err) {
			slowSkipped = true
			continue
		}
		if retryable(err) {
			g.markCooldown(inst.Provider.ID, model, err)
			continue
		}
		return lastAccount, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	if retryable(last) && !slowSkipped {
		return lastAccount, cooldownErr(last)
	}
	return lastAccount, last
}

type openAIShim struct {
	ID      string         `json:"id"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Message      openAIMsg `json:"message"`
	FinishReason string    `json:"finish_reason"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type countWriter struct {
	w io.Writer
	n int
}

func noteStream(guard *streamguard.Guard, callErr error) error {
	if guard == nil {
		return callErr
	}
	if callErr == nil {
		if err := guard.Finish(); err != nil {
			return err
		}
	}
	if perr := guard.Err(); perr != nil && (callErr == nil || errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded)) {
		return perr
	}
	return callErr
}

func preludeFailover(err error) bool {
	return errors.Is(err, streamguard.ErrPrelude) || errors.Is(err, streamguard.ErrTimeout)
}

// slowPrelude is a first event that took too long. It moves on to the next
// account but is not an account failure, so it never starts a cooldown.
func slowPrelude(err error) bool {
	return errors.Is(err, streamguard.ErrTimeout)
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

func retryable(err error) bool {
	return router.Retryable(err)
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
func cooldownFor(err error) time.Duration {
	var he adapter.HTTPError
	if errors.As(err, &he) && he.RetryAfter > 0 {
		return he.RetryAfter
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
	g.noteStatLocked(account, nil, 0)
	g.mu.Unlock()
	g.rememberSticky(model, account)
	g.bindAffinity(session, model, account)
}

func (g *Gateway) admit(ctx context.Context, account string) (func(), error) {
	if g.admission == nil || g.cfg.RequestEngine.MaxInFlight <= 0 {
		return func() {}, nil
	}
	g.admission.MaxInFlight = g.cfg.RequestEngine.MaxInFlight
	return g.admission.Acquire(ctx, account)
}

func (g *Gateway) bindAffinity(session, model, account string) {
	if session == "" || model == "" || account == "" || !g.cfg.AffinityEnabled() {
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
		c, ok := g.cool[id]
		if !ok || !now.Before(c.Until) {
			parts = append(parts, fmt.Sprintf("%s recovering from cooldown, retry shortly", id))
			continue
		}
		if c.Model != "" && c.Model != model {
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

func (g *Gateway) markCooldown(id, model string, err error) {
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
	g.cool[id] = Cooldown{AccountID: id, Model: scopedModel, Until: time.Now().Add(wait), Reason: reason}
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
	for id, c := range g.cool {
		if now.After(c.Until) {
			delete(g.cool, id)
			continue
		}
		c.RemainingMs = c.Until.Sub(now).Milliseconds()
		if c.RemainingMs < 0 {
			c.RemainingMs = 0
		}
		c.QuotaHint = g.quotaHintFor(id)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	if out == nil {
		out = []Cooldown{}
	}
	return out
}

// AdapterHealth returns last probe/ListModels status, overlaying active cooldowns.
func (g *Gateway) AdapterHealth() []AdapterHealth {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	out := make([]AdapterHealth, len(g.health))
	copy(out, g.health)
	for i := range out {
		if c, ok := g.cool[out[i].AccountID]; ok && now.Before(c.Until) && out[i].Status == "ok" {
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
			Capabilities: inst.Adapter.Capabilities(),
		}
		if old, ok := prev[inst.Provider.ID]; ok {
			h.Models = old.Models
		}
		if err != nil {
			h.Status = "error"
			h.Error = usage.Redact(err.Error())
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
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	expired := map[string]struct{}{}
	for id, c := range g.cool {
		if !now.Before(c.Until) {
			delete(g.cool, id)
			expired[id] = struct{}{}
		}
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
		if c, ok := g.cool[inst.Provider.ID]; ok && now.Before(c.Until) && (c.Model == "" || c.Model == model) {
			if until.IsZero() || c.Until.Before(until) {
				until = c.Until
			}
			continue
		}
		if _, just := expired[inst.Provider.ID]; just && g.admission != nil && !g.admission.TryHalfOpen(inst.Provider.ID) {
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

// SetRequestLog toggles the opt-in redacted JSONL log and persists config.
func (g *Gateway) SetRequestLog(on bool) error {
	g.mu.Lock()
	g.cfg.RequestLog = on
	path := g.path
	cfg := g.cfg
	g.mu.Unlock()
	if g.Usage != nil {
		if on && path != "" {
			g.Usage.SetRequestLog(filepath.Join(filepath.Dir(path), "requests.log"))
		} else {
			g.Usage.SetRequestLog("")
		}
	}
	if path != "" {
		return config.Save(path, cfg)
	}
	return nil
}

// AddProvider appends an account, rebuilds, saves, and refreshes.
func (g *Gateway) AddProvider(ctx context.Context, p config.Provider) error {
	g.mu.Lock()
	for _, e := range g.cfg.Providers {
		if e.ID == p.ID {
			g.mu.Unlock()
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
		return err
	}
	g.cfg.Providers = append(g.cfg.Providers, p)
	if err := g.rebuild(); err != nil {
		g.mu.Unlock()
		return err
	}
	path := g.path
	cfg := g.cfg
	g.mu.Unlock()
	if path != "" {
		if err := config.Save(path, cfg); err != nil {
			return err
		}
	}
	g.Refresh(ctx)
	return nil
}

// RemoveProvider deletes an account.
func (g *Gateway) RemoveProvider(ctx context.Context, id string) error {
	g.mu.Lock()
	kept := g.cfg.Providers[:0]
	for _, p := range g.cfg.Providers {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	g.cfg.Providers = kept
	for responseID, bind := range g.continuations {
		if bind.Account == id {
			delete(g.continuations, responseID)
		}
	}
	if g.responses != nil {
		g.responses.InvalidatePrefix(id + "\x00")
	}
	_ = g.rebuild()
	path, cfg := g.path, g.cfg
	g.mu.Unlock()
	if path != "" {
		if err := config.Save(path, cfg); err != nil {
			return err
		}
	}
	g.Refresh(ctx)
	return nil
}

// ToggleHide listing-only hide for a provider or model id.
func (g *Gateway) ToggleHide(kind, id string, hidden bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch kind {
	case "provider":
		g.cfg.Hide.Providers = setHidden(g.cfg.Hide.Providers, id, hidden)
	case "model":
		g.cfg.Hide.Models = setHidden(g.cfg.Hide.Models, id, hidden)
	default:
		return fmt.Errorf("kind must be provider or model")
	}
	if g.path != "" {
		return config.Save(g.path, g.cfg)
	}
	return nil
}

// SetCatalogOverlay pins and/or renames a live model id. Empty displayName clears the overlay.
func (g *Gateway) SetCatalogOverlay(id string, displayName *string, pinned *bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if pinned != nil {
		g.cfg.Catalog.Pin = setHidden(g.cfg.Catalog.Pin, id, *pinned)
	}
	if displayName != nil {
		name := strings.TrimSpace(*displayName)
		if g.cfg.Catalog.Rename == nil {
			g.cfg.Catalog.Rename = map[string]string{}
		}
		if name == "" {
			delete(g.cfg.Catalog.Rename, id)
		} else {
			g.cfg.Catalog.Rename[id] = name
		}
		if len(g.cfg.Catalog.Rename) == 0 {
			g.cfg.Catalog.Rename = nil
		}
	}
	if g.path != "" {
		return config.Save(g.path, g.cfg)
	}
	return nil
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

// Save persists config.
func (g *Gateway) Save() error {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.path == "" {
		return errors.New("no config path")
	}
	return config.Save(g.path, g.cfg)
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

// SaveOAuth persists refreshed subscription tokens via the secret store.
func (g *Gateway) SaveOAuth(id string, tok oauth.Token) error {
	ct := config.OAuthFromRuntime(tok)
	g.mu.Lock()
	for i := range g.cfg.Providers {
		if g.cfg.Providers[i].ID == id {
			g.cfg.Providers[i].OAuth = &ct
			break
		}
	}
	path := g.path
	cfg := g.cfg
	g.mu.Unlock()
	if path != "" {
		return config.Save(path, cfg)
	}
	return nil
}
