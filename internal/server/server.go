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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/clients"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/ui"
	"github.com/ks1686/peaproxy/internal/usage"
	"github.com/ks1686/peaproxy/internal/version"
)

const maxBody = 8 << 20

// Server is the localhost gateway (OpenAI + Claude + admin + UI).
type Server struct {
	gw        *gateway.Gateway
	http      *http.Server
	oauthMu   sync.Mutex
	oauthJobs map[string]*oauthJob
}

type oauthJob struct {
	LoginURL string
	UserCode string
	Done     bool
	Err      string
}

// Options wires dependencies for tests.
type Options struct {
	Gateway *gateway.Gateway
	Config  config.Config
	Models  []catalog.Model
}

// New builds an HTTP server that is not yet listening.
func New(opts Options) *Server {
	s := &Server{gw: opts.Gateway, oauthJobs: map[string]*oauthJob{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	mux.HandleFunc("GET /v0/catalog", s.handleCatalogAPI)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/messages", s.handleClaudeMessages)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	admin := func(h http.HandlerFunc) http.HandlerFunc { return s.requireAdmin(h) }
	mux.HandleFunc("GET /admin/health", admin(s.handleHealth))
	mux.HandleFunc("GET /admin/presets", admin(s.handlePresets))
	mux.HandleFunc("GET /admin/accounts", admin(s.handleAccounts))
	mux.HandleFunc("POST /admin/accounts", admin(s.handleAddAccount))
	mux.HandleFunc("DELETE /admin/accounts/{id}", admin(s.handleDeleteAccount))
	mux.HandleFunc("GET /admin/catalog", admin(s.handleAdminCatalog))
	mux.HandleFunc("POST /admin/catalog/refresh", admin(s.handleRefresh))
	mux.HandleFunc("POST /admin/hide", admin(s.handleHide))
	mux.HandleFunc("POST /admin/showcase", admin(s.handleShowcase))
	mux.HandleFunc("GET /admin/usage", admin(s.handleUsage))
	mux.HandleFunc("GET /admin/clients", admin(s.handleClients))
	mux.HandleFunc("POST /admin/oauth/start", admin(s.handleOAuthStart))
	mux.HandleFunc("GET /admin/oauth/status", admin(s.handleOAuthStatus))
	mux.HandleFunc("GET /admin/settings", admin(s.handleSettings))
	mux.HandleFunc("POST /admin/settings", admin(s.handleSettingsPost))
	uiFS, err := fs.Sub(ui.FS, "web")
	if err != nil {
		log.Printf("ui embed: %v", err)
		uiFS = ui.FS
	}
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(uiFS))))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
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
		sw := &sseWriter{ResponseWriter: w}
		account, err := s.gw.ChatStream(r.Context(), raw, sw)
		s.record(account, peek.Model, "openai", true, http.StatusOK, err, "")
		if err != nil && !sw.started {
			writeErr(w, err)
		}
		return
	}
	resp, account, err := s.gw.Chat(r.Context(), raw)
	if err != nil {
		s.record(account, peek.Model, "openai", false, statusOf(err), err, "")
		writeErr(w, err)
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

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	if peek.Stream {
		sw := &sseWriter{ResponseWriter: w}
		account, err := s.gw.ResponsesStream(r.Context(), raw, sw)
		s.record(account, peek.Model, "responses", true, http.StatusOK, err, "")
		if err != nil && !sw.started {
			writeErr(w, err)
		}
		return
	}
	out, account, err := s.gw.Responses(r.Context(), raw)
	if err != nil {
		s.record(account, peek.Model, "responses", false, statusOf(err), err, "")
		writeErr(w, err)
		return
	}
	s.record(account, peek.Model, "responses", false, http.StatusOK, nil, "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func (s *Server) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	raw, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	if peek.Stream {
		sw := &sseWriter{ResponseWriter: w}
		account, err := s.gw.ClaudeChatStream(r.Context(), raw, sw)
		s.record(account, peek.Model, "claude", true, http.StatusOK, err, "")
		if err != nil && !sw.started {
			writeErr(w, err)
		}
		return
	}
	out, account, err := s.gw.ClaudeChat(r.Context(), raw)
	if err != nil {
		s.record(account, peek.Model, "claude", false, statusOf(err), err, "")
		writeErr(w, err)
		return
	}
	s.record(account, peek.Model, "claude", false, http.StatusOK, nil, "")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_ = r
	cfg := s.gw.Config()
	usagePath := ""
	if s.gw.Usage != nil {
		usagePath = s.gw.Usage.Path()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"version":            version.Version,
		"bind":               cfg.Bind,
		"port":               cfg.Port,
		"oauth":              "subscription OAuth (ToS risk)",
		"config":             s.gw.ConfigPath(),
		"usageFile":          usagePath,
		"requestLog":         cfg.RequestLog,
		"models":             len(s.gw.Models()),
		"adapters":           adapters.Names(),
		"cooldowns":          s.gw.Cooldowns(),
		"allowNonLoopback":   cfg.AllowNonLoopback,
		"lan":                cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind),
		"adminTokenRequired": s.adminRequired(),
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	_ = r
	out := map[string]any{"status": "ok", "version": version.Version}
	if s.gw != nil {
		cfg := s.gw.Config()
		lan := cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind)
		out["bind"] = cfg.Bind
		out["port"] = cfg.Port
		out["lan"] = lan
		out["adminTokenRequired"] = s.adminRequired()
		out["lanWarning"] = lan
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeJSON(w, http.StatusOK, map[string]any{"presets": adapters.AccountPresets()})
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
		if adapters.IsOAuthAdapter(p.Adapter) {
			if p.HasOAuth() || p.APIKey == "configured" {
				status = "oauth-ready"
			} else {
				status = "oauth-login-required"
			}
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

func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID   string `json:"id"`
		Flow string `json:"flow"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if body.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	adp, ok := s.gw.AdapterByID(body.ID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown account; add the OAuth preset first"})
		return
	}
	auth, ok := adp.(adapter.Authenticator)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "account does not support OAuth"})
		return
	}
	sess, err := auth.AuthStart(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	job := &oauthJob{LoginURL: sess.LoginURL, UserCode: sess.UserCode}
	s.oauthMu.Lock()
	s.oauthJobs[body.ID] = job
	s.oauthMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		err := auth.AuthComplete(ctx, sess, "")
		s.oauthMu.Lock()
		defer s.oauthMu.Unlock()
		job.Done = true
		if err != nil {
			job.Err = err.Error()
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "pending",
		"id":       body.ID,
		"loginURL": sess.LoginURL,
		"userCode": sess.UserCode,
		"warning":  oauth.LiabilityWarning(),
		"cli":      "peaproxy auth login --provider " + s.cliProviderFor(body.ID),
	})
}

func (s *Server) handleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	s.oauthMu.Lock()
	job := s.oauthJobs[id]
	s.oauthMu.Unlock()
	if job == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle", "id": id})
		return
	}
	status := "pending"
	if job.Done && job.Err == "" {
		status = "complete"
	} else if job.Done {
		status = "error"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   status,
		"id":       id,
		"loginURL": job.LoginURL,
		"userCode": job.UserCode,
		"error":    job.Err,
		"warning":  oauth.LiabilityWarning(),
	})
}

func (s *Server) cliProviderFor(id string) string {
	for _, p := range s.gw.Instances() {
		if p.ID == id {
			return adapters.CLIProvider(p.Adapter)
		}
	}
	return adapters.CLIProvider(id)
}

func (s *Server) handleAdminCatalog(w http.ResponseWriter, r *http.Request) {
	filter := catalog.Filter(r.URL.Query().Get("filter"))
	writeJSON(w, http.StatusOK, map[string]any{
		"filter":          filter,
		"models":          s.gw.Annotated(filter),
		"note":            "hide affects listing only; models stay routable by id",
		"imageGeneration": "not_yet",
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
		Model         string `json:"model"`
		Prompt        string `json:"prompt"`
		ImageURL      string `json:"imageUrl"`
		GenerateImage bool   `json:"generateImage"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if body.GenerateImage {
		writeJSON(w, http.StatusNotImplemented, map[string]any{
			"error":    "image generation not yet",
			"notYet":   true,
			"imageOut": false,
		})
		return
	}
	if body.Prompt == "" {
		body.Prompt = "Say hello in one short sentence."
	}
	raw, err := showcaseBody(body.Model, body.Prompt, body.ImageURL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errJSON(err))
		return
	}
	resp, account, err := s.gw.Chat(r.Context(), raw)
	if err != nil {
		s.record(account, body.Model, "showcase", false, statusOf(err), err, "")
		writeErr(w, err)
		return
	}
	s.record(account, body.Model, "showcase", false, http.StatusOK, nil, resp.Content)
	writeJSON(w, http.StatusOK, map[string]any{
		"account":  account,
		"model":    body.Model,
		"content":  resp.Content,
		"raw":      json.RawMessage(resp.Raw),
		"redacted": true,
		"vision":   body.ImageURL != "",
	})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	_ = r
	path := ""
	if s.gw.Usage != nil {
		path = s.gw.Usage.Path()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recent":     s.gw.Usage.Recent(),
		"byAccount":  s.gw.Usage.ByAccount(),
		"path":       path,
		"disclaimer": "Persisted to usage.json next to the config file. Opt-in requestLog writes redacted JSONL to requests.log.",
	})
}

