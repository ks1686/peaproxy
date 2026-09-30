// Package openai_oauth implements ChatGPT/Codex subscription OAuth.
//
// Flow is a clean-room reimplementation inspired by CLIProxyAPI (MIT,
// router-for-me/CLIProxyAPI): PKCE or device-code against auth.openai.com,
// then Codex Responses at chatgpt.com/backend-api/codex. Not an official
// third-party API; see docs/OAUTH.md for liability.
package openai_oauth

import (
	"bufio"
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
	if !a.skipLoopback {
		lb, lerr := oauth.StartLoopback("127.0.0.1:"+callbackPort, "/auth/callback")
		if lerr == nil {
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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
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
	next, err := oauth.DefaultRefresh.Do(ctx, "openai:"+a.id, func(ctx context.Context) (oauth.Token, error) {
		return a.refresh(ctx, tok.RefreshToken)
	})
	if err != nil {
		return err
	}
	if next.AccountID == "" {
		next.AccountID = tok.AccountID
	}
	if next.Email == "" {
		next.Email = tok.Email
	}
	a.mu.Lock()
	kept, store, _ := oauth.KeepIfCurrent(seen, a.generation, a.token, next, nil)
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
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(body))
	}
	ids := parseModelIDs(body)
	out := make([]catalog.Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, catalog.Model{
			ID:                id,
			Provider:          Name,
			AccountID:         a.id,
			Tier:              catalog.TierPaid,
			Modalities:        catalog.InferModalities(id),
			Status:            "ready",
			SubscriptionOAuth: true,
			Exposed:           true,
			Routable:          true,
		})
	}
	return out, nil
}

func parseModelIDs(body []byte) []string {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil
	}
	var ids []string
	for _, m := range list.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		for _, m := range list.Models {
			id := m.ID
			if id == "" {
				id = m.Slug
			}
			if id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	if err := a.ensureToken(ctx); err != nil {
		return adapter.ChatResponse{}, err
	}
	// Codex rejects stream:false. Non-stream clients still get one JSON body;
	// the upstream call is a stream that we assemble locally.
	raw, err := chatToResponses(req.Raw, req.Model, true)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if raw == nil {
		raw, err = chatToResponsesFromMessages(req.Model, req.Messages, chatCarry{}, true)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
	}
	body, err := a.postResponses(ctx, raw, true)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	body, err = responsesStreamToJSON(body)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	oa, content, err := responsesToChatCompletion(req.Model, body)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: content}, nil
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	raw, err := chatToResponses(req.Raw, req.Model, true)
	if err != nil {
		return err
	}
	if raw == nil {
		raw, err = chatToResponsesFromMessages(req.Model, req.Messages, chatCarry{}, true)
		if err != nil {
			return err
		}
	}
	resp, err := a.doResponses(ctx, raw, true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return adapter.NewHTTPError(resp, truncate(body))
	}
	return responsesSSEToOpenAI(resp.Body, w, req.Model)
}

func (a *Adapter) Responses(ctx context.Context, raw []byte) ([]byte, error) {
	if err := a.ensureToken(ctx); err != nil {
		return nil, err
	}
	body, err := a.postResponses(ctx, prepareResponses(raw, true), true)
	if err != nil {
		return nil, err
	}
	return responsesStreamToJSON(body)
}

func (a *Adapter) ResponsesStream(ctx context.Context, raw []byte, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	resp, err := a.doResponses(ctx, prepareResponses(raw, true), true)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
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
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(body))
	}
	return body, nil
}

