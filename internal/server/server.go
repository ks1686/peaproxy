package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/hosted"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/clients"
	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/gateway"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/quota"
	"github.com/ks1686/peaproxy/internal/requestmeta"
	"github.com/ks1686/peaproxy/internal/router"
	"github.com/ks1686/peaproxy/internal/ui"
	"github.com/ks1686/peaproxy/internal/usage"
	"github.com/ks1686/peaproxy/internal/version"
)

const maxBody = 8 << 20

// Server is the localhost gateway (OpenAI + Claude + admin + UI).
type Server struct {
	gw         *gateway.Gateway
	http       *http.Server
	oauthMu    sync.Mutex
	oauthJobs  map[string]*oauthJob
	clientRoot string
	listenAddr atomic.Value
}

type oauthJob struct {
	LoginURL string
	UserCode string
	Done     bool
	Err      string
}

// Options wires dependencies for tests.
type Options struct {
	Gateway    *gateway.Gateway
	Config     config.Config
	Models     []catalog.Model
	ClientRoot string
}

// New builds an HTTP server that is not yet listening.
func New(opts Options) *Server {
	s := &Server{gw: opts.Gateway, oauthJobs: map[string]*oauthJob{}, clientRoot: opts.ClientRoot}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.handleOpenAIModels)
	mux.HandleFunc("GET /v0/catalog", s.handleCatalogAPI)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("POST /v1/messages", s.handleClaudeMessages)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/images/generations", s.handleImageGenerations)
	mux.HandleFunc("POST /v1/images/edits", s.handleImageEdits)
	mux.HandleFunc("POST /v1/embeddings", s.handleEmbeddings)
	admin := func(h http.HandlerFunc) http.HandlerFunc { return s.requireAdmin(h) }
	mux.HandleFunc("GET /admin/health", admin(s.handleHealth))
	mux.HandleFunc("GET /admin/quota", admin(s.handleQuota))
	mux.HandleFunc("GET /admin/presets", admin(s.handlePresets))
	mux.HandleFunc("GET /admin/accounts", admin(s.handleAccounts))
	mux.HandleFunc("POST /admin/accounts", admin(s.handleAddAccount))
	mux.HandleFunc("DELETE /admin/accounts/{id}", admin(s.handleDeleteAccount))
	mux.HandleFunc("GET /admin/catalog", admin(s.handleAdminCatalog))
	mux.HandleFunc("POST /admin/catalog/refresh", admin(s.handleRefresh))
	mux.HandleFunc("POST /admin/catalog/overlay", admin(s.handleCatalogOverlay))
	mux.HandleFunc("POST /admin/hide", admin(s.handleHide))
	mux.HandleFunc("POST /admin/showcase", admin(s.handleShowcase))
	mux.HandleFunc("GET /admin/usage", admin(s.handleUsage))
	mux.HandleFunc("GET /admin/requests", admin(s.handleRequests))
	mux.HandleFunc("GET /admin/clients", admin(s.handleClients))
	mux.HandleFunc("POST /admin/clients/{name}/connect", admin(s.handleClientConnect))
	mux.HandleFunc("POST /admin/clients/{name}/disconnect", admin(s.handleClientDisconnect))
	mux.HandleFunc("POST /admin/clients/{name}/verify", admin(s.handleClientVerify))
	mux.HandleFunc("GET /admin/engine", admin(s.handleEngine))
	mux.HandleFunc("POST /admin/oauth/start", admin(s.handleOAuthStart))
	mux.HandleFunc("GET /admin/oauth/status", admin(s.handleOAuthStatus))
	mux.HandleFunc("GET /admin/settings", admin(s.handleSettings))
	mux.HandleFunc("POST /admin/settings", admin(s.handleSettingsPost))
	mux.HandleFunc("POST /admin/health/probe", admin(s.handleHealthProbe))
	uiFS, err := fs.Sub(ui.FS, "web")
	if err != nil {
		log.Printf("ui embed: %v", err)
		uiFS = ui.FS
	}
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(uiFS))))
	mux.HandleFunc("GET /favicon.ico", s.handleFavicon)
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
		Handler:           s.guard(mux),
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
	s.setListenAddr(ln.Addr().String())
	log.Printf("peaproxy listening on http://%s", ln.Addr())
	return s.http.Serve(ln)
}

func (s *Server) setListenAddr(addr string) { s.listenAddr.Store(addr) }

