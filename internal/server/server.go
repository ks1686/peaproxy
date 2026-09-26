package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/clients"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/ui"
	"github.com/ks1686/peaproxy/internal/usage"
)

const maxBody = 8 << 20

// Server is the localhost gateway (OpenAI + Claude + admin + UI).
type Server struct {
	gw   *gateway.Gateway
	http *http.Server
}

// Options wires dependencies for tests.
type Options struct {
	Gateway *gateway.Gateway
	Config  config.Config
	Models  []catalog.Model
}

// New builds an HTTP server that is not yet listening.
func New(opts Options) *Server {
	s := &Server{gw: opts.Gateway}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	mux.HandleFunc("GET /v0/catalog", s.handleCatalogAPI)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/messages", s.handleClaudeMessages)
	mux.HandleFunc("GET /admin/health", s.handleHealth)
	mux.HandleFunc("GET /admin/accounts", s.handleAccounts)
	mux.HandleFunc("POST /admin/accounts", s.handleAddAccount)
	mux.HandleFunc("DELETE /admin/accounts/{id}", s.handleDeleteAccount)
	mux.HandleFunc("GET /admin/catalog", s.handleAdminCatalog)
	mux.HandleFunc("POST /admin/catalog/refresh", s.handleRefresh)
	mux.HandleFunc("POST /admin/hide", s.handleHide)
	mux.HandleFunc("POST /admin/showcase", s.handleShowcase)
	mux.HandleFunc("GET /admin/usage", s.handleUsage)
	mux.HandleFunc("GET /admin/clients", s.handleClients)
	mux.HandleFunc("GET /admin/settings", s.handleSettings)
	uiFS, err := fs.Sub(ui.FS, "web")
	if err != nil {
		log.Printf("ui embed: %v", err)
		uiFS = ui.FS
	}
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(uiFS))))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	addr := "127.0.0.1:8317"
	if s.gw != nil {
		addr = s.gw.Config().Addr()
	} else if opts.Config.Port != 0 {
		addr = opts.Config.Addr()
	}
	s.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Handler exposes the mux for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// ListenAndServe binds cfg.Addr (default 127.0.0.1:8317).
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	log.Printf("peaproxy listening on http://%s", ln.Addr())
	return s.http.Serve(ln)
}

// Shutdown stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	_ = r
	data, err := ui.FS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	filter := catalog.Filter(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = catalog.FilterAll
	}
	list := catalog.ToOpenAIList(s.gw.Listed(filter))
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCatalogAPI(w http.ResponseWriter, r *http.Request) {
	filter := catalog.Filter(r.URL.Query().Get("filter"))
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"models": s.gw.Annotated(filter),
	})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	if peek.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		account, err := s.gw.ChatStream(r.Context(), raw, w)
		s.record(account, peek.Model, "openai", true, http.StatusOK, err, "")
		if err != nil {
			writeJSON(w, statusOf(err), errJSON(err))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	resp, account, err := s.gw.Chat(r.Context(), raw)
	if err != nil {
		s.record(account, peek.Model, "openai", false, statusOf(err), err, "")
		writeJSON(w, statusOf(err), errJSON(err))
		return
	}
	s.record(account, peek.Model, "openai", false, http.StatusOK, nil, resp.Content)
	if len(resp.Raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Raw)
		return
	}
	writeJSON(w, http.StatusOK, completionJSON(peek.Model, resp.Content))
}