func (a *Adapter) doResponses(ctx context.Context, raw []byte, stream bool) (*http.Response, error) {
	raw = prepareResponses(raw, stream)
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

type responsesInput struct {
	Role    string          `json:"role"`
	Content []responsesPart `json:"content"`
}

type responsesPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesBody struct {
	Model        string            `json:"model"`
	Instructions string            `json:"instructions,omitempty"`
	Input        []json.RawMessage `json:"input"`
	Stream       bool              `json:"stream"`
	Tools        json.RawMessage   `json:"tools,omitempty"`
	ToolChoice   json.RawMessage   `json:"tool_choice,omitempty"`

	ServiceTier       string              `json:"service_tier,omitempty"`
	Reasoning         *responsesReasoning `json:"reasoning,omitempty"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// chatCarry holds the chat request fields that have a Responses equivalent.
type chatCarry struct {
	ServiceTier       string
	ReasoningEffort   string
	ParallelToolCalls *bool
}

func (c chatCarry) apply(body *responsesBody) {
	body.ServiceTier = normalizeServiceTier(c.ServiceTier)
	if c.ReasoningEffort != "" {
		body.Reasoning = &responsesReasoning{Effort: c.ReasoningEffort}
	}
	body.ParallelToolCalls = c.ParallelToolCalls
}

// normalizeServiceTier maps a chat service_tier onto the values the ChatGPT
// backend accepts. Codex omits the field for the default tier, so every other
// value is dropped rather than risk a 400.
func normalizeServiceTier(s string) string {
	switch s {
	case "fast", "priority":
		return "priority"
	case "flex":
		return "flex"
	}
	return ""
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatInboundMessage struct {
	Role            string          `json:"role"`
	Content         json.RawMessage `json:"content"`
	ToolCalls       []chatToolCall  `json:"tool_calls"`
	ToolCallID      string          `json:"tool_call_id"`
	ReasoningOpaque json.RawMessage `json:"reasoning_opaque"`
}

type chatStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type chatStreamDelta struct {
	Content   string               `json:"content,omitempty"`
	ToolCalls []chatStreamToolCall `json:"tool_calls,omitempty"`
}

type chatChoiceMessage struct {
	Role            string          `json:"role"`
	Content         json.RawMessage `json:"content"`
	ToolCalls       []chatToolCall  `json:"tool_calls,omitempty"`
	ReasoningOpaque json.RawMessage `json:"reasoning_opaque,omitempty"`
}

func chatToResponses(raw []byte, model string, stream bool) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Contains(raw, []byte(`"input"`)) && !bytes.Contains(raw, []byte(`"messages"`)) {
		return prepareResponses(raw, stream), nil
	}
	var parsed struct {
		Model             string               `json:"model"`
		Tools             json.RawMessage      `json:"tools"`
		ToolChoice        json.RawMessage      `json:"tool_choice"`
		Messages          []chatInboundMessage `json:"messages"`
		ServiceTier       string               `json:"service_tier"`
		ReasoningEffort   string               `json:"reasoning_effort"`
		ParallelToolCalls *bool                `json:"parallel_tool_calls"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Model != "" {
		model = parsed.Model
	}
	carry := chatCarry{
		ServiceTier:       parsed.ServiceTier,
		ReasoningEffort:   parsed.ReasoningEffort,
		ParallelToolCalls: parsed.ParallelToolCalls,
	}
	if len(parsed.Tools) > 0 || len(parsed.ToolChoice) > 0 || chatMessagesHaveTools(parsed.Messages) || chatMessagesHaveOpaque(parsed.Messages) || chatMessagesHaveImages(parsed.Messages) {
		return chatToResponsesWithTools(model, parsed.Messages, parsed.Tools, parsed.ToolChoice, carry, stream)
	}
	msgs := make([]adapter.Message, 0, len(parsed.Messages))
	for _, m := range parsed.Messages {
		msgs = append(msgs, adapter.Message{Role: m.Role, Content: messageContentString(m.Content)})
	}
	return chatToResponsesFromMessages(model, msgs, carry, stream)
}

func chatMessagesHaveOpaque(msgs []chatInboundMessage) bool {
	for _, m := range msgs {
		raw := bytes.TrimSpace(m.ReasoningOpaque)
		if len(raw) > 0 && string(raw) != "null" && string(raw) != "[]" {
			return true
		}
	}
	return false
}

func chatMessagesHaveTools(msgs []chatInboundMessage) bool {
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 || m.ToolCallID != "" || strings.EqualFold(m.Role, "tool") {
			return true
		}
	}
	return false
}

func prepareResponses(raw []byte, stream bool) []byte {
	// chatgpt.com Codex OAuth rejects stream_options, max_output_tokens, and
	// any store value other than false (omit or true → HTTP 400).
	// OpenAI accepts input as a string; Codex requires a message list.
	raw = jsonx.DropTopLevelKeys(raw, "stream_options", "max_output_tokens", "store")
	raw = coerceResponsesInput(raw)
	raw = jsonx.SetStream(raw, stream)
	return jsonx.SetBool(raw, "store", false)
}

