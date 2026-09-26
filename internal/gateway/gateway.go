package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
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

const cooldownTTL = 30 * time.Second

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
}

// Cooldown is a temporary skip of an account after 429/401.
type Cooldown struct {
	AccountID string    `json:"accountId"`
	Until     time.Time `json:"until"`
	Reason    string    `json:"reason"`
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
	g := &Gateway{cfg: cfg, path: path, reg: reg, cool: map[string]Cooldown{}}
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
	}
}

// Refresh pulls live ListModels from every adapter.
func (g *Gateway) Refresh(ctx context.Context) {
	g.mu.Lock()
	inst := append([]instance(nil), g.inst...)
	g.mu.Unlock()
	var all []catalog.Model
	for _, inst := range inst {
		models, err := inst.Adapter.ListModels(ctx)
		if err != nil {
			all = append(all, catalog.Model{
				ID:        inst.Provider.ID + ":unavailable",
				Provider:  inst.Provider.Adapter,
				AccountID: inst.Provider.ID,
				Tier:      catalog.Tier(inst.Provider.Tier),
				Status:    "auth_error",
				Routable:  false,
				Exposed:   false,
			})
			continue
		}
		all = append(all, models...)
	}
	g.mu.Lock()
	g.models = all
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

// Chat proxies a non-stream OpenAI chat.completions body with round-robin + 429/401 failover.
func (g *Gateway) Chat(ctx context.Context, raw []byte) (adapter.ChatResponse, string, error) {
	peek := jsonx.PeekBody(raw)
	cands := g.candidates(peek.Model)
	if len(cands) == 0 {
		return adapter.ChatResponse{}, "", router.ErrNoAccount
	}
	req := adapter.ChatRequest{Model: peek.Model, Raw: raw, Stream: false}
	var last error
	var lastAccount string
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		resp, err := inst.Adapter.Chat(ctx, req)
		if err == nil {
			return resp, lastAccount, nil
		}
		last = err
		if retryable(err) {
			g.markCooldown(inst.Provider.ID, err)
			continue
		}
		return adapter.ChatResponse{}, lastAccount, err
	}
	return adapter.ChatResponse{}, lastAccount, last
}

// ChatStream proxies SSE with failover before any bytes are written.
func (g *Gateway) ChatStream(ctx context.Context, raw []byte, w io.Writer) (string, error) {
	peek := jsonx.PeekBody(raw)
	cands := g.candidates(peek.Model)
	if len(cands) == 0 {
		return "", router.ErrNoAccount
	}
	cw := &countWriter{w: w}
	req := adapter.ChatRequest{Model: peek.Model, Raw: raw, Stream: true}
	var last error
	var lastAccount string
	for _, inst := range cands {
		lastAccount = inst.Provider.ID
		err := inst.Adapter.ChatStream(ctx, req, cw)
		if err == nil {
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
	return lastAccount, last
}

// ClaudeChat uses native Messages when available, otherwise OpenAI translation.
func (g *Gateway) ClaudeChat(ctx context.Context, raw []byte) ([]byte, string, error) {
	peek := jsonx.PeekBody(raw)
	cands := g.candidates(peek.Model)
	if len(cands) == 0 {
		return nil, "", router.ErrNoAccount
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
		return out, lastAccount, err
	}
	if last == nil {
		last = xerr
	}
	if last == nil {
		last = router.ErrNoAccount
	}
	return nil, lastAccount, last
}

// ClaudeChatStream writes true Anthropic SSE (native pass-through or converted OpenAI stream).
func (g *Gateway) ClaudeChatStream(ctx context.Context, raw []byte, w io.Writer) (string, error) {
	peek := jsonx.PeekBody(raw)
	cands := g.candidates(peek.Model)
	if len(cands) == 0 {
		return "", router.ErrNoAccount
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
	var he adapter.HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests || he.Status == http.StatusUnauthorized
	}
	return false
}

func (g *Gateway) markCooldown(id string, err error) {
	reason := "failover"
	var he adapter.HTTPError
	if errors.As(err, &he) {
		reason = fmt.Sprintf("HTTP %d", he.Status)
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
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	if out == nil {
		out = []Cooldown{}
	}
	return out
}

func (g *Gateway) candidates(model string) []instance {
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
	cool := make([]instance, 0)
	for _, inst := range matched {
		if c, ok := g.cool[inst.Provider.ID]; ok && now.Before(c.Until) {
			cool = append(cool, inst)
			continue
		}
		hot = append(hot, inst)
	}
	pool := hot
	if len(pool) == 0 {
		pool = cool
	}
	if len(pool) == 0 {
		return nil
	}
	start := int(g.rr % uint64(len(pool)))
	g.rr++
	out := make([]instance, 0, len(pool))
	out = append(out, pool[start:]...)
	out = append(out, pool[:start]...)
	return out
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

// SaveOAuth persists refreshed subscription tokens (0600 YAML).
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