func (s *Server) handleClients(w http.ResponseWriter, r *http.Request) {
	_ = r
	type item struct {
		Name    string `json:"name"`
		BaseURL string `json:"baseURL"`
		Cloak   string `json:"cloak"`
		Notes   string `json:"notes"`
		Snippet string `json:"snippet"`
	}
	var out []item
	for _, n := range clients.List() {
		p, _ := clients.Get(n)
		out = append(out, item{Name: p.Name, BaseURL: p.BaseURL, Cloak: p.Cloak, Notes: p.Notes, Snippet: p.Snippet})
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
		"requestLog":       cfg.RequestLog,
		"lan":              cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind),
		"lanWarning":       cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind),
	})
}

func (s *Server) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestLog *bool `json:"requestLog"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if body.RequestLog != nil {
		if err := s.gw.SetRequestLog(*body.RequestLog); err != nil {
			writeJSON(w, http.StatusBadRequest, errJSON(err))
			return
		}
	}
	s.handleSettings(w, r)
}

func (s *Server) adminRequired() bool {
	if s.gw == nil {
		return false
	}
	cfg := s.gw.Config()
	return cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind)
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.adminRequired() {
			next(w, r)
			return
		}
		token := s.gw.Config().AdminToken
		got := r.Header.Get("X-Admin-Token")
		if got == "" {
			if a := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(a), "bearer ") {
				got = strings.TrimSpace(a[7:])
			}
		}
		if token == "" || got != token {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "admin token required (X-Admin-Token)"})
			return
		}
		next(w, r)
	}
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
	var ce router.CooldownError
	if errors.As(err, &ce) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, router.ErrNoAccount) {
		return http.StatusNotFound
	}
	var he adapter.HTTPError
	if errors.As(err, &he) && he.Status >= 400 {
		return he.Status
	}
	return http.StatusBadGateway
}

func writeErr(w http.ResponseWriter, err error) {
	if sec := router.RetryAfterSeconds(err); sec > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(sec))
	}
	writeJSON(w, statusOf(err), errJSON(err))
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

type sseWriter struct {
	http.ResponseWriter
	started bool
}

func (s *sseWriter) Write(p []byte) (int, error) {
	if !s.started {
		s.Header().Set("Content-Type", "text/event-stream")
		s.Header().Set("Cache-Control", "no-cache")
		s.started = true
	}
	n, err := s.ResponseWriter.Write(p)
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return n, err
}

type showcasePart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

func showcaseBody(model, prompt, imageURL string) ([]byte, error) {
	type msg struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	type req struct {
		Model    string `json:"model"`
		Messages []msg  `json:"messages"`
		Stream   bool   `json:"stream"`
	}
	content, err := json.Marshal(prompt)
	if err != nil {
		return nil, err
	}
	if imageURL != "" {
		parts := []showcasePart{
			{Type: "text", Text: prompt},
			{Type: "image_url", ImageURL: &struct {
				URL string `json:"url"`
			}{URL: imageURL}},
		}
		content, err = json.Marshal(parts)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(req{
		Model:    model,
		Messages: []msg{{Role: "user", Content: content}},
		Stream:   false,
	})
}