func coerceResponsesInput(raw []byte) []byte {
	var probe struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return raw
	}
	in := bytes.TrimSpace(probe.Input)
	if len(in) == 0 || in[0] != '"' {
		return raw
	}
	var text string
	if json.Unmarshal(in, &text) != nil {
		return raw
	}
	item, err := marshalResponsesMessage("user", "input_text", text)
	if err != nil {
		return raw
	}
	list, err := json.Marshal([]json.RawMessage{item})
	if err != nil {
		return raw
	}
	return jsonx.SetTopLevelRaw(raw, "input", list)
}

// responsesStreamToJSON turns a Codex SSE body into the completed response
// object. A JSON object is returned unchanged so a non-stream payload still parses.
func responsesStreamToJSON(body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty responses body")
	}
	if trimmed[0] == '{' {
		return trimmed, nil
	}
	var completed []byte
	var completedType string
	var text strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(trimmed))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.completed", "response.incomplete", "response.failed":
			resp := bytes.TrimSpace(ev.Response)
			if len(resp) > 0 && resp[0] == '{' {
				completed = append([]byte(nil), resp...)
				completedType = ev.Type
			}
		}
		if strings.HasSuffix(ev.Type, "output_text.delta") {
			text.WriteString(ev.Delta)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if completedType == "response.failed" {
		return nil, fmt.Errorf("responses stream failed")
	}
	if len(completed) > 0 {
		// Codex often completes with reasoning only. The answer arrived as
		// output_text.delta events, so keep that text when the object has none.
		if text.Len() > 0 && !responsesObjectHasText(completed) {
			return mergeResponsesDeltaText(completed, text.String())
		}
		return completed, nil
	}
	if text.Len() == 0 {
		return nil, fmt.Errorf("responses stream ended without a completed response")
	}
	return json.Marshal(map[string]any{
		"output_text": text.String(),
		"output": []any{
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "output_text", "text": text.String()},
				},
			},
		},
	})
}

func responsesObjectHasText(body []byte) bool {
	var parsed struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return false
	}
	if strings.TrimSpace(parsed.OutputText) != "" {
		return true
	}
	for _, item := range parsed.Output {
		for _, part := range item.Content {
			if strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
	}
	return false
}

func mergeResponsesDeltaText(completed []byte, text string) ([]byte, error) {
	quoted, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	msg, err := marshalResponsesMessage("assistant", "output_text", text)
	if err != nil {
		return nil, err
	}
	var existing struct {
		Output []json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(completed, &existing)
	items := append(existing.Output, msg)
	rawItems, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	out := jsonx.SetTopLevelRaw(completed, "output_text", quoted)
	return jsonx.SetTopLevelRaw(out, "output", rawItems), nil
}

func chatToResponsesFromMessages(model string, msgs []adapter.Message, carry chatCarry, stream bool) ([]byte, error) {
	var instr strings.Builder
	var input []json.RawMessage
	for _, m := range msgs {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if instr.Len() > 0 {
				instr.WriteByte('\n')
			}
			instr.WriteString(m.Content)
		case "assistant":
			raw, err := marshalResponsesMessage("assistant", "output_text", m.Content)
			if err != nil {
				return nil, err
			}
			input = append(input, raw)
		default:
			raw, err := marshalResponsesMessage("user", "input_text", m.Content)
			if err != nil {
				return nil, err
			}
			input = append(input, raw)
		}
	}
	body := responsesBody{
		Model:        model,
		Instructions: instr.String(),
		Input:        input,
		Stream:       stream,
	}
	carry.apply(&body)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return prepareResponses(raw, stream), nil
}