// Shutdown stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		peerLoopback := remoteLoopback(r.RemoteAddr)
		if peerLoopback && !config.IsLoopback(hostOnly(r.Host)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "loopback host required"})
			return
		}
		if !originAllowed(r, peerLoopback) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request rejected"})
			return
		}
		if peerLoopback && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			if !mutationContentType(r) {
				writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "content-type must be application/json"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func remoteLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	return config.IsLoopback(host)
}

func hostOnly(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return host
}

func originAllowed(r *http.Request, peerLoopback bool) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if peerLoopback {
		return config.IsLoopback(hostOnly(parsed.Host))
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func mutationContentType(r *http.Request) bool {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "application/json") {
		return true
	}
	return r.URL.Path == "/v1/images/edits" && strings.HasPrefix(ct, "multipart/form-data")
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	data, err := ui.FS.ReadFile("web/favicon.png")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
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
	started := time.Now()
	raw, err := readJSONBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	if peek.Stream {
		sw := &sseWriter{ResponseWriter: w}
		account, err := s.gw.ChatStream(requestCtx(r, raw), raw, sw)
		s.record(account, peek.Model, "openai", "/v1/chat/completions", true, http.StatusOK, err, inspectorPreview(raw, ""), started, sw.usage)
		if err != nil && !sw.started {
			writeErr(w, wireOpenAI, err)
		}
		return
	}
	resp, account, err := s.gw.Chat(requestCtx(r, raw), raw)
	if err != nil {
		s.record(account, peek.Model, "openai", "/v1/chat/completions", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
		writeErr(w, wireOpenAI, err)
		return
	}
	s.recordCall(account, peek.Model, "openai", "/v1/chat/completions", false, http.StatusOK, nil, inspectorPreview(raw, resp.Content), started, resp.Raw, resp.CacheHit)
	if len(resp.Raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Raw)
		return
	}
	writeJSON(w, http.StatusOK, completionJSON(peek.Model, resp.Content))
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	raw, err := readJSONBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	if peek.Stream {
		sw := &sseWriter{ResponseWriter: w}
		account, err := s.gw.ResponsesStream(requestCtx(r, raw), raw, sw)
		s.record(account, peek.Model, "responses", "/v1/responses", true, http.StatusOK, err, inspectorPreview(raw, ""), started, sw.usage)
		if err != nil && !sw.started {
			writeErr(w, wireOpenAI, err)
		}
		return
	}
	out, account, err := s.gw.Responses(requestCtx(r, raw), raw)
	if err != nil {
		s.record(account, peek.Model, "responses", "/v1/responses", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
		writeErr(w, wireOpenAI, err)
		return
	}
	s.record(account, peek.Model, "responses", "/v1/responses", false, http.StatusOK, nil, inspectorPreview(raw, ""), started, out)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func (s *Server) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	raw, err := readJSONBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	resp, account, err := s.gw.GenerateImage(requestCtx(r, raw), raw)
	if err != nil {
		s.record(account, peek.Model, "images", "/v1/images/generations", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
		writeErr(w, wireOpenAI, err)
		return
	}
	s.record(account, peek.Model, "images", "/v1/images/generations", false, http.StatusOK, nil, inspectorPreview(raw, imagePreview(resp)), started, resp.Raw)
	if len(resp.Raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Raw)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": resp.Created,
		"data":    imageDataJSON(resp),
	})
}

func (s *Server) handleImageEdits(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	raw, err := readBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "multipart/") {
		if err := validateJSONObject(raw); err != nil {
			writeJSON(w, http.StatusBadRequest, errJSON(err))
			return
		}
	}
	model := gateway.ImageEditModel(raw, ct)
	resp, account, err := s.gw.EditImage(requestCtx(r, raw), raw, ct)
	if err != nil {
		s.record(account, model, "images", "/v1/images/edits", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
		writeErr(w, wireOpenAI, err)
		return
	}
	s.record(account, model, "images", "/v1/images/edits", false, http.StatusOK, nil, inspectorPreview(raw, imagePreview(resp)), started, resp.Raw)
	if len(resp.Raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Raw)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": resp.Created,
		"data":    imageDataJSON(resp),
	})
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	raw, err := readJSONBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	resp, account, err := s.gw.CreateEmbeddings(requestCtx(r, raw), raw)
	if err != nil {
		s.record(account, peek.Model, "embeddings", "/v1/embeddings", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
		writeErr(w, wireOpenAI, err)
		return
	}
	s.recordCall(account, peek.Model, "embeddings", "/v1/embeddings", false, http.StatusOK, nil, inspectorPreview(raw, embeddingPreview(resp)), started, resp.Raw, resp.CacheHit)
	if len(resp.Raw) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resp.Raw)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"model":  resp.Model,
		"data":   []any{},
	})
}

