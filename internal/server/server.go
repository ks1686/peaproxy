package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/ui"
)

// Server is the localhost gateway (OpenAI + Claude + admin + UI).
type Server struct {
	cfg    config.Config
	reg    *adapter.Registry
	rt     *router.Router
	http   *http.Server
	models []catalog.Model
}

// Options wires dependencies for tests.
type Options struct {
	Config   config.Config
	Registry *adapter.Registry
	Router   *router.Router
	Models   []catalog.Model
}

// New builds an HTTP server that is not yet listening.
func New(opts Options) *Server {
	s := &Server{
		cfg:    opts.Config,
		reg:    opts.Registry,
		rt:     opts.Router,
		models: opts.Models,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/messages", s.handleClaudeMessages)
	mux.HandleFunc("GET /admin/health", s.handleHealth)
	mux.HandleFunc("GET /admin/accounts", s.handleAccounts)
	mux.HandleFunc("GET /admin/catalog", s.handleCatalog)
	mux.HandleFunc("GET /admin/settings", s.handleSettings)
	uiFS, err := fs.Sub(ui.FS, "web")
	if err != nil {
		log.Printf("ui embed: %v", err)
		uiFS = ui.FS
	}
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(uiFS))))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	s.http = &http.Server{
		Addr:              s.cfg.Addr(),
		Handler:           withLoopbackLog(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Handler exposes the mux for tests.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// ListenAndServe binds cfg.Addr (default 127.0.0.1:8317).
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.Addr())
	if err != nil {
		return err
	}
	log.Printf("peaproxy listening on http://%s (UI /  OpenAI /v1  Claude /v1/messages)", ln.Addr())
	s.http.Addr = ln.Addr().String()
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
	q := catalog.Query{
		Filter:        filter,
		HideProviders: s.cfg.Hide.Providers,
		HideModels:    s.cfg.Hide.Models,
		ExposeModels:  s.cfg.Expose.Models,
		ForClients:    true,
	}
	list := catalog.ToOpenAIList(catalog.Apply(s.models, q))
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if s.rt == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{
			"error": "router not configured (spike TODO: wire adapters)",
		})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var parsed struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &parsed)
	resp, err := s.rt.Chat(r.Context(), adapter.ChatRequest{Model: parsed.Model, Raw: body})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, router.ErrNoAccount) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if len(resp.Raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Raw)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "peaproxy-stub",
		"object":  "chat.completion",
		"model":   parsed.Model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": resp.Content}}},
	})
}

func (s *Server) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "Claude /v1/messages is stubbed (spike TODO)",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"bind":     s.cfg.Bind,
		"port":     s.cfg.Port,
		"phase":    "scaffold",
		"oauth":    "not implemented",
		"adapters": []string{"ollama", "openai_compat", "anthropic_oauth", "openai_oauth"},
	})
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	_ = r
	type row struct {
		ID      string `json:"id"`
		Adapter string `json:"adapter"`
		Tier    string `json:"tier"`
		Status  string `json:"status"`
	}
	out := make([]row, 0, len(s.cfg.Providers))
	for _, p := range s.cfg.Providers {
		status := "configured"
		switch p.Adapter {
		case "anthropic_oauth", "openai_oauth":
			status = "oauth-stub"
		}
		out = append(out, row{ID: p.ID, Adapter: p.Adapter, Tier: p.Tier, Status: status})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	filter := catalog.Filter(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = catalog.FilterAll
	}
	q := catalog.Query{
		Filter:        filter,
		HideProviders: s.cfg.Hide.Providers,
		HideModels:    s.cfg.Hide.Models,
		ExposeModels:  s.cfg.Expose.Models,
		ForClients:    r.URL.Query().Get("clients") == "1",
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"filter": filter,
		"models": catalog.Apply(s.models, q),
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion":    s.cfg.SchemaVersion,
		"bind":             s.cfg.Bind,
		"port":             s.cfg.Port,
		"allowNonLoopback": s.cfg.AllowNonLoopback,
		"hide":             s.cfg.Hide,
		"expose":           s.cfg.Expose,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func withLoopbackLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin") && r.URL.Path != "/healthz" && !strings.HasPrefix(r.URL.Path, "/ui") && r.URL.Path != "/" {
			// Public API paths stay quiet unless debug is added later.
		}
		next.ServeHTTP(w, r)
	})
}