func chatToResponsesWithTools(model string, msgs []chatInboundMessage, tools, toolChoice json.RawMessage, carry chatCarry, stream bool) ([]byte, error) {
	var instr strings.Builder
	var input []json.RawMessage
	for _, m := range msgs {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if instr.Len() > 0 {
				instr.WriteByte('\n')
			}
			instr.WriteString(messageContentString(m.Content))
		case "tool":
			item, err := json.Marshal(responsesFunctionCallOutput{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: messageContentString(m.Content),
			})
			if err != nil {
				return nil, err
			}
			input = append(input, item)
		case "assistant":
			items, err := translate.ResponsesReasoningItems(m.ReasoningOpaque)
			if err != nil {
				return nil, err
			}
			input = append(input, items...)
			text := messageContentString(m.Content)
			if text != "" {
				raw, err := marshalResponsesMessage("assistant", "output_text", text)
				if err != nil {
					return nil, err
				}
				input = append(input, raw)
			}
			for _, tc := range m.ToolCalls {
				item, err := json.Marshal(responsesFunctionCall{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
				if err != nil {
					return nil, err
				}
				input = append(input, item)
			}
			if text == "" && len(m.ToolCalls) == 0 && len(items) == 0 {
				raw, err := marshalResponsesMessage("assistant", "output_text", "")
				if err != nil {
					return nil, err
				}
				input = append(input, raw)
			}
		default:
			raw, err := marshalResponsesParts("user", contentParts(m.Content, "input_text"))
			if err != nil {
				return nil, err
			}
			input = append(input, raw)
		}
	}
	body := responsesBody{
		Model:        model,
		Instructions: instr.String(),
		Input:        input,
		Stream:       stream,
		Tools:        chatToolsToResponses(tools),
		ToolChoice:   chatToolChoiceToResponses(toolChoice),
	}
	carry.apply(&body)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return prepareResponses(raw, stream), nil
}

func marshalResponsesMessage(role, partType, text string) (json.RawMessage, error) {
	return marshalResponsesParts(role, []responsesPart{{Type: partType, Text: text}})
}

func marshalResponsesParts(role string, parts []responsesPart) (json.RawMessage, error) {
	if len(parts) == 0 {
		parts = []responsesPart{{Type: "input_text", Text: ""}}
	}
	return json.Marshal(responsesInput{Role: role, Content: parts})
}

func chatMessagesHaveImages(msgs []chatInboundMessage) bool {
	for _, m := range msgs {
		for _, part := range contentParts(m.Content, "input_text") {
			if part.Type == "input_image" {
				return true
			}
		}
	}
	return false
}

func contentParts(raw json.RawMessage, textType string) []responsesPart {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return []responsesPart{{Type: textType, Text: s}}
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return []responsesPart{{Type: textType, Text: messageContentString(raw)}}
	}
	var out []responsesPart
	for _, p := range parts {
		switch p.Type {
		case "image_url", "input_image":
			if url := imageURLValue(p.ImageURL); url != "" {
				out = append(out, responsesPart{Type: "input_image", ImageURL: url})
			}
		default:
			if p.Text != "" {
				out = append(out, responsesPart{Type: textType, Text: p.Text})
			}
		}
	}
	if len(out) == 0 {
		if text := messageContentString(raw); text != "" {
			return []responsesPart{{Type: textType, Text: text}}
		}
	}
	return out
}

func imageURLValue(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.URL
	}
	return ""
}

func chatToolsToResponses(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var parsed struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Function    *struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if json.Unmarshal(item, &parsed) != nil {
			out = append(out, item)
			continue
		}
		if parsed.Function != nil && parsed.Function.Name != "" {
			flat := struct {
				Type        string          `json:"type"`
				Name        string          `json:"name"`
				Description string          `json:"description,omitempty"`
				Parameters  json.RawMessage `json:"parameters,omitempty"`
			}{Type: "function", Name: parsed.Function.Name, Description: parsed.Function.Description, Parameters: parsed.Function.Parameters}
			b, err := json.Marshal(flat)
			if err != nil {
				out = append(out, item)
				continue
			}
			out = append(out, b)
			continue
		}
		out = append(out, item)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

func chatToolChoiceToResponses(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		return raw
	}
	var parsed struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return raw
	}
	if parsed.Function != nil && parsed.Function.Name != "" {
		b, err := json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "function", Name: parsed.Function.Name})
		if err != nil {
			return raw
		}
		return b
	}
	return raw
}

func messageContentString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