func (s *Server) handleClaudeMessages(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	raw, err := readJSONBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	peek := jsonx.PeekBody(raw)
	if peek.Stream {
		sw := &sseWriter{ResponseWriter: w}
		account, err := s.gw.ClaudeChatStream(requestCtx(r, raw), raw, sw)
		s.record(account, peek.Model, "claude", "/v1/messages", true, http.StatusOK, err, inspectorPreview(raw, ""), started, sw.usage)
		if err != nil && !sw.started {
			writeErr(w, wireAnthropic, err)
		}
		return
	}
	out, account, err := s.gw.ClaudeChat(requestCtx(r, raw), raw)
	if err != nil {
		s.record(account, peek.Model, "claude", "/v1/messages", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
		writeErr(w, wireAnthropic, err)
		return
	}
	s.record(account, peek.Model, "claude", "/v1/messages", false, http.StatusOK, nil, inspectorPreview(raw, ""), started, out)
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
		"failoverPolicy":     cfg.FailoverPolicy(),
		"adapterHealth":      s.gw.AdapterHealth(),
		"quota":              s.gw.Quota(),
		"cooldowns":          s.gw.Cooldowns(),
		"cooldownTtlMs":      gateway.CooldownTTL.Milliseconds(),
		"allowNonLoopback":   cfg.AllowNonLoopback,
		"lan":                cfg.AllowNonLoopback && !config.IsLoopback(cfg.Bind),
		"adminTokenRequired": s.adminRequired(),
	})
}

func (s *Server) handleQuota(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeJSON(w, http.StatusOK, map[string]any{
		"quota":    s.gw.Quota(),
		"families": quota.Families(),
		"honesty":  "unknown remaining is omitted (null). 0 is only shown when the provider returned 0. unlimited is only shown when the provider documented null remaining as unlimited (OpenRouter GET /key).",
	})
}

func (s *Server) handleHealthProbe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"adapterHealth": s.gw.Probe(ctx),
		"quota":         s.gw.Quota(),
		"cooldowns":     s.gw.Cooldowns(),
		"cooldownTtlMs": gateway.CooldownTTL.Milliseconds(),
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
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out, "onboarding": s.gw.Config().NeedsOnboarding()})
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
	if spec, ok := hosted.Lookup(p.Adapter); ok {
		p.BaseURL = spec.FillBaseURL(p.BaseURL)
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
		err := auth.AuthComplete(ctx, sess, "")
		cancel()
		s.oauthMu.Lock()
		job.Done = true
		if err != nil {
			job.Err = err.Error()
		}
		s.oauthMu.Unlock()
		if err == nil {
			// Unbounded like the startup refresh: Refresh lists accounts in
			// series, so a shared deadline lets one hung upstream drop the rest.
			s.gw.Refresh(context.Background())
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
	var job oauthJob
	live := s.oauthJobs[id]
	if live != nil {
		job = *live
	}
	s.oauthMu.Unlock()
	if live == nil {
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
		"imageGeneration": "proxy",
		"embeddings":      "proxy",
	})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	s.gw.Refresh(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "models": len(s.gw.Models())})
}

