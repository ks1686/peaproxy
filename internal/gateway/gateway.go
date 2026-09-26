package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/translate"
	"github.com/ks1686/peaproxy/internal/usage"
)

// Gateway owns config, live adapters, catalog, and usage.
type Gateway struct {
	mu     sync.RWMutex
	cfg    config.Config
	path   string
	reg    *adapter.Registry
	inst   []instance
	models []catalog.Model
	Usage  *usage.Store
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
	g := &Gateway{cfg: cfg, path: path, reg: reg, Usage: &usage.Store{}}
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
		adp, err := g.reg.Open(p.Adapter, adapter.Options{
			ID:        p.ID,
			BaseURL:   p.BaseURL,
			APIKey:    p.ResolveKey(),
			SessionID: p.SessionID,
			Tier:      catalog.Tier(p.Tier),
		})
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
	defer g.mu.Unlock()
	var all []catalog.Model
	for _, inst := range g.inst {
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
	g.models = all
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

// Chat proxies a non-stream OpenAI chat.completions body.
func (g *Gateway) Chat(ctx context.Context, raw []byte) (adapter.ChatResponse, string, error) {
	peek := jsonx.PeekBody(raw)
	adp, account, err := g.route(peek.Model)
	if err != nil {
		return adapter.ChatResponse{}, "", err
	}
	resp, err := adp.Chat(ctx, adapter.ChatRequest{Model: peek.Model, Raw: raw, Stream: false})
	return resp, account, err
}

// ChatStream proxies SSE.
func (g *Gateway) ChatStream(ctx context.Context, raw []byte, w io.Writer) (string, error) {
	peek := jsonx.PeekBody(raw)
	adp, account, err := g.route(peek.Model)
	if err != nil {
		return "", err
	}
	return account, adp.ChatStream(ctx, adapter.ChatRequest{Model: peek.Model, Raw: raw, Stream: true}, w)
}

// ClaudeChat translates Messages → OpenAI → Messages.
func (g *Gateway) ClaudeChat(ctx context.Context, raw []byte) ([]byte, string, error) {
	_, req, err := translate.ToOpenAI(raw)
	if err != nil {
		return nil, "", err
	}
	adp, account, err := g.route(req.Model)
	if err != nil {
		return nil, "", err
	}
	resp, err := adp.Chat(ctx, req)
	if err != nil {
		return nil, account, err
	}
	oaRaw := resp.Raw
	if len(oaRaw) == 0 {
		oaRaw, err = json.Marshal(openAIShim{
			ID:    "peaproxy",
			Model: req.Model,
			Choices: []openAIChoice{{
				Message:      openAIMsg{Role: "assistant", Content: resp.Content},
				FinishReason: "stop",
			}},
		})
		if err != nil {
			return nil, account, err
		}
	}
	out, err := translate.FromOpenAI(oaRaw, req.Model)
	return out, account, err
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

func (g *Gateway) route(model string) (adapter.Adapter, string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	q := g.queryLocked()
	if m, ok := catalog.FindRoutable(g.models, q, model); ok {
		for _, inst := range g.inst {
			if inst.Provider.ID == m.AccountID {
				return inst.Adapter, inst.Provider.ID, nil
			}
		}
	}
	if len(g.inst) == 0 {
		return nil, "", router.ErrNoAccount
	}
	return g.inst[0].Adapter, g.inst[0].Provider.ID, nil
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
	}
	return out
}