func responsesToChatCompletion(model string, body []byte) ([]byte, string, error) {
	var parsed struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Choices []struct {
			Message struct {
				Content   string         `json:"content"`
				ToolCalls []chatToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", err
	}
	var outputRaw struct {
		Output []json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(body, &outputRaw)
	var reasoningItems []json.RawMessage
	for _, item := range outputRaw.Output {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &kind) == nil && kind.Type == "reasoning" {
			reasoningItems = append(reasoningItems, item)
		}
	}
	opaque, err := translate.CarryResponsesReasoning(reasoningItems)
	if err != nil {
		return nil, "", err
	}
	var text strings.Builder
	if parsed.OutputText != "" {
		text.WriteString(parsed.OutputText)
	}
	var calls []chatToolCall
	for _, o := range parsed.Output {
		switch o.Type {
		case "reasoning":
			continue
		case "function_call":
			tc := chatToolCall{ID: o.CallID, Type: "function"}
			tc.Function.Name = o.Name
			tc.Function.Arguments = o.Arguments
			calls = append(calls, tc)
		default:
			for _, c := range o.Content {
				if c.Type == "output_text" || c.Type == "text" || c.Text != "" {
					if parsed.OutputText == "" {
						text.WriteString(c.Text)
					}
				}
			}
		}
	}
	if text.Len() == 0 && len(parsed.Choices) > 0 {
		text.WriteString(parsed.Choices[0].Message.Content)
		if len(calls) == 0 {
			calls = parsed.Choices[0].Message.ToolCalls
		}
	}
	content := text.String()
	finish := "stop"
	msg := chatChoiceMessage{Role: "assistant"}
	msg.ReasoningOpaque = opaque
	if len(calls) > 0 {
		msg.ToolCalls = calls
		finish = "tool_calls"
		if content == "" {
			msg.Content = json.RawMessage("null")
		} else {
			raw, err := json.Marshal(content)
			if err != nil {
				return nil, "", err
			}
			msg.Content = raw
		}
	} else {
		raw, err := json.Marshal(content)
		if err != nil {
			return nil, "", err
		}
		msg.Content = raw
	}
	out := struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int               `json:"index"`
			Message      chatChoiceMessage `json:"message"`
			FinishReason string            `json:"finish_reason"`
		} `json:"choices"`
	}{ID: "peaproxy-codex", Object: "chat.completion", Model: model}
	out.Choices = make([]struct {
		Index        int               `json:"index"`
		Message      chatChoiceMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	}, 1)
	out.Choices[0].Message = msg
	out.Choices[0].FinishReason = finish
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	return raw, content, nil
}

