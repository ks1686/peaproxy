package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/ks1686/peaproxy/internal/config"
	"github.com/ks1686/peaproxy/internal/usage"

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
		body.BaseURL = s.clientBaseURL()
	}
	if err := s.clientLayout().Connect(name, body.BaseURL, body.Model); err != nil {
		if errors.Is(err, clients.ErrGuidedSetup) {
			snippet := ""
			if preset, ok := clients.Get(name); ok {
				snippet = clients.WithOrigin(preset, body.BaseURL).Snippet
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
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "verify": clients.VerifyHint("peaproxy clients verify "+name, s.clientOrigin())})
}

func (s *Server) clientOrigin() string {
	origin := clients.NormalizeOrigin(s.clientBaseURL())
	if origin == "" {
		return clients.DefaultOrigin
	}
	return origin
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

// clientBaseURL is the address clients should use to reach this server. It
// follows the bound listener rather than the saved settings, which can change
// while the listener stays put; unspecified binds are reached over loopback.
func (s *Server) clientBaseURL() string {
	addr, _ := s.listenAddr.Load().(string)
	if addr == "" {
		addr = s.http.Addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return clients.DefaultOrigin + "/v1"
	}
	if host == "" || net.ParseIP(host).IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/v1"
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

// handlePolicy reports what v3 is currently doing, so an opinionated default is
// something a user can see and turn off rather than merely accept.
//
// Every value is the resolved one. The nullable booleans inside the config are
// an internal distinction between "not said" and "said no"; what a user needs
// to know is whether a behaviour is on right now.
//
// Spend is reported with the same honesty rules as everything else. A ceiling
// that is not configured is null, not zero, because zero would read as "you
// have spent nothing and the budget is gone". And when recorded spend cannot be
// measured, the panel says so rather than presenting a partial total as a
// complete one.
func (s *Server) handlePolicy(w http.ResponseWriter, r *http.Request) {
	_ = r
	cfg := s.gw.Config()

	spent := usage.SpendWindow{}
	reserved := 0.0
	if u := s.gw.Usage; u != nil {
		spent = u.SpentInLastDays(30)
		reserved = u.Reserved()
	}

	ceiling := any(nil)
	if v := cfg.SpendCeiling(); v > 0 {
		ceiling = v
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"policy": map[string]any{
			"automatic":              cfg.OptimizationEnabled(),
			"freeOnly":               cfg.FreeOnly(),
			"contextOptimization":    s.gw.ContextOptimizationEnabled(),
			"spendCeilingUSD":        ceiling,
			"spentLast30DaysUSD":     spent.USD,
			"estimatedLast30DaysUSD": spent.EstimatedUSD,
			"inFlightReservedUSD":    reserved,
			"pricedCallsLast30Days":  spent.Priced,
			"totalCallsLast30Days":   spent.Total,
			"spendMeasurable":        spent.Priced == spent.Total,
			"spendNote":              spendNote(spent, cfg.SpendCeiling()),
			"promptCache":            cfg.EffectivePromptCache(),
			"localAssistant":         cfg.LocalAssistantEnabled(),
			"localEndpointConfigured": func() bool {
				_, ok := cfg.LocalAssistantConfig()
				return ok
			}(),
			"persistentContext": cfg.PersistentContextEnabled(),
			"policyVersion":     config.OptimizationPolicyVersion,
			"schemaVersion":     config.SchemaVersion,
		},
		"honesty": "spendCeilingUSD is null when no ceiling is set; 0 there would mean a budget of zero. " +
			"spendMeasurable is false when calls in the window carried no published price, and spentLast30DaysUSD is then a floor, not a total. " +
			"estimatedLast30DaysUSD is the part of that total PeaProxy priced from published tokens rather than a provider's own cost figure.",
	})
}

func spendNote(w usage.SpendWindow, ceiling float64) string {
	switch {
	case w.Priced < w.Total:
		return "some calls in this window had no published price, so the total is a floor rather than a sum"
	case w.EstimatedUSD > 0 && ceiling > 0:
		return fmt.Sprintf("measured spend over the last 30 days, of which %.2f USD was estimated from published token counts, against the configured ceiling", w.EstimatedUSD)
	case ceiling > 0:
		return "measured spend over the last 30 days, against the configured ceiling"
	default:
		return "measured spend over the last 30 days; no ceiling is configured"
	}
}