func (s *Server) handleCatalogOverlay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string  `json:"id"`
		DisplayName *string `json:"displayName"`
		Pinned      *bool   `json:"pinned"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if err := s.gw.SetCatalogOverlay(body.ID, body.DisplayName, body.Pinned); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"id":      body.ID,
		"catalog": s.gw.Config().Catalog,
	})
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
	started := time.Now()
	var body struct {
		Model            string `json:"model"`
		Prompt           string `json:"prompt"`
		ImageURL         string `json:"imageUrl"`
		GenerateImage    bool   `json:"generateImage"`
		CreateEmbeddings bool   `json:"createEmbeddings"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errJSON(err))
		return
	}
	if body.GenerateImage {
		if body.Prompt == "" {
			body.Prompt = "a simple icon"
		}
		raw, err := json.Marshal(struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}{Model: body.Model, Prompt: body.Prompt})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errJSON(err))
			return
		}
		resp, account, err := s.gw.GenerateImage(requestCtx(r, raw), raw)
		if err != nil {
			s.record(account, body.Model, "showcase", "/admin/showcase", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
			writeErr(w, wireAdmin, err)
			return
		}
		s.record(account, body.Model, "showcase", "/admin/showcase", false, http.StatusOK, nil, inspectorPreview(raw, imagePreview(resp)), started, resp.Raw)
		writeJSON(w, http.StatusOK, map[string]any{
			"account":  account,
			"model":    body.Model,
			"urls":     resp.URLs,
			"b64":      resp.B64,
			"raw":      json.RawMessage(resp.Raw),
			"redacted": true,
			"imageOut": true,
		})
		return
	}
	if body.CreateEmbeddings {
		if body.Prompt == "" {
			body.Prompt = "hello world"
		}
		raw, err := json.Marshal(struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}{Model: body.Model, Input: body.Prompt})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errJSON(err))
			return
		}
		resp, account, err := s.gw.CreateEmbeddings(requestCtx(r, raw), raw)
		if err != nil {
			s.record(account, body.Model, "showcase", "/admin/showcase", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
			writeErr(w, wireAdmin, err)
			return
		}
		s.recordCall(account, body.Model, "showcase", "/admin/showcase", false, http.StatusOK, nil, inspectorPreview(raw, embeddingPreview(resp)), started, resp.Raw, resp.CacheHit)
		writeJSON(w, http.StatusOK, map[string]any{
			"account":    account,
			"model":      body.Model,
			"count":      resp.Count,
			"dimensions": resp.Dimensions,
			"raw":        json.RawMessage(resp.Raw),
			"redacted":   true,
			"embeddings": true,
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
	resp, account, err := s.gw.Chat(requestCtx(r, raw), raw)
	if err != nil {
		s.record(account, body.Model, "showcase", "/admin/showcase", false, statusOf(err), err, inspectorPreview(raw, ""), started, nil)
		writeErr(w, wireAdmin, err)
		return
	}
	s.recordCall(account, body.Model, "showcase", "/admin/showcase", false, http.StatusOK, nil, inspectorPreview(raw, resp.Content), started, resp.Raw, resp.CacheHit)
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
		"byProvider": s.gw.Usage.ByProvider(),
		"path":       path,
		"byDay":      s.gw.Usage.ByDay(),
		"disclaimer": "usage.json keeps a 200-event ring and 90 UTC days of per-account totals. Tokens and costUSD count only calls whose provider published them. Subscription quota is omitted unless the upstream sent it.",
	})
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	_ = r
	cfg := s.gw.Config()
	path := ""
	events := []usage.Event{}
	if s.gw.Usage != nil {
		path = s.gw.Usage.RequestLogPath()
		if cfg.RequestLog {
			events = s.gw.Usage.Tail(100)
		}
	}
	if events == nil {
		events = []usage.Event{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     cfg.RequestLog,
		"path":        path,
		"events":      events,
		"disclaimer":  "Opt-in. Secrets are redacted. File mode 0600. Rotated when the log exceeds 1MiB.",
		"neverLogged": []string{"Authorization", "x-api-key", "access_token", "refresh_token", "sk-*", "PEM private keys"},
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
		Verify  string `json:"verify"`
		// Connectable is what Layout can actually write. The web UI renders its
		// Connect button from this rather than from a list of names of its own,
		// which is how Pi went missing from it.
		Connectable bool `json:"connectable"`
	}
	origin := s.clientOrigin()
	var out []item
	for _, n := range clients.List() {
		p, _ := clients.Get(n)
		p = clients.WithOrigin(p, origin)
		out = append(out, item{
			Name:        p.Name,
			BaseURL:     p.BaseURL,
			Cloak:       p.Cloak,
			Notes:       p.Notes,
			Snippet:     p.Snippet,
			Verify:      p.Verify,
			Connectable: clients.Connectable(n),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": out})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	_ = r
	writeJSON(w, http.StatusOK, s.settingsPayload())
}

func (s *Server) settingsPayload() map[string]any {
	cfg := s.gw.Config()
	loopback := config.IsLoopback(cfg.Bind)
	lan := cfg.AllowNonLoopback && !loopback
	reqPath := ""
	usagePath := ""
	if s.gw.Usage != nil {
		reqPath = s.gw.Usage.RequestLogPath()
		usagePath = s.gw.Usage.Path()
	}
	backend := "file"
	backendNote := "AES-GCM file next to the config (secrets.enc). Tokens are never shown here."
	if path := s.gw.ConfigPath(); path != "" {
		if store, err := config.OpenStore(path); err == nil && store != nil {
			backend = string(store.Backend())
		}
	}
	if backend == "keyring" {
		backendNote = "OS keychain (macOS Keychain / Windows Credential Manager / Linux Secret Service). Tokens are never shown here."
	}
	return map[string]any{
		"schemaVersion":      cfg.SchemaVersion,
		"bind":               cfg.Bind,
		"port":               cfg.Port,
		"addr":               cfg.Addr(),
		"allowNonLoopback":   cfg.AllowNonLoopback,
		"loopback":           loopback,
		"hasAdminToken":      cfg.AdminToken != "",
		"adminTokenRequired": s.adminRequired(),
		"hide":               cfg.Hide,
		"expose":             cfg.Expose,
		"configPath":         s.gw.ConfigPath(),
		"listingOnlyHide":    !cfg.Hide.BlockRouting,
		"requestLog":         cfg.RequestLog,
		"requestLogPath":     reqPath,
		"usagePath":          usagePath,
		"catalog":            cfg.Catalog,
		"lan":                lan,
		"lanWarning":         lan,
		"secretBackend":      backend,
		"secretBackendNote":  backendNote,
	}
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

func requestCtx(r *http.Request, raw []byte) context.Context {
	session := router.SessionID(r.Header, raw)
	ctx := router.WithSession(r.Context(), session)
	wire := requestmeta.WireChat
	switch r.URL.Path {
	case "/v1/messages":
		wire = requestmeta.WireMessages
	case "/v1/responses":
		wire = requestmeta.WireResponses
	case "/v1/embeddings":
		wire = requestmeta.WireEmbeddings
	case "/v1/images/generations", "/v1/images/edits":
		wire = requestmeta.WireImages
	}
	peek := jsonx.PeekBody(raw)
	return requestmeta.WithRequest(ctx, requestmeta.Request{
		ID:            r.Header.Get("X-Request-Id"),
		SessionID:     session,
		Model:         peek.Model,
		Wire:          wire,
		Requirements:  requestmeta.RequirementsFromBody(wire, raw),
		AnthropicBeta: requestmeta.NormalizeAnthropicBeta(strings.Join(r.Header.Values("anthropic-beta"), ",")),
	})
}

func (s *Server) record(account, model, proto, path string, stream bool, status int, err error, preview string, started time.Time, body []byte) {
	s.recordCall(account, model, proto, path, stream, status, err, preview, started, body, false)
}

func (s *Server) recordCall(account, model, proto, path string, stream bool, status int, err error, preview string, started time.Time, body []byte, cacheHit bool) {
	if s.gw == nil || s.gw.Usage == nil {
		return
	}
	e := usage.Event{
		AccountID:  account,
		Provider:   s.providerOf(account),
		Model:      model,
		Protocol:   proto,
		Path:       path,
		Stream:     stream,
		Status:     status,
		Preview:    preview,
		DurationMS: time.Since(started).Milliseconds(),
	}
	usage.ApplyPublishedUsage(&e, body, cacheHit)
	if err != nil {
		e.Error = usage.Redact(err.Error())
		if e.Status == 0 {
			e.Status = statusOf(err)
		}
	}
	if snap, ok := s.gw.QuotaSnapshot(account); ok && snap.Reported() && snap.CapturedAt != nil && !snap.CapturedAt.Before(started) {
		e.QuotaHint = snap.Compact()
	}
	s.gw.Usage.Add(e)
}

func (s *Server) providerOf(accountID string) string {
	if s.gw == nil || accountID == "" {
		return ""
	}
	for _, p := range s.gw.Config().Providers {
		if p.ID == accountID {
			return p.Adapter
		}
	}
	return ""
}

func inspectorPreview(raw []byte, response string) string {
	req := strings.TrimSpace(string(raw))
	if req == "" && response == "" {
		return ""
	}
	if response == "" {
		return req
	}
	return req + " → " + response
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

func readJSONBody(r *http.Request) ([]byte, error) {
	raw, err := readBody(r)
	if err != nil {
		return nil, err
	}
	if err := validateJSONObject(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateJSONObject(raw []byte) error {
	trimmed := bytes.TrimSpace(raw)
	var doc json.RawMessage
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return err
	}
	doc = bytes.TrimSpace(doc)
	if len(doc) == 0 || doc[0] != '{' {
		return errors.New("json body must be an object")
	}
	return nil
}

func statusOf(err error) int {
	var ce router.CooldownError
	if errors.As(err, &ce) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, router.ErrNoAccount) {
		return http.StatusNotFound
	}
	if errors.Is(err, adapter.ErrModelNotImageOut) || errors.Is(err, adapter.ErrImageOutUnsupported) || errors.Is(err, adapter.ErrImageModelRequired) {
		return http.StatusBadRequest
	}
	if errors.Is(err, adapter.ErrModelNotEmbeddings) || errors.Is(err, adapter.ErrEmbeddingsUnsupported) || errors.Is(err, adapter.ErrEmbeddingModelRequired) {
		return http.StatusBadRequest
	}
	var he adapter.HTTPError
	if errors.As(err, &he) && he.Status >= 400 {
		return he.Status
	}
	return http.StatusBadGateway
}

// errWire selects the error envelope a client is expecting. An OpenAI SDK reads
// error.type and error.message; an Anthropic SDK reads a top-level
// "type":"error" plus error.type. Neither reads a bare string, so on those two
// wires a plain string shows up as a generic message and misclassifies a rate
// limit as something the caller cannot retry.
type errWire int

const (
	// wireAdmin keeps the plain {"error":"..."} form. /admin/* is a local
	// operator surface, and the pre-routing middleware errors are ours, not a
	// provider's, so they keep the string form too.
	wireAdmin errWire = iota
	wireOpenAI
	wireAnthropic
)

// clientMessage is what the client is told. An upstream body can echo request
// fragments or credentials straight back at us, so any error that knows how to
// redact itself does. The full text stays in the local request log.
func clientMessage(err error) string {
	var s interface{ Sanitized() string }
	if errors.As(err, &s) {
		return s.Sanitized()
	}
	return err.Error()
}

// errorTypeFor derives the wire's error type from the status, which is what the
// SDKs branch on to decide whether a call is retryable.
func errorTypeFor(wr errWire, status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusRequestEntityTooLarge:
		if wr == wireAnthropic {
			return "request_too_large"
		}
		return "invalid_request_error"
	case http.StatusServiceUnavailable:
		if wr == wireAnthropic {
			return "overloaded_error"
		}
		return "server_error"
	}
	if status >= 500 {
		if wr == wireAnthropic {
			return "api_error"
		}
		return "server_error"
	}
	return "invalid_request_error"
}

func wireError(wr errWire, status int, err error) any {
	msg := clientMessage(err)
	switch wr {
	case wireOpenAI:
		// param and code are part of the shape even when null; SDKs read both.
		return map[string]any{"error": map[string]any{
			"message": msg,
			"type":    errorTypeFor(wr, status),
			"param":   nil,
			"code":    nil,
		}}
	case wireAnthropic:
		return map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    errorTypeFor(wr, status),
				"message": msg,
			},
		}
	default:
		return map[string]string{"error": msg}
	}
}

func writeErr(w http.ResponseWriter, wr errWire, err error) {
	status := statusOf(err)
	if sec := router.RetryAfterSeconds(err); sec > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(sec))
	}
	writeJSON(w, status, wireError(wr, status, err))
}

func errJSON(err error) map[string]string {
	return map[string]string{"error": clientMessage(err)}
}

func completionJSON(model, content string) map[string]any {
	return map[string]any{
		"id":      "peaproxy",
		"object":  "chat.completion",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
	}
}

func imagePreview(resp adapter.ImageResponse) string {
	if len(resp.URLs) > 0 {
		return strings.Join(resp.URLs, " ")
	}
	if len(resp.B64) > 0 {
		return fmt.Sprintf("%d b64 image(s)", len(resp.B64))
	}
	return ""
}

func embeddingPreview(resp adapter.EmbeddingResponse) string {
	return fmt.Sprintf("%d embedding(s) dim=%d", resp.Count, resp.Dimensions)
}

func imageDataJSON(resp adapter.ImageResponse) []map[string]string {
	out := make([]map[string]string, 0, len(resp.URLs)+len(resp.B64))
	for _, u := range resp.URLs {
		out = append(out, map[string]string{"url": u})
	}
	for _, b := range resp.B64 {
		out = append(out, map[string]string{"b64_json": b})
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type sseWriter struct {
	http.ResponseWriter
	started bool
	usage   []byte
}

func (s *sseWriter) Write(p []byte) (int, error) {
	if !s.started {
		s.Header().Set("Content-Type", "text/event-stream")
		s.Header().Set("Cache-Control", "no-cache")
		s.started = true
	}
	if bytes.Contains(p, []byte(`"usage"`)) {
		s.usage = append([]byte(nil), p...)
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
