// Package openai_oauth implements ChatGPT/Codex subscription OAuth.
//
// Flow is a clean-room reimplementation inspired by CLIProxyAPI (MIT,
// router-for-me/CLIProxyAPI): PKCE or device-code against auth.openai.com,
// then Codex Responses at chatgpt.com/backend-api/codex. Not an official
// third-party API; see docs/OAUTH.md for liability.
package openai_oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/translate"
)

const (
	Name           = "openai_oauth"
	AuthURL        = "https://auth.openai.com/oauth/authorize"
	TokenURL       = "https://auth.openai.com/oauth/token"
	ClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	RedirectURI    = "http://localhost:1455/auth/callback"
	Scope          = "openid email profile offline_access"
	DefaultAPIBase = "https://chatgpt.com/backend-api/codex"
	Originator     = "codex_cli_rs"
	ClientVersion  = "0.155.0"
	UserAgent      = Originator + "/" + ClientVersion
	callbackPort   = "1455"
	deviceUserURL  = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	devicePollURL  = "https://auth.openai.com/api/accounts/deviceauth/token"
	deviceVerify   = "https://auth.openai.com/codex/device"
	deviceRedirect = "https://auth.openai.com/deviceauth/callback"
)

// Adapter is a Codex/ChatGPT subscription OAuth client (Responses API).
type Adapter struct {
	id           string
	apiBase      string
	tokenURL     string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	flow         string
	mu           sync.Mutex
	token        oauth.Token
	generation   uint64
	commitMu     sync.Mutex
	pending      *pendingAuth
	skipLoopback bool
}

type pendingAuth struct {
	pkce         oauth.PKCE
	state        string
	lb           *oauth.Loopback
	deviceAuthID string
	userCode     string
	redirectURI  string
}

