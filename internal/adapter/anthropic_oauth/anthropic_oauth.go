// Package anthropic_oauth implements Claude Pro/Max subscription OAuth.
//
// Flow is a clean-room reimplementation inspired by CLIProxyAPI (MIT,
// router-for-me/CLIProxyAPI): loopback PKCE against claude.ai / platform.claude.com,
// then Messages API with a Bearer token and Claude Code's inference fingerprint
// (User-Agent, Stainless headers, OAuth betas, metadata.user_id). Not an
// official third-party API; see docs/OAUTH.md for liability.
package anthropic_oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/translate"
)

const (
	Name           = "anthropic_oauth"
	DefaultBaseURL = "https://api.anthropic.com"
	AuthURL        = "https://claude.ai/oauth/authorize"
	TokenURL       = "https://platform.claude.com/v1/oauth/token"
	ClientID       = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	RedirectURI    = "http://localhost:54545/callback"
	Scope          = "user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	APIVersion     = "2023-06-01"
	callbackPort   = "54545"
	defaultMaxTok  = 4096
	// TokenUserAgent matches Claude Code's OAuth control-plane client (axios).
	TokenUserAgent = "axios/1.15.2"
	// MessagesUserAgent is Claude Code 2.1.280's inference User-Agent. Stock
	// Go's "Go-http-client/1.1" is treated as a bot signature; Anthropic then
	// answers OAuth /v1/messages with HTTP 429 rate_limit_error / "Error".
	MessagesUserAgent = "claude-cli/2.1.280 (external, cli)"
	// Stainless* match @anthropic-ai/sdk 0.112.1 as captured by CLIProxyAPI.
	StainlessPackageVersion = "0.112.1"
	StainlessRuntimeVersion = "v26.3.0"
	cloudflare403           = "Cloudflare/WAF likely blocked stock Go TLS on the Claude token endpoint. PeaProxy does not spoof TLS fingerprints. Use an official API key (adapter anthropic, https://console.anthropic.com/settings/keys) or retry from a typical desktop network. See docs/OAUTH.md."
)

// OAuth beta fragments match Claude Code 2.1.280 / CLIProxyAPI wire order.
// effort-2025-11-24 sits after mid-conversation-system (and tool-changes when
// present) and before extended-cache-ttl. oauthBetas omits effort for haiku
// and thinking.type=disabled, and omits tool-changes for claude-sonnet-5.
const (
	oauthBetasPrefix      = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,mid-conversation-system-2026-04-07"
	oauthBetasToolChanges = "mid-conversation-tool-changes-2026-07-01"
	oauthBetasEffort      = "effort-2025-11-24"
	oauthBetasCacheTTL    = "extended-cache-ttl-2025-04-11"
	// OAuthBetas is the sonnet-5 baseline with thinking enabled (includes effort).
	OAuthBetas = oauthBetasPrefix + "," + oauthBetasEffort + "," + oauthBetasCacheTTL
)

// Adapter is a Claude subscription OAuth client (Messages API + PKCE login).
type Adapter struct {
	id           string
	baseURL      string
	tokenURL     string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	mu           sync.Mutex
	token        oauth.Token
	generation   uint64
	pending      *pendingAuth
	skipLoopback bool
	deviceID     string
	sessionID    string
}

type claudeMetadataUserID struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
	SessionID   string `json:"session_id"`
}

type pendingAuth struct {
	pkce  oauth.PKCE
	state string
	lb    *oauth.Loopback
}

// New builds the adapter. Tokens come from Options.OAuth.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	base = strings.TrimSuffix(base, "/v1")
	return &Adapter{
		id:           id,
		baseURL:      base,
		tokenURL:     TokenURL,
		httpClient:   adapter.HTTPClient(0, opts.ObserveHeaders),
		persist:      opts.PersistOAuth,
		token:        opts.OAuth,
		skipLoopback: opts.SkipLoopback,
		deviceID:     newDeviceID(),
		sessionID:    randomUUID(),
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	a.mu.Lock()
	defer a.mu.Unlock()
	return adapter.Capabilities{
		Chat: true, Stream: true, VisionIn: true, Tools: true,
		ListModels: true, OAuth: true, NeedsAuth: !a.token.Valid(),
	}
}

