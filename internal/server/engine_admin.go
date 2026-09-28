package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/ks1686/peaproxy/internal/clients"
)

func (s *Server) handleClientConnect(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin client mutation rejected"})
		return
	}
	name := r.PathValue("name")
	var body struct {
		BaseURL string `json:"baseURL"`
		Model   string `json:"model"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
	}
	if body.BaseURL == "" {
		body.BaseURL = "http://127.0.0.1:8317/v1"
	}
	if err := s.clientLayout().Connect(name, body.BaseURL, body.Model); err != nil {
		if errors.Is(err, clients.ErrGuidedSetup) {
			snippet := ""
			if preset, ok := clients.Get(name); ok {
				snippet = preset.Snippet
			}
			writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "guided", "snippet": snippet})
			return
		}
		writeClientErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "connected"})
}

func (s *Server) handleClientDisconnect(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin client mutation rejected"})
		return
	}
	name := r.PathValue("name")
	if err := s.clientLayout().Disconnect(name); err != nil {
		writeClientErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "disconnected"})
}

func (s *Server) handleClientVerify(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin client mutation rejected"})
		return
	}
	name := r.PathValue("name")
	if _, ok := clients.Get(name); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown client"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "verify": "peaproxy clients verify " + name})
}

func (s *Server) handleEngine(w http.ResponseWriter, r *http.Request) {
	_ = r
	if s.gw == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "unavailable"})
		return
	}
	cfg := s.gw.Config()
	mode := cfg.RequestEngine.PromptCache
	if mode == "" {
		mode = "preserve"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"maxAttempts":     cfg.RequestMaxAttempts(),
		"deadline":        cfg.RequestDeadline().String(),
		"promptCache":     mode,
		"cacheResponses":  cfg.RequestEngine.CacheResponses,
		"cacheEmbeddings": cfg.RequestEngine.CacheEmbeddings,
		"automaticRoutes": cfg.AutomaticRoutes.Enabled,
		"maxInFlight":     cfg.RequestEngine.MaxInFlight,
	})
}

func (s *Server) clientLayout() clients.Layout {
	root := s.clientRoot
	if root == "" {
		root, _ = os.UserHomeDir()
	}
	return clients.Layout{Root: root}
}

func writeClientErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, clients.ErrUnknownClient):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown client"})
	case errors.Is(err, clients.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "client config changed"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

func sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}
