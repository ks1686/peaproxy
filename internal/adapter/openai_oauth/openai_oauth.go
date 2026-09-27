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
		return adapter.AuthSession{}, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(raw)}
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
		return fmt.Errorf("openai_oauth: empty authorization code")
	}
	_ = stateFromInput
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
			return adapter.HTTPError{Status: resp.StatusCode, Body: truncate(raw)}
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
		return oauth.Token{}, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(raw)}
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
	next, err := a.refresh(ctx, tok.RefreshToken)
	if err != nil {
		return err
	}
	if next.AccountID == "" {
		next.AccountID = tok.AccountID
	}
	if next.Email == "" {
		next.Email = tok.Email
	}
	return a.storeToken(next)
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
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
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
	raw, err := chatToResponses(req.Raw, req.Model, false)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if raw == nil {
		raw, err = chatToResponsesFromMessages(req.Model, req.Messages, false)
		if err != nil {
			return adapter.ChatResponse{}, err
		}
	}
	body, err := a.postResponses(ctx, raw, false)
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
		raw, err = chatToResponsesFromMessages(req.Model, req.Messages, true)
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
		return adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
	}
	return responsesSSEToOpenAI(resp.Body, w, req.Model)
}

func (a *Adapter) Responses(ctx context.Context, raw []byte) ([]byte, error) {
	if err := a.ensureToken(ctx); err != nil {
		return nil, err
	}
	return a.postResponses(ctx, prepareResponses(raw, false), false)
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
		return adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
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
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(body)}
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
	return a.httpClient.Do(httpReq)
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
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesBody struct {
	Model        string            `json:"model"`
	Instructions string            `json:"instructions,omitempty"`
	Input        []json.RawMessage `json:"input"`
	Stream       bool              `json:"stream"`
	Tools        json.RawMessage   `json:"tools,omitempty"`
	ToolChoice   json.RawMessage   `json:"tool_choice,omitempty"`
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
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
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
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls []chatToolCall  `json:"tool_calls,omitempty"`
}

func chatToResponses(raw []byte, model string, stream bool) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Contains(raw, []byte(`"input"`)) && !bytes.Contains(raw, []byte(`"messages"`)) {
		return prepareResponses(raw, stream), nil
	}
	var parsed struct {
		Model      string               `json:"model"`
		Tools      json.RawMessage      `json:"tools"`
		ToolChoice json.RawMessage      `json:"tool_choice"`
		Messages   []chatInboundMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Model != "" {
		model = parsed.Model
	}
	if len(parsed.Tools) > 0 || len(parsed.ToolChoice) > 0 || chatMessagesHaveTools(parsed.Messages) {
		return chatToResponsesWithTools(model, parsed.Messages, parsed.Tools, parsed.ToolChoice, stream)
	}
	msgs := make([]adapter.Message, 0, len(parsed.Messages))
	for _, m := range parsed.Messages {
		msgs = append(msgs, adapter.Message{Role: m.Role, Content: messageContentString(m.Content)})
	}
	return chatToResponsesFromMessages(model, msgs, stream)
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
	raw = jsonx.DropTopLevelKeys(raw, "stream_options", "max_output_tokens", "store")
	raw = jsonx.SetStream(raw, stream)
	return jsonx.SetBool(raw, "store", false)
}

func chatToResponsesFromMessages(model string, msgs []adapter.Message, stream bool) ([]byte, error) {
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
	raw, err := json.Marshal(responsesBody{
		Model:        model,
		Instructions: instr.String(),
		Input:        input,
		Stream:       stream,
	})
	if err != nil {
		return nil, err
	}
	return prepareResponses(raw, stream), nil
}

func chatToResponsesWithTools(model string, msgs []chatInboundMessage, tools, toolChoice json.RawMessage, stream bool) ([]byte, error) {
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
			if text == "" && len(m.ToolCalls) == 0 {
				raw, err := marshalResponsesMessage("assistant", "output_text", "")
				if err != nil {
					return nil, err
				}
				input = append(input, raw)
			}
		default:
			raw, err := marshalResponsesMessage("user", "input_text", messageContentString(m.Content))
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
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return prepareResponses(raw, stream), nil
}

func marshalResponsesMessage(role, partType, text string) (json.RawMessage, error) {
	return json.Marshal(responsesInput{Role: role, Content: []responsesPart{{Type: partType, Text: text}}})
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
	var text strings.Builder
	if parsed.OutputText != "" {
		text.WriteString(parsed.OutputText)
	}
	var calls []chatToolCall
	for _, o := range parsed.Output {
		switch o.Type {
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
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		if chunk, ok, err := responsesStreamChunkToChat(payload, model, callIndex, &nextIndex); err != nil {
			return err
		} else if ok {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk); err != nil {
				return err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}

func responsesStreamChunkToChat(payload, model string, callIndex map[string]int, nextIndex *int) ([]byte, bool, error) {
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
		return nil, false, nil
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
		idx := ev.OutputIndex
		key := ev.Item.CallID
		if key == "" {
			key = ev.Item.ID
		}
		if key != "" {
			if existing, ok := callIndex[key]; ok {
				idx = existing
			} else {
				idx = *nextIndex
				callIndex[key] = idx
				*nextIndex++
			}
		} else {
			idx = *nextIndex
			*nextIndex++
		}
		tc := chatStreamToolCall{Index: idx, ID: ev.Item.CallID, Type: "function"}
		tc.Function.Name = ev.Item.Name
		tc.Function.Arguments = ev.Item.Arguments
		delta.ToolCalls = []chatStreamToolCall{tc}
	case strings.Contains(ev.Type, "function_call_arguments.delta"):
		idx := ev.OutputIndex
		if ev.ItemID != "" {
			if existing, ok := callIndex[ev.ItemID]; ok {
				idx = existing
			}
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
		return nil, false, nil
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
		return nil, false, err
	}
	return raw, true, nil
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