// New builds the adapter. Tokens come from Options.OAuth.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = DefaultAPIBase
	}
	return &Adapter{
		id:           id,
		apiBase:      base,
		tokenURL:     TokenURL,
		httpClient:   adapter.HTTPClient(0, opts.ObserveHeaders),
		persist:      opts.PersistOAuth,
		token:        opts.OAuth,
		flow:         strings.ToLower(strings.TrimSpace(opts.OAuthFlow)),
		skipLoopback: opts.SkipLoopback,
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

// AuthorizeURL builds the Codex login URL (no network).
func AuthorizeURL(pkce oauth.PKCE, state string) string {
	q := url.Values{
		"client_id":                  {ClientID},
		"response_type":              {"code"},
		"redirect_uri":               {RedirectURI},
		"scope":                      {Scope},
		"state":                      {state},
		"code_challenge":             {pkce.Challenge},
		"code_challenge_method":      {"S256"},
		"prompt":                     {"login"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
	}
	return AuthURL + "?" + q.Encode()
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	if a.flow == "device" {
		return a.startDevice(ctx)
	}
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	state, err := oauth.RandomState()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	pending := &pendingAuth{pkce: pkce, state: state, redirectURI: RedirectURI}
	// A new login supersedes one still waiting on the fixed callback port.
	// Release that listener first, or binding the port again fails with
	// "in use" until the old attempt times out.
	a.mu.Lock()
	if a.pending != nil && a.pending.lb != nil {
		_ = a.pending.lb.Close()
		a.pending.lb = nil
	}
	a.mu.Unlock()
	if !a.skipLoopback {
		lb, lerr := oauth.StartLoopback("127.0.0.1:"+callbackPort, "/auth/callback")
		if lerr != nil {
			return adapter.AuthSession{}, fmt.Errorf("openai_oauth: callback port %s is in use: %w", callbackPort, lerr)
		}
		lb.ExpectState(pending.state)
		pending.lb = lb
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

func (a *Adapter) startDevice(ctx context.Context) (adapter.AuthSession, error) {
	body, err := json.Marshal(struct {
		ClientID string `json:"client_id"`
	}{ClientID: ClientID})
	if err != nil {
		return adapter.AuthSession{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceUserURL, bytes.NewReader(body))
	if err != nil {
		return adapter.AuthSession{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return adapter.AuthSession{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return adapter.AuthSession{}, adapter.NewHTTPError(resp, truncate(raw))
	}
	var parsed struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		UserCodeAlt  string `json:"usercode"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return adapter.AuthSession{}, err
	}
	userCode := parsed.UserCode
	if userCode == "" {
		userCode = parsed.UserCodeAlt
	}
	a.mu.Lock()
	a.pending = &pendingAuth{deviceAuthID: parsed.DeviceAuthID, userCode: userCode, redirectURI: deviceRedirect}
	a.mu.Unlock()
	return adapter.AuthSession{
		Provider:        Name,
		LoginURL:        deviceVerify,
		UserCode:        userCode,
		VerificationURL: deviceVerify,
		ExpiresIn:       int((15 * time.Minute).Seconds()),
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("openai_oauth: no in-progress login (call AuthStart first)")
	}
	if pending.deviceAuthID != "" {
		return a.completeDevice(ctx, pending)
	}
	code, stateFromInput, err := oauth.CallbackFromInput(code)
	if err != nil {
		return err
	}
	fromLoopback := false
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
		fromLoopback = true
	}
	if pending.lb != nil {
		_ = pending.lb.Close()
	}
	if code == "" {
		return fmt.Errorf("openai_oauth: empty authorization code")
	}
	if err := oauth.ConfirmCallbackState(pending.state, stateFromInput, fromLoopback); err != nil {
		return err
	}
	_ = session
	redirect := pending.redirectURI
	if redirect == "" {
		redirect = RedirectURI
	}
	tok, err := a.exchange(ctx, code, redirect, pending.pkce)
	if err != nil {
		return err
	}
	return a.storeToken(tok)
}

func (a *Adapter) completeDevice(ctx context.Context, pending *pendingAuth) error {
	deadline := time.Now().Add(15 * time.Minute)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("openai_oauth: device login timed out")
		}
		body, err := json.Marshal(struct {
			DeviceAuthID string `json:"device_auth_id"`
			UserCode     string `json:"user_code"`
		}{DeviceAuthID: pending.deviceAuthID, UserCode: pending.userCode})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, devicePollURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := a.httpClient.Do(req)
		if err != nil {
			return err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				continue
			}
		}
		if resp.StatusCode >= 300 {
			return adapter.NewHTTPError(resp, truncate(raw))
		}
		var parsed struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
			CodeChallenge     string `json:"code_challenge"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return err
		}
		pkce := oauth.PKCE{Verifier: parsed.CodeVerifier, Challenge: parsed.CodeChallenge}
		tok, err := a.exchange(ctx, parsed.AuthorizationCode, deviceRedirect, pkce)
		if err != nil {
			return err
		}
		return a.storeToken(tok)
	}
}

func (a *Adapter) storeToken(tok oauth.Token) error {
	a.commitMu.Lock()
	defer a.commitMu.Unlock()
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

func (a *Adapter) exchange(ctx context.Context, code, redirect string, pkce oauth.PKCE) (oauth.Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {pkce.Verifier},
	}
	return a.postForm(ctx, form)
}

func (a *Adapter) refresh(ctx context.Context, refreshToken string) (oauth.Token, error) {
	form := url.Values{
		"client_id":     {ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {"openid profile email"},
	}
	tok, err := a.postForm(ctx, form)
	if err != nil {
		return oauth.Token{}, err
	}
	return tok.WithFallbackRefresh(refreshToken), nil
}

func (a *Adapter) postForm(ctx context.Context, form url.Values) (oauth.Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return oauth.Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return oauth.Token{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauth.Token{}, err
	}
	if resp.StatusCode >= 300 {
		return oauth.Token{}, adapter.NewHTTPError(resp, truncate(raw))
	}
	return oauth.ParseTokenResponse(raw)
}

func (a *Adapter) ensureToken(ctx context.Context) error {
	a.mu.Lock()
	tok := a.token
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
	return oauth.Ensure(ctx, &oauth.DefaultRefresh, "openai:"+a.id, &a.commitMu, &a.mu, &a.token, &a.generation,
		5*time.Minute,
		func(ctx context.Context, tok oauth.Token) (oauth.Token, error) {
			return a.refresh(ctx, tok.RefreshToken)
		},
		func(old, next oauth.Token) oauth.Token {
			if next.AccountID == "" {
				next.AccountID = old.AccountID
			}
			if next.Email == "" {
				next.Email = old.Email
			}
			return next
		},
		a.persist)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiBase+"/models", nil)
	if err != nil {
		return nil, err
	}
	// Official Codex CLI always appends client_version (openai/codex
	// ModelsClient::append_client_version_query). chatgpt.com rejects
	// GET /models without it: HTTP 400 "Field required: query client_version".
	q := req.URL.Query()
	q.Set("client_version", ClientVersion)
	req.URL.RawQuery = q.Encode()
	a.headers(req, false)
	c := *a.httpClient
	c.Timeout = 8 * time.Second
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai_oauth list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(body))
	}
	parsed, err := catalog.ParseCodexCatalog(body)
	if err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(parsed))
	for _, m := range parsed {
		m.Provider = Name
		m.AccountID = a.id
		m.Tier = catalog.TierPaid
		m.Status = "ready"
		m.SubscriptionOAuth = true
		m.Exposed = true
		m.Routable = true
		out = append(out, m)
	}
	return out, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	if err := a.ensureToken(ctx); err != nil {
		return adapter.ChatResponse{}, err
	}
	// Codex rejects stream:false. Non-stream clients still get one JSON body;
	// the upstream call is a stream that we assemble locally.
	raw, err := translate.ChatToResponses(req.Raw, req.Model, true)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if raw == nil {
		raw, err = translate.ChatToResponsesFromMessages(req.Model, req.Messages, true)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
	}
	body, err := a.postResponses(ctx, raw, true)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	body, err = translate.ResponsesStreamToJSON(body)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	oa, content, err := translate.ResponsesToChatCompletion(req.Model, body)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: content}, nil
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	raw, err := translate.ChatToResponses(req.Raw, req.Model, true)
	if err != nil {
		return err
	}
	if raw == nil {
		raw, err = translate.ChatToResponsesFromMessages(req.Model, req.Messages, true)
		if err != nil {
			return err
		}
	}
	resp, err := a.doResponses(ctx, raw, true)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.NewHTTPError(resp, truncate(body))
	}
	return translate.ResponsesSSEToOpenAI(resp.Body, w, req.Model)
}