func responsesSSEToOpenAI(r io.Reader, w io.Writer, model string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	callIndex := map[string]int{}
	nextIndex := 0
	wroteRole := false
	hadToolCalls := false
	var reasoningRaw []json.RawMessage
	const id = "peaproxy-codex"
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		if items := reasoningItemsFromResponsesEvent(payload); len(items) > 0 {
			reasoningRaw = items
		}
		chunk, ok, tools, err := responsesStreamChunkToChat(payload, model, callIndex, &nextIndex)
		if err != nil {
			return err
		}
		if !ok {
			// Reasoning and lifecycle events can run for a long time before the
			// first text delta. A comment keeps the prelude timer alive so a
			// live Codex stream is not canceled and failed over.
			if _, err := io.WriteString(w, ": ping\n"); err != nil {
				return err
			}
			continue
		}
		if tools {
			hadToolCalls = true
		}
		if !wroteRole {
			if err := translate.WriteOpenAIChatSSERole(w, id, model); err != nil {
				return err
			}
			wroteRole = true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	reason := "stop"
	if hadToolCalls {
		reason = "tool_calls"
	}
	if len(reasoningRaw) > 0 {
		opaque, err := translate.CarryResponsesReasoning(reasoningRaw)
		if err != nil {
			return err
		}
		if len(opaque) > 0 {
			if !wroteRole {
				if err := translate.WriteOpenAIChatSSERole(w, id, model); err != nil {
					return err
				}
			}
			if err := translate.WriteOpenAIChatSSEOpaque(w, id, model, opaque); err != nil {
				return err
			}
		}
	}
	return translate.WriteOpenAIChatSSEFinish(w, id, model, reason)
}

func reasoningItemsFromResponsesEvent(payload string) []json.RawMessage {
	var ev struct {
		Type     string          `json:"type"`
		Item     json.RawMessage `json:"item"`
		Response struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return nil
	}
	var fromOutput []json.RawMessage
	for _, item := range ev.Response.Output {
		if responsesEventItemType(item) == "reasoning" {
			fromOutput = append(fromOutput, item)
		}
	}
	if len(fromOutput) > 0 {
		return fromOutput
	}
	if strings.Contains(ev.Type, "output_item.added") && responsesEventItemType(ev.Item) == "reasoning" {
		return []json.RawMessage{ev.Item}
	}
	return nil
}

func responsesEventItemType(item json.RawMessage) string {
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(item, &kind) != nil {
		return ""
	}
	return kind.Type
}

func responsesStreamChunkToChat(payload, model string, callIndex map[string]int, nextIndex *int) ([]byte, bool, bool, error) {
	var ev struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Text  string `json:"text"`
		Item  *struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			ID        string `json:"id"`
		} `json:"item"`
		OutputIndex int    `json:"output_index"`
		ItemID      string `json:"item_id"`
	}
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return nil, false, false, nil
	}
	var delta chatStreamDelta
	switch {
	case strings.Contains(ev.Type, "output_text.delta"), strings.Contains(ev.Type, "text.delta"):
		if ev.Delta != "" {
			delta.Content = ev.Delta
		} else {
			delta.Content = ev.Text
		}
	case strings.Contains(ev.Type, "output_item.added") && ev.Item != nil && ev.Item.Type == "function_call":
		// Responses uses the output item id (fc_…) on later argument
		// deltas and the call id (call_…) on the item itself. output_index
		// also counts reasoning items, so it is not a chat tool slot.
		idx, found := resolveCallIndex(callIndex, ev.Item.CallID, ev.Item.ID, ev.OutputIndex)
		if !found {
			idx = *nextIndex
			*nextIndex++
		}
		rememberCallIndex(callIndex, idx, ev.Item.CallID, ev.Item.ID, ev.OutputIndex)
		id := ev.Item.CallID
		if id == "" {
			id = ev.Item.ID
		}
		tc := chatStreamToolCall{Index: idx, ID: id, Type: "function"}
		tc.Function.Name = ev.Item.Name
		tc.Function.Arguments = ev.Item.Arguments
		delta.ToolCalls = []chatStreamToolCall{tc}
	case strings.Contains(ev.Type, "function_call_arguments.delta"):
		idx, found := resolveCallIndex(callIndex, "", ev.ItemID, ev.OutputIndex)
		if !found {
			break
		}
		tc := chatStreamToolCall{Index: idx}
		tc.Function.Arguments = ev.Delta
		if tc.Function.Arguments == "" {
			tc.Function.Arguments = ev.Text
		}
		delta.ToolCalls = []chatStreamToolCall{tc}
	default:
		if ev.Delta != "" && (ev.Type == "" || strings.Contains(ev.Type, "delta")) && !strings.Contains(ev.Type, "function_call") {
			delta.Content = ev.Delta
		}
	}
	if delta.Content == "" && len(delta.ToolCalls) == 0 {
		return nil, false, false, nil
	}
	chunk := struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index int             `json:"index"`
			Delta chatStreamDelta `json:"delta"`
		} `json:"choices"`
	}{ID: "peaproxy-codex", Object: "chat.completion.chunk", Model: model}
	chunk.Choices = make([]struct {
		Index int             `json:"index"`
		Delta chatStreamDelta `json:"delta"`
	}, 1)
	chunk.Choices[0].Delta = delta
	raw, err := json.Marshal(chunk)
	if err != nil {
		return nil, false, false, err
	}
	return raw, true, len(delta.ToolCalls) > 0, nil
}

// callIndex keys a chat tool slot by every id Codex may use for that call.
// output-index is stored only after the call is known, so a bare responses
// output_index cannot open a new slot that has no string id.
func rememberCallIndex(callIndex map[string]int, idx int, callID, itemID string, outputIndex int) {
	for _, key := range callIndexKeys(callID, itemID, outputIndex) {
		callIndex[key] = idx
	}
}

func resolveCallIndex(callIndex map[string]int, callID, itemID string, outputIndex int) (int, bool) {
	for _, key := range callIndexKeys(callID, itemID, outputIndex) {
		if idx, ok := callIndex[key]; ok {
			return idx, true
		}
	}
	return 0, false
}

func callIndexKeys(callID, itemID string, outputIndex int) []string {
	keys := make([]string, 0, 3)
	if callID != "" {
		keys = append(keys, callID)
	}
	if itemID != "" && itemID != callID {
		keys = append(keys, itemID)
	}
	keys = append(keys, fmt.Sprintf("output-index:%d", outputIndex))
	return keys
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
