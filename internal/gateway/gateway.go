package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/translate"
	"github.com/ks1686/peaproxy/internal/usage"
)

const CooldownTTL = 30 * time.Second

// cooldownTTL is the skip window after a retryable failure. Tests may shorten it.
var cooldownTTL = CooldownTTL

// Gateway owns config, live adapters, catalog, and usage.
type Gateway struct {
	mu     sync.RWMutex
	cfg    config.Config
	path   string
	reg    *adapter.Registry
	inst   []instance
	models []catalog.Model
	Usage  *usage.Store
	cool   map[string]Cooldown
	rr     uint64
	sticky map[string]string
	health []AdapterHealth
}

// Cooldown is a temporary skip of an account after a retryable failure.
type Cooldown struct {
	AccountID   string    `json:"accountId"`
	Until       time.Time `json:"until"`
	Reason      string    `json:"reason"`
	RemainingMs int64     `json:"remainingMs"`
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
}

type instance struct {
	Provider config.Provider
	Adapter  adapter.Adapter
}

// New builds adapters from cfg. Call Refresh after.
func New(cfg config.Config, path string, reg *adapter.Registry) (*Gateway, error) {
	if reg == nil {
		reg = adapters.DefaultRegistry()
	}
	g := &Gateway{cfg: cfg, path: path, reg: reg, cool: map[string]Cooldown{}, sticky: map[string]string{}}
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
		health = append(health, h)
		all = append(all, models...)
	}
	g.mu.Lock()
	g.models = all
	g.health = health
	g.mu.Unlock()
}

// Models is the last refreshed full catalog (including hidden).
func (g *Gateway) Models() []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]catalog.Model, len(g.models))
	copy(out, g.models)
	return out
}

// Listed applies hide/expose for /v1/models.
func (g *Gateway) Listed(filter catalog.Filter) []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	q.Filter = filter
	return catalog.List(g.models, q)
}

// Annotated is the UI catalog (hidden rows included).
func (g *Gateway) Annotated(filter catalog.Filter) []catalog.Model {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	q.Filter = filter
	q.ForClients = false
	return catalog.AllAnnotated(g.models, q)
}

// Chat proxies a non-stream OpenAI chat.completions body with policy-based failover.
func (g *Gateway) Chat(ctx context.Context, raw []byte) (adapter.ChatResponse, string, error) {
	peek := jsonx.PeekBody(raw)
	cands, err := g.route(peek.Model)
	if err != nil {
		return adapter.ChatResponse{}, "", err
	}
	req := adapter.ChatRequest{Model: peek.Model, Raw: raw, Stream: false}
	var last error
	var lastAccount string
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		resp, err := inst.Adapter.Chat(ctx, req)
		if err == nil {
			g.rememberSticky(peek.Model, lastAccount)
			return resp, lastAccount, nil
		}
		last = err
		if retryable(err) {
			g.markCooldown(inst.Provider.ID, err)
			continue
		}
		return adapter.ChatResponse{}, lastAccount, err
	}
	return adapter.ChatResponse{}, lastAccount, cooldownErr(last)
}

// ChatStream proxies SSE with failover before any bytes are written.
func (g *Gateway) ChatStream(ctx context.Context, raw []byte, w io.Writer) (string, error) {
	peek := jsonx.PeekBody(raw)
	cands, err := g.route(peek.Model)
	if err != nil {
		return "", err
	}
	cw := &countWriter{w: w}
	req := adapter.ChatRequest{Model: peek.Model, Raw: raw, Stream: true}
	var last error
	var lastAccount string
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		err := inst.Adapter.ChatStream(ctx, req, cw)
		if err == nil {
			g.rememberSticky(peek.Model, lastAccount)
			return lastAccount, nil
		}
		last = err
		if cw.n > 0 {
			return lastAccount, err
		}
		if retryable(err) {
			g.markCooldown(inst.Provider.ID, err)
			continue
		}
		return lastAccount, err
	}
	return lastAccount, cooldownErr(last)
}