func (a *Adapter) Responses(ctx context.Context, raw []byte) ([]byte, error) {
	if err := a.ensureToken(ctx); err != nil {
		return nil, err
	}
	body, err := a.postResponses(ctx, translate.PrepareResponses(raw, true), true)
	if err != nil {
		return nil, err
	}
	return translate.ResponsesStreamToJSON(body)
}

func (a *Adapter) ResponsesStream(ctx context.Context, raw []byte, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	resp, err := a.doResponses(ctx, translate.PrepareResponses(raw, true), true)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.NewHTTPError(resp, truncate(body))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (a *Adapter) postResponses(ctx context.Context, raw []byte, stream bool) ([]byte, error) {
	resp, err := a.doResponses(ctx, raw, stream)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, adapter.NewHTTPError(resp, truncate(body))
	}
	// The whole SSE stream is buffered here (Codex refuses stream:false), and
	// every lifecycle event repeats the response object, so a long answer is
	// large; past the limit it fails instead of truncating.
	return adapter.ReadBodyLimit(resp.Body, maxBufferedStream)
}

const maxBufferedStream = 32 << 20

func (a *Adapter) doResponses(ctx context.Context, raw []byte, stream bool) (*http.Response, error) {
	raw = translate.PrepareResponses(raw, stream)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiBase+"/responses", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	a.headers(httpReq, stream)
	if hint := routingHint(raw); hint != "" {
		httpReq.Header.Set("x-codex-routing-hint", hint)
	}
	return a.httpClient.Do(httpReq)
}

// routingHint builds the x-codex-routing-hint Codex sends on /responses from
// the body actually posted, so header and body cannot disagree.
func routingHint(raw []byte) string {
	var probe struct {
		Model       string `json:"model"`
		ServiceTier string `json:"service_tier"`
	}
	if json.Unmarshal(raw, &probe) != nil || !routingHintSafe(probe.Model) {
		return ""
	}
	hint := "model=" + probe.Model
	if probe.ServiceTier != "" {
		if !routingHintSafe(probe.ServiceTier) {
			return ""
		}
		hint += ";tier=" + probe.ServiceTier
	}
	return hint
}

func routingHintSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e || s[i] == ';' {
			return false
		}
	}
	return true
}

func (a *Adapter) headers(req *http.Request, stream bool) {
	a.mu.Lock()
	tok := a.token.AccessToken
	acct := a.token.AccountID
	a.mu.Unlock()
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if acct != "" {
		req.Header.Set("Chatgpt-Account-Id", acct)
	}
	req.Header.Set("Originator", Originator)
	req.Header.Set("User-Agent", UserAgent)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
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
var _ adapter.NativeResponses = (*Adapter)(nil)