func (s *Server) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	out, account, err := s.gw.ClaudeChat(r.Context(), raw)
	if err != nil {
		s.record(account, peek.Model, "claude", peek.Stream, statusOf(err), err, "")
		writeJSON(w, statusOf(err), errJSON(err))
		return
	}
	s.record(account, peek.Model, "claude", peek.Stream, http.StatusOK, nil, "")
	if peek.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = io.WriteString(w, "event: message\ndata: "+string(out)+"\n\n")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = r
	cfg := s.gw.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"bind":     cfg.Bind,
		"port":     cfg.Port,
		"phase":    "v0.1",
		"oauth":    "not implemented",
		"config":   s.gw.ConfigPath(),
		"models":   len(s.gw.Models()),
		"adapters": []string{"ollama", "openai_compat", "opencode_zen", "anthropic_oauth", "openai_oauth"},
	})
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	_ = r
	type row struct {
		ID      string `json:"id"`
		Adapter string `json:"adapter"`
		Tier    string `json:"tier"`
		BaseURL string `json:"baseURL"`
		HasKey  bool   `json:"hasKey"`
		Status  string `json:"status"`
	}
	out := []row{}
	for _, p := range s.gw.Instances() {
		status := "configured"
		switch p.Adapter {
		case "anthropic_oauth", "openai_oauth":
			status = "oauth-stub"
		}
		out = append(out, row{
			ID: p.ID, Adapter: p.Adapter, Tier: p.Tier, BaseURL: p.BaseURL,
			HasKey: p.APIKey != "", Status: status,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var p config.Provider
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if p.ID == "" || p.Adapter == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id and adapter are required"})
		return
	}
	if err := s.gw.AddProvider(r.Context(), p); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "id": p.ID})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.gw.RemoveProvider(r.Context(), id); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminCatalog(w http.ResponseWriter, r *http.Request) {
	filter := catalog.Filter(r.URL.Query().Get("filter"))
	writeJSON(w, http.StatusOK, map[string]any{
		"filter": filter,
		"models": s.gw.Annotated(filter),
		"note":   "hide affects listing only; models stay routable by id",
	})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.gw.Refresh(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": len(s.gw.Models())})
}

func (s *Server) handleHide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		Hidden bool   `json:"hidden"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if err := s.gw.ToggleHide(body.Kind, body.ID, body.Hidden); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleShowcase(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if body.Prompt == "" {
		body.Prompt = "Say hello in one short sentence."
	}
	req := struct {
		Model    string            `json:"model"`
		Messages []adapter.Message `json:"messages"`
		Stream   bool              `json:"stream"`
	}{
		Model:    body.Model,
		Messages: []adapter.Message{{Role: "user", Content: body.Prompt}},
		Stream:   false,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errJSON(err))
		return
	}
	resp, account, err := s.gw.Chat(r.Context(), raw)
	if err != nil {
		s.record(account, body.Model, "showcase", false, statusOf(err), err, "")
		writeJSON(w, statusOf(err), errJSON(err))
		return
	}
	s.record(account, body.Model, "showcase", false, http.StatusOK, nil, resp.Content)
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  account,
		"model":    body.Model,
		"content":  resp.Content,
		"raw":      json.RawMessage(resp.Raw),
		"redacted": true,
	})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeJSON(w, http.StatusOK, map[string]any{
		"recent":     s.gw.Usage.Recent(),
		"byAccount":  s.gw.Usage.ByAccount(),
		"disclaimer": "In-memory only; not persisted. Built-in usage is a PeaProxy win vs CPA v6.10+.",
	})
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	_ = r
	type item struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseURL"`
		Notes   string `json:"notes"`
		Snippet string `json:"snippet"`
	}
	var out []item
	for _, n := range clients.List() {
		p, _ := clients.Get(n)
		out = append(out, item{Name: p.Name, BaseURL: p.BaseURL, Notes: p.Notes, Snippet: p.Snippet})
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": out})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	_ = r
	cfg := s.gw.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion":    cfg.SchemaVersion,
		"bind":             cfg.Bind,
		"port":             cfg.Port,
		"allowNonLoopback": cfg.AllowNonLoopback,
		"hide":             cfg.Hide,
		"expose":           cfg.Expose,
		"configPath":       s.gw.ConfigPath(),
		"listingOnlyHide":  !cfg.Hide.BlockRouting,
	})
}

func (s *Server) record(account, model, proto string, stream bool, status int, err error, preview string) {
	if s.gw == nil || s.gw.Usage == nil {
		return
	}
	e := usage.Event{AccountID: account, Model: model, Protocol: proto, Stream: stream, Status: status, Preview: preview}
	if err != nil {
		e.Error = err.Error()
		if e.Status == 0 {
			e.Status = statusOf(err)
		}
	}
	s.gw.Usage.Add(e)
}

func readBody(r *http.Request) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxBody {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxBody)
	}
	return raw, nil
}

func statusOf(err error) int {
	if errors.Is(err, router.ErrNoAccount) {
		return http.StatusNotFound
	}
	var he adapter.HTTPError
	if errors.As(err, &he) && he.Status >= 400 {
		return he.Status
	}
	return http.StatusBadGateway
}

func errJSON(err error) map[string]string {
	return map[string]string{"error": err.Error()}
}

func completionJSON(model, content string) map[string]any {
	return map[string]any{
		"id":      "peaproxy",
		"object":  "chat.completion",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
