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
	UserAgent      = "codex_cli_rs/0.155.0"
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
		httpClient:   &http.Client{},
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
		Chat: true, Stream: true, VisionIn: true,
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
	content := extractResponsesText(body)
	oa, err := toOpenAIChatJSON(req.Model, content)
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
	return a.postResponses(ctx, jsonx.SetStream(raw, false), false)
}

func (a *Adapter) ResponsesStream(ctx context.Context, raw []byte, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	resp, err := a.doResponses(ctx, jsonx.SetStream(raw, true), true)
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
	Model        string           `json:"model"`
	Instructions string           `json:"instructions,omitempty"`
	Input        []responsesInput `json:"input"`
	Stream       bool             `json:"stream"`
}

func chatToResponses(raw []byte, model string, stream bool) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Contains(raw, []byte(`"input"`)) && !bytes.Contains(raw, []byte(`"messages"`)) {
		return jsonx.SetStream(raw, stream), nil
	}
	var parsed struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Model != "" {
		model = parsed.Model
	}
	msgs := make([]adapter.Message, 0, len(parsed.Messages))
	for _, m := range parsed.Messages {
		msgs = append(msgs, adapter.Message{Role: m.Role, Content: messageContentString(m.Content)})
	}
	return chatToResponsesFromMessages(model, msgs, stream)
}

func chatToResponsesFromMessages(model string, msgs []adapter.Message, stream bool) ([]byte, error) {
	var instr strings.Builder
	var input []responsesInput
	for _, m := range msgs {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if instr.Len() > 0 {
				instr.WriteByte('\n')
			}
			instr.WriteString(m.Content)
		case "assistant":
			input = append(input, responsesInput{Role: "assistant", Content: []responsesPart{{Type: "output_text", Text: m.Content}}})
		default:
			input = append(input, responsesInput{Role: "user", Content: []responsesPart{{Type: "input_text", Text: m.Content}}})
		}
	}
	return json.Marshal(responsesBody{
		Model:        model,
		Instructions: instr.String(),
		Input:        input,
		Stream:       stream,
	})
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

func extractResponsesText(body []byte) string {
	var parsed struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if parsed.OutputText != "" {
		return parsed.OutputText
	}
	var b strings.Builder
	for _, o := range parsed.Output {
		for _, c := range o.Content {
			if c.Type == "output_text" || c.Type == "text" || c.Text != "" {
				b.WriteString(c.Text)
			}
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	if len(parsed.Choices) > 0 {
		return parsed.Choices[0].Message.Content
	}
	return ""
}

func toOpenAIChatJSON(model, content string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":      "peaproxy-codex",
		"object":  "chat.completion",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
	})
}

func responsesSSEToOpenAI(r io.Reader, w io.Writer, model string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		delta := extractStreamDelta(payload)
		if delta == "" {
			continue
		}
		chunk, err := json.Marshal(map[string]any{
			"id":      "peaproxy-codex",
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]string{"content": delta}}},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}

func extractStreamDelta(payload string) string {
	var ev struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Text  string `json:"text"`
	}
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return ""
	}
	switch {
	case strings.Contains(ev.Type, "output_text.delta"), strings.Contains(ev.Type, "text.delta"):
		if ev.Delta != "" {
			return ev.Delta
		}
		return ev.Text
	}
	if ev.Delta != "" && (ev.Type == "" || strings.Contains(ev.Type, "delta")) {
		return ev.Delta
	}
	return ""
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