// Responses uses native Responses when available, otherwise OpenAI chat translation.
func (g *Gateway) Responses(ctx context.Context, raw []byte) ([]byte, string, error) {
	peek := jsonx.PeekBody(raw)
	cands, err := g.route(peek.Model)
	if err != nil {
		return nil, "", err
	}
	oaBody, oaReq, xerr := translate.ResponsesToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, false)
		oaReq.Stream = false
	}
	var last error
	var lastAccount string
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		if nr, ok := inst.Adapter.(adapter.NativeResponses); ok {
			out, err := nr.Responses(ctx, jsonx.SetStream(raw, false))
			if err == nil {
				g.rememberSticky(peek.Model, lastAccount)
				return out, lastAccount, nil
			}
			last = err
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, err)
				continue
			}
			return nil, lastAccount, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		resp, err := inst.Adapter.Chat(ctx, oaReq)
		if err != nil {
			last = err
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, err)
				continue
			}
			return nil, lastAccount, err
		}
		if len(resp.Raw) > 0 {
			out, ferr := translate.FromOpenAIChat(resp.Raw, oaReq.Model)
			if ferr == nil {
				g.rememberSticky(peek.Model, lastAccount)
				return out, lastAccount, nil
			}
		}
		out, err := translate.FromChatContent(resp.ID, oaReq.Model, resp.Content)
		if err == nil {
			g.rememberSticky(peek.Model, lastAccount)
		}
		return out, lastAccount, err
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
	peek := jsonx.PeekBody(raw)
	cands, err := g.route(peek.Model)
	if err != nil {
		return "", err
	}
	oaBody, oaReq, xerr := translate.ResponsesToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, true)
		oaReq.Stream = true
	}
	cw := &countWriter{w: w}
	var last error
	var lastAccount string
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		if nr, ok := inst.Adapter.(adapter.NativeResponses); ok {
			err := nr.ResponsesStream(ctx, jsonx.SetStream(raw, true), cw)
			if err == nil {
				g.rememberSticky(peek.Model, lastAccount)
				return lastAccount, nil
			}
			last = err
			if cw.n > 0 {
				return lastAccount, err
			}
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, err)
				continue
			}
			return lastAccount, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			errCh <- translate.OpenAISSEToResponses(pr, cw, peek.Model)
			_ = pr.Close()
		}()
		err := inst.Adapter.ChatStream(ctx, oaReq, pw)
		_ = pw.Close()
		convErr := <-errCh
		if err == nil {
			g.rememberSticky(peek.Model, lastAccount)
			return lastAccount, convErr
		}
		last = err
		if cw.n > 0 {
			return lastAccount, err
		}
		if retryable(err) {
			g.markCooldown(inst.Provider.ID, err)
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
	if retryable(last) {
		return lastAccount, cooldownErr(last)
	}
	return lastAccount, last
}