// AuthorizeURL builds the Claude login URL (no network).
func AuthorizeURL(pkce oauth.PKCE, state string) string {
	q := url.Values{
		"code":                  {"true"},
		"client_id":             {ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {RedirectURI},
		"scope":                 {Scope},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	return AuthURL + "?" + q.Encode()
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	_ = ctx
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	state, err := oauth.RandomState()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	pending := &pendingAuth{pkce: pkce, state: state}
	if !a.skipLoopback {
		lb, lerr := oauth.StartLoopback("127.0.0.1:"+callbackPort, "/callback")
		if lerr != nil {
			// Port busy: user can paste the redirect URL into AuthComplete.
			pending.lb = nil
		} else {
			pending.lb = lb
		}
	}
	a.mu.Lock()
	if a.pending != nil && a.pending.lb != nil {
		_ = a.pending.lb.Close()
	}
	a.pending = pending
	a.mu.Unlock()
	return adapter.AuthSession{
		Provider: Name,
		LoginURL: AuthorizeURL(pkce, state),
		State:    state,
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("anthropic_oauth: no in-progress login (call AuthStart first)")
	}
	code, stateFromInput, err := oauth.CallbackFromInput(code)
	if err != nil {
		return err
	}
	if code == "" && pending.lb != nil {
		waitCtx := ctx
		if waitCtx == nil {
			waitCtx = context.Background()
		}
		waitCtx, cancel := context.WithTimeout(waitCtx, 5*time.Minute)
		defer cancel()
		res, werr := pending.lb.Wait(waitCtx)
		if werr != nil {
			return werr
		}
		code, stateFromInput = res.Code, res.State
	}
	if pending.lb != nil {
		_ = pending.lb.Close()
	}
	if code == "" {
		return fmt.Errorf("anthropic_oauth: empty authorization code")
	}
	state := pending.state
	if stateFromInput != "" {
		state = stateFromInput
	} else if session.State != "" {
		state = session.State
	}
	tok, err := a.exchange(ctx, code, state, pending.pkce)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.generation++
	a.token = tok
	a.pending = nil
	persist := a.persist
	a.mu.Unlock()
	if persist != nil {
		return persist(tok)
	}
	return nil
}

type authorizationCodeExchangeRequest struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	RedirectURI  string `json:"redirect_uri"`
	ClientID     string `json:"client_id"`
	CodeVerifier string `json:"code_verifier"`
	State        string `json:"state"`
}

type refreshRequest struct {
	ClientID     string `json:"client_id"`
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func (a *Adapter) exchange(ctx context.Context, code, state string, pkce oauth.PKCE) (oauth.Token, error) {
	if i := strings.IndexByte(code, '#'); i >= 0 {
		if strings.TrimSpace(state) == "" {
			state = code[i+1:]
		}
		code = code[:i]
	}
	body, err := json.Marshal(authorizationCodeExchangeRequest{
		GrantType:    "authorization_code",
		Code:         code,
		RedirectURI:  RedirectURI,
		ClientID:     ClientID,
		CodeVerifier: pkce.Verifier,
		State:        state,
	})
	if err != nil {
		return oauth.Token{}, err
	}
	raw, err := a.postJSON(ctx, a.tokenURL, body)
	if err != nil {
		return oauth.Token{}, err
	}
	return parseClaudeToken(raw)
}

func (a *Adapter) refresh(ctx context.Context, refreshToken string) (oauth.Token, error) {
	body, err := json.Marshal(refreshRequest{
		ClientID:     ClientID,
		GrantType:    "refresh_token",
		RefreshToken: refreshToken,
		Scope:        Scope,
	})
	if err != nil {
		return oauth.Token{}, err
	}
	raw, err := a.postJSON(ctx, a.tokenURL, body)
	if err != nil {
		return oauth.Token{}, err
	}
	tok, err := parseClaudeToken(raw)
	if err != nil {
		return oauth.Token{}, err
	}
	return tok.WithFallbackRefresh(refreshToken), nil
}

func parseClaudeToken(raw []byte) (oauth.Token, error) {
	tok, err := oauth.ParseTokenResponse(raw)
	if err != nil {
		return oauth.Token{}, err
	}
	var extra struct {
		Account struct {
			UUID         string `json:"uuid"`
			EmailAddress string `json:"email_address"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		} `json:"organization"`
	}
	_ = json.Unmarshal(raw, &extra)
	if extra.Account.EmailAddress != "" {
		tok.Email = extra.Account.EmailAddress
	}
	if extra.Account.UUID != "" {
		tok.AccountID = extra.Account.UUID
	}
	return tok, nil
}

func (a *Adapter) postJSON(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", TokenUserAgent)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, tokenStatusError(resp.StatusCode, raw)
	}
	return raw, nil
}

func tokenStatusError(status int, raw []byte) error {
	body := truncate(raw)
	if status == http.StatusForbidden {
		if body != "" {
			return adapter.HTTPError{Status: status, Body: cloudflare403 + " Upstream: " + body}
		}
		return adapter.HTTPError{Status: status, Body: cloudflare403}
	}
	return adapter.HTTPError{Status: status, Body: body}
}

func (a *Adapter) ensureToken(ctx context.Context) error {
	a.mu.Lock()
	tok := a.token
	seen := a.generation
	a.mu.Unlock()
	if !tok.NeedsRefresh(5 * time.Minute) {
		if !tok.Valid() {
			return adapter.ErrAuthRequired
		}
		return nil
	}
	if tok.RefreshToken == "" {
		return adapter.ErrAuthRequired
	}
	next, err := oauth.DefaultRefresh.Do(ctx, "anthropic:"+a.id, func(ctx context.Context) (oauth.Token, error) {
		return a.refresh(ctx, tok.RefreshToken)
	})
	if err != nil {
		return err
	}
	a.mu.Lock()
	latest := a.generation
	current := a.token
	kept, store, _ := oauth.KeepIfCurrent(seen, latest, current, next, nil)
	if store && a.generation == seen {
		a.token = kept
	} else {
		store = false
	}
	persist := a.persist
	a.mu.Unlock()
	if !store || persist == nil {
		return nil
	}
	return persist(kept)
}

func (a *Adapter) Validate(ctx context.Context) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	_, err := a.ListModels(ctx)
	return err
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	if err := a.ensureToken(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	a.headers(req, "", a.sessionID, nil)
	c := *a.httpClient
	c.Timeout = 8 * time.Second
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic_oauth list models: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	var list struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, catalog.Model{
			ID:                m.ID,
			DisplayName:       m.DisplayName,
			Provider:          Name,
			AccountID:         a.id,
			Tier:              catalog.TierPaid,
			Modalities:        catalog.InferModalities(m.ID),
			Status:            "ready",
			SubscriptionOAuth: true,
			Exposed:           true,
			Routable:          true,
		})
	}
	return out, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	raw, err := a.claudeBody(req, false)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	out, err := a.Messages(ctx, raw)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	oa, err := translate.FromClaude(out)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: translate.ClaudeText(out)}, nil
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	raw, err := a.claudeBody(req, true)
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- translate.ClaudeSSEToOpenAI(pr, w)
		_ = pr.Close()
	}()
	err = a.MessagesStream(ctx, raw, pw)
	_ = pw.Close()
	convErr := <-errCh
	if err != nil {
		return err
	}
	return convErr
}

func (a *Adapter) Messages(ctx context.Context, raw []byte) ([]byte, error) {
	if err := a.ensureToken(ctx); err != nil {
		return nil, err
	}
	raw = a.shapeOAuthBody(raw)
	raw, restore := aliasOAuthToolNames(raw)
	raw = jsonx.SetStream(raw, false)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.messagesURL(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.headers(httpReq, jsonx.PeekBody(raw).Model, claudeSessionID(raw), raw)
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(body))
	}
	return restoreOAuthToolNames(body, restore), nil
}

func (a *Adapter) MessagesStream(ctx context.Context, raw []byte, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	raw = a.shapeOAuthBody(raw)
	raw, restore := aliasOAuthToolNames(raw)
	raw = jsonx.SetStream(raw, true)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.messagesURL(), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.headers(httpReq, jsonx.PeekBody(raw).Model, claudeSessionID(raw), raw)
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.NewHTTPError(resp, truncate(body))
	}
	filter := &oAuthToolSSEFilter{dst: w, reverse: restore}
	_, err = io.Copy(filter, resp.Body)
	if flushErr := filter.Flush(); err == nil {
		err = flushErr
	}
	return err
}

func (a *Adapter) claudeBody(req adapter.ChatRequest, stream bool) ([]byte, error) {
	if len(req.Raw) > 0 && translate.LooksLikeClaude(req.Raw) {
		return withThinking(jsonx.SetStream(req.Raw, stream), req.ThinkingBudget), nil
	}
	if len(req.Raw) > 0 {
		body, err := translate.ToClaude(req.Raw, stream)
		if err != nil {
			return nil, err
		}
		return withThinking(body, req.ThinkingBudget), nil
	}
	oa, err := json.Marshal(struct {
		Model    string            `json:"model"`
		Messages []adapter.Message `json:"messages"`
		Stream   bool              `json:"stream"`
	}{Model: req.Model, Messages: req.Messages, Stream: stream})
	if err != nil {
		return nil, err
	}
	body, err := translate.ToClaude(oa, stream)
	if err != nil {
		return nil, err
	}
	return withThinking(body, req.ThinkingBudget), nil
}

func withThinking(raw []byte, budget int) []byte {
	if budget <= 0 {
		return raw
	}
	return translate.ApplyThinkingBudget(raw, budget)
}

func (a *Adapter) messagesURL() string {
	return a.baseURL + "/v1/messages?beta=true"
}

func (a *Adapter) headers(req *http.Request, model, sessionID string, body []byte) {
	a.mu.Lock()
	tok := a.token.AccessToken
	a.mu.Unlock()
	if sessionID == "" {
		sessionID = a.sessionID
	}
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("anthropic-beta", oauthBetas(model, body))
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	req.Header.Set("x-app", "cli")
	req.Header.Set("User-Agent", MessagesUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Stainless-Retry-Count", "0")
	req.Header.Set("X-Stainless-Timeout", "600")
	req.Header.Set("X-Stainless-Runtime", "node")
	req.Header.Set("X-Stainless-Lang", "js")
	req.Header.Set("X-Stainless-Package-Version", StainlessPackageVersion)
	req.Header.Set("X-Stainless-Runtime-Version", StainlessRuntimeVersion)
	req.Header.Set("X-Stainless-Os", stainlessOS())
	req.Header.Set("X-Stainless-Arch", stainlessArch())
	req.Header.Set("X-Claude-Code-Session-Id", sessionID)
	req.Header.Set("x-client-request-id", randomUUID())
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}

func (a *Adapter) shapeOAuthBody(raw []byte) []byte {
	raw = rewriteClaudeModel(raw)
	raw = ensureMaxTokens(raw, defaultMaxTok)
	if !hasValidClaudeUserID(raw) {
		raw = jsonx.SetTopLevelRaw(raw, "metadata", a.metadataJSON())
	}
	return applyOAuthCloak(raw)
}

func (a *Adapter) metadataJSON() []byte {
	a.mu.Lock()
	account := a.token.AccountID
	device := a.deviceID
	session := a.sessionID
	a.mu.Unlock()
	ident, err := json.Marshal(claudeMetadataUserID{
		DeviceID:    device,
		AccountUUID: account,
		SessionID:   session,
	})
	if err != nil {
		ident = []byte(`{}`)
	}
	userID, err := json.Marshal(string(ident))
	if err != nil {
		userID = []byte(`""`)
	}
	return []byte(`{"user_id":` + string(userID) + `}`)
}

func oauthBetas(model string, body []byte) string {
	canon := translate.CanonicalClaudeModel(model)
	parts := []string{oauthBetasPrefix}
	if !isClaudeSonnet5(canon) {
		parts = append(parts, oauthBetasToolChanges)
	}
	if includeEffortBeta(canon, body) {
		parts = append(parts, oauthBetasEffort)
	}
	parts = append(parts, oauthBetasCacheTTL)
	return strings.Join(parts, ",")
}

func includeEffortBeta(model string, body []byte) bool {
	if strings.Contains(model, "haiku") {
		return false
	}
	var probe struct {
		Thinking struct {
			Type string `json:"type"`
		} `json:"thinking"`
	}
	_ = json.Unmarshal(body, &probe)
	return !strings.EqualFold(strings.TrimSpace(probe.Thinking.Type), "disabled")
}

func isClaudeSonnet5(model string) bool {
	return model == "claude-sonnet-5" || strings.HasPrefix(model, "claude-sonnet-5-") || strings.HasPrefix(model, "claude-sonnet-5[")
}

func rewriteClaudeModel(raw []byte) []byte {
	cur := jsonx.PeekBody(raw).Model
	next := translate.CanonicalClaudeModel(cur)
	if next == "" || next == cur {
		return raw
	}
	b, err := json.Marshal(next)
	if err != nil {
		return raw
	}
	return jsonx.SetTopLevelRaw(raw, "model", b)
}

func ensureMaxTokens(raw []byte, def int) []byte {
	var probe struct {
		MaxTokens int `json:"max_tokens"`
	}
	_ = json.Unmarshal(raw, &probe)
	if probe.MaxTokens > 0 {
		return raw
	}
	return jsonx.SetTopLevelRaw(raw, "max_tokens", []byte(strconv.Itoa(def)))
}

func hasValidClaudeUserID(raw []byte) bool {
	var probe struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.Metadata.UserID == "" {
		return false
	}
	var ident claudeMetadataUserID
	if json.Unmarshal([]byte(probe.Metadata.UserID), &ident) != nil {
		return false
	}
	if len(ident.DeviceID) != 64 {
		return false
	}
	return looksLikeUUID(ident.SessionID)
}

func claudeSessionID(raw []byte) string {
	var probe struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &probe) != nil || probe.Metadata.UserID == "" {
		return ""
	}
	var ident claudeMetadataUserID
	if json.Unmarshal([]byte(probe.Metadata.UserID), &ident) != nil {
		return ""
	}
	return ident.SessionID
}

func stainlessOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "MacOS"
	case "windows":
		return "Windows"
	case "linux":
		return "Linux"
	case "freebsd":
		return "FreeBSD"
	default:
		return "Other::" + runtime.GOOS
	}
}

func stainlessArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	default:
		return "other::" + runtime.GOARCH
	}
}

func newDeviceID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
				return false
			}
		}
	}
	return true
}

func truncate(b []byte) string {
	const n = 240
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
var _ adapter.NativeMessages = (*Adapter)(nil)
