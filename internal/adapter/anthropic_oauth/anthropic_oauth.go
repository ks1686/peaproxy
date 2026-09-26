// Package anthropic_oauth implements Claude Pro/Max subscription OAuth.
//
// Flow is a clean-room reimplementation inspired by CLIProxyAPI (MIT,
// router-for-me/CLIProxyAPI): loopback PKCE against claude.ai / platform.claude.com,
// then Messages API with a Bearer token. Not an official third-party API;
// see docs/OAUTH.md for liability.
package anthropic_oauth

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
	OAuthBetas     = "claude-code-20250219,oauth-2025-04-20"
	callbackPort   = "54545"
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
	pending      *pendingAuth
	skipLoopback bool
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
		httpClient:   &http.Client{},
		persist:      opts.PersistOAuth,
		token:        opts.OAuth,
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
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(raw)}
	}
	return raw, nil
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
	next, err := a.refresh(ctx, tok.RefreshToken)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.token = next
	persist := a.persist
	a.mu.Unlock()
	if persist != nil {
		_ = persist(next)
	}
	return nil
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
	a.headers(req)
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
	raw = jsonx.SetStream(raw, false)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.headers(httpReq)
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
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	return body, nil
}

func (a *Adapter) MessagesStream(ctx context.Context, raw []byte, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	raw = jsonx.SetStream(raw, true)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	a.headers(httpReq)
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (a *Adapter) claudeBody(req adapter.ChatRequest, stream bool) ([]byte, error) {
	if len(req.Raw) > 0 && translate.LooksLikeClaude(req.Raw) {
		return jsonx.SetStream(req.Raw, stream), nil
	}
	if len(req.Raw) > 0 {
		return translate.ToClaude(req.Raw, stream)
	}
	oa, err := json.Marshal(struct {
		Model    string            `json:"model"`
		Messages []adapter.Message `json:"messages"`
		Stream   bool              `json:"stream"`
	}{Model: req.Model, Messages: req.Messages, Stream: stream})
	if err != nil {
		return nil, err
	}
	return translate.ToClaude(oa, stream)
}

func (a *Adapter) headers(req *http.Request) {
	a.mu.Lock()
	tok := a.token.AccessToken
	a.mu.Unlock()
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("anthropic-beta", OAuthBetas)
	req.Header.Set("x-app", "cli")
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
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
var _ adapter.NativeMessages = (*Adapter)(nil)