// ClaudeChat uses native Messages when available, otherwise OpenAI translation.
func (g *Gateway) ClaudeChat(ctx context.Context, raw []byte) ([]byte, string, error) {
	peek := jsonx.PeekBody(raw)
	cands, err := g.route(peek.Model)
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
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		if nm, ok := inst.Adapter.(adapter.NativeMessages); ok {
			out, err := nm.Messages(ctx, jsonx.SetStream(raw, false))
			if err == nil {
				g.rememberSticky(peek.Model, lastAccount)
				return out, lastAccount, nil
			}
			last = err
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, err)
				continue
			}
			return nil, lastAccount, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		resp, err := inst.Adapter.Chat(ctx, oaReq)
		if err != nil {
			last = err
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, err)
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
		out, err := translate.FromOpenAI(oaRaw, oaReq.Model)
		if err == nil {
			g.rememberSticky(peek.Model, lastAccount)
		}
		return out, lastAccount, err
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
	peek := jsonx.PeekBody(raw)
	cands, err := g.route(peek.Model)
	if err != nil {
		return "", err
	}
	oaBody, oaReq, xerr := translate.ToOpenAI(raw)
	if xerr == nil {
		oaReq.Raw = jsonx.SetStream(oaBody, true)
		oaReq.Stream = true
	}
	cw := &countWriter{w: w}
	var last error
	var lastAccount string
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		if nm, ok := inst.Adapter.(adapter.NativeMessages); ok {
			err := nm.MessagesStream(ctx, jsonx.SetStream(raw, true), cw)
			if err == nil {
				g.rememberSticky(peek.Model, lastAccount)
				return lastAccount, nil
			}
			last = err
			if cw.n > 0 {
				return lastAccount, err
			}
			if retryable(err) {
				g.markCooldown(inst.Provider.ID, err)
				continue
			}
			return lastAccount, err
		}
		if xerr != nil {
			last = xerr
			continue
		}
		pr, pw := io.Pipe()
		errCh := make(chan error, 1)
		go func() {
			errCh <- translate.OpenAISSEToClaude(pr, cw, peek.Model)
			_ = pr.Close()
		}()
		err := inst.Adapter.ChatStream(ctx, oaReq, pw)
		_ = pw.Close()
		convErr := <-errCh
		if err == nil {
			g.rememberSticky(peek.Model, lastAccount)
			return lastAccount, convErr
		}
		last = err
		if cw.n > 0 {
			return lastAccount, err
		}
		if retryable(err) {
			g.markCooldown(inst.Provider.ID, err)
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
	if retryable(last) {
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

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

func retryable(err error) bool {
	return router.Retryable(err)
}

func cooldownErr(last error) error {
	return router.CooldownError{RetryAfter: cooldownTTL, Err: last}
}

func (g *Gateway) rememberSticky(model, account string) {
	if model == "" || account == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sticky[model] = account
}

func (g *Gateway) route(model string) ([]instance, error) {
	cands, retry := g.candidates(model)
	if len(cands) == 0 {
		if retry > 0 {
			return nil, router.CooldownError{RetryAfter: retry}
		}
		return nil, router.ErrNoAccount
	}
	return cands, nil
}

func (g *Gateway) markCooldown(id string, err error) {
	class := router.Classify(err)
	reason := class.String()
	if class == router.FailoverNone {
		reason = "failover"
	}
	var he adapter.HTTPError
	if errors.As(err, &he) {
		reason = fmt.Sprintf("HTTP %d (%s)", he.Status, reason)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cool[id] = Cooldown{AccountID: id, Until: time.Now().Add(cooldownTTL), Reason: reason}
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
	return g.AdapterHealth()
}

func (g *Gateway) candidates(model string) ([]instance, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for id, c := range g.cool {
		if !now.Before(c.Until) {
			delete(g.cool, id)
		}
	}
	q := g.queryLocked()
	ids := catalog.AccountsForModel(g.models, q, model)
	var matched []instance
	if len(ids) > 0 {
		want := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			want[id] = struct{}{}
		}
		for _, inst := range g.inst {
			if _, ok := want[inst.Provider.ID]; ok {
				matched = append(matched, inst)
			}
		}
	}
	if len(matched) == 0 {
		matched = append(matched, g.inst...)
	}
	hot := make([]instance, 0, len(matched))
	var until time.Time
	for _, inst := range matched {
		if c, ok := g.cool[inst.Provider.ID]; ok && now.Before(c.Until) {
			if until.IsZero() || c.Until.Before(until) {
				until = c.Until
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
	ordered := router.Order(router.Policy(g.cfg.Failover.Policy), hotIDs, &g.rr, g.sticky[model])
	out := make([]instance, 0, len(ordered))
	for _, id := range ordered {
		out = append(out, byID[id])
	}
	return out, 0
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
