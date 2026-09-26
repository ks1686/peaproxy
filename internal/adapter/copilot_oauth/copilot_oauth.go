// Package copilot_oauth implements GitHub Copilot subscription device-code OAuth.
//
// Clean-room reimplementation. The GitHub App client id is the public VS Code
// Copilot Chat app (same class of public CLI client as Claude Code / Codex).
// Chat is api.githubcopilot.com — not retired GitHub Models. See docs/OAUTH.md.
package copilot_oauth

import (
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
	"github.com/ks1686/peaproxy/internal/adapter/oauthcompat"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
)

const (
	Name             = "copilot_oauth"
	ClientID         = "Iv1.b507a08c87ecfe98"
	Scope            = "read:user"
	DeviceURL        = "https://github.com/login/device/code"
	TokenURL         = "https://github.com/login/oauth/access_token"
	CopilotTokenURL  = "https://api.github.com/copilot_internal/v2/token"
	UserURL          = "https://api.github.com/user"
	DefaultAPIBase   = "https://api.githubcopilot.com"
	IntegrationID    = "vscode-chat"
	EditorVersion    = "vscode/1.99.3"
	EditorPlugin     = "copilot-chat/0.27.0"
	userAgent        = "peaproxy"
	githubTokenExtra = "github_token"
)

// Adapter is a GitHub Copilot subscription OAuth client (device code + chat).
type Adapter struct {
	id              string
	deviceURL       string
	tokenURL        string
	copilotTokenURL string
	userURL         string
	apiBase         string
	apiBaseLocked   bool
	httpClient      *http.Client
	persist         func(oauth.Token) error
	pollInterval    time.Duration
	mu              sync.Mutex
	token           oauth.Token
	pending         *oauth.DeviceCode
}

// New builds the adapter.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	locked := false
	if base == "" {
		base = DefaultAPIBase
	} else {
		locked = true
	}
	return &Adapter{
		id:              id,
		deviceURL:       DeviceURL,
		tokenURL:        TokenURL,
		copilotTokenURL: CopilotTokenURL,
		userURL:         UserURL,
		apiBase:         base,
		apiBaseLocked:   locked,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		persist:         opts.PersistOAuth,
		token:           opts.OAuth,
		pollInterval:    5 * time.Second,
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	a.mu.Lock()
	defer a.mu.Unlock()
	return adapter.Capabilities{
		Chat: true, Stream: true, VisionIn: true,
		ListModels: true, OAuth: true, NeedsAuth: !a.token.Valid() && a.token.RefreshToken == "",
		ImageOut: false, Embeddings: false,
	}
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	form := url.Values{"client_id": {ClientID}, "scope": {Scope}}
	raw, err := a.postForm(ctx, a.deviceURL, form)
	if err != nil {
		return adapter.AuthSession{}, err
	}
	dc, err := oauth.ParseDeviceCode(raw)
	if err != nil {
		return adapter.AuthSession{}, err
	}
	a.mu.Lock()
	a.pending = &dc
	a.mu.Unlock()
	return adapter.AuthSession{
		Provider:        Name,
		LoginURL:        dc.LoginURL(),
		UserCode:        dc.UserCode,
		VerificationURL: dc.LoginURL(),
		ExpiresIn:       dc.ExpiresIn,
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = session
	_ = code
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("copilot_oauth: no in-progress login (call AuthStart first)")
	}
	interval := a.pollInterval
	if pending.Interval > 0 {
		interval = time.Duration(pending.Interval) * time.Second
		if interval < a.pollInterval {
			interval = a.pollInterval
		}
	}
	max := 15 * time.Minute
	if pending.ExpiresIn > 0 {
		max = time.Duration(pending.ExpiresIn) * time.Second
	}
	githubTok, err := oauth.PollDevice(ctx, interval, max, func() (oauth.DevicePollResult, error) {
		form := url.Values{
			"grant_type":  {oauth.DeviceGrantType},
			"device_code": {pending.DeviceCode},
			"client_id":   {ClientID},
		}
		raw, status, perr := a.postFormStatus(ctx, a.tokenURL, form)
		if perr != nil && status == 0 {
			return oauth.DevicePollResult{}, perr
		}
		if status == 0 {
			status = 200
		}
		return oauth.InterpretDevicePoll(status, raw)
	})
	if err != nil {
		return err
	}
	sessionTok, err := a.exchangeCopilot(ctx, githubTok.AccessToken)
	if err != nil {
		return err
	}
	sessionTok.RefreshToken = githubTok.AccessToken
	sessionTok = sessionTok.WithExtra(githubTokenExtra, githubTok.AccessToken)
	if email := a.fetchLogin(ctx, githubTok.AccessToken); email != "" {
		sessionTok.Email = email
	}
	a.applyAPIBase(sessionTok.AccessToken)
	return a.storeToken(sessionTok)
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

func (a *Adapter) exchangeCopilot(ctx context.Context, githubToken string) (oauth.Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.copilotTokenURL, nil)
	if err != nil {
		return oauth.Token{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("User-Agent", userAgent)
	a.setCopilotHeaders(req)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return oauth.Token{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauth.Token{}, err
	}
	if resp.StatusCode == http.StatusForbidden {
		return oauth.Token{}, fmt.Errorf("copilot_oauth: GitHub Copilot is not available on this account (HTTP 403)")
	}
	if resp.StatusCode >= 300 {
		return oauth.Token{}, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(raw)}
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return oauth.Token{}, err
	}
	if strings.TrimSpace(body.Token) == "" {
		return oauth.Token{}, fmt.Errorf("copilot_oauth: empty Copilot session token")
	}
	tok := oauth.Token{AccessToken: body.Token}
	if body.ExpiresAt > 0 {
		tok.ExpiresAt = time.Unix(body.ExpiresAt, 0)
	} else {
		tok.ExpiresAt = time.Now().Add(30 * time.Minute)
	}
	return tok, nil
}

func (a *Adapter) fetchLogin(ctx context.Context, githubToken string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.userURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || resp.StatusCode >= 300 {
		return ""
	}
	var user struct {
		Login string `json:"login"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &user); err != nil {
		return ""
	}
	if user.Email != "" {
		return user.Email
	}
	return user.Login
}

func (a *Adapter) refresh(ctx context.Context, githubToken string) (oauth.Token, error) {
	tok, err := a.exchangeCopilot(ctx, githubToken)
	if err != nil {
		return oauth.Token{}, err
	}
	return tok.WithFallbackRefresh(githubToken), nil
}

func (a *Adapter) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	raw, status, err := a.postFormStatus(ctx, endpoint, form)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, adapter.HTTPError{Status: status, Body: truncate(raw)}
	}
	return raw, nil
}

func (a *Adapter) postFormStatus(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

func (a *Adapter) ensureToken(ctx context.Context) error {
	a.mu.Lock()
	tok := a.token
	a.mu.Unlock()
	github := tok.RefreshToken
	if github == "" {
		github = tok.ExtraGet(githubTokenExtra)
	}
	if !tok.NeedsRefresh(5 * time.Minute) {
		if !tok.Valid() {
			return adapter.ErrAuthRequired
		}
		return nil
	}
	if github == "" {
		return adapter.ErrAuthRequired
	}
	next, err := a.refresh(ctx, github)
	if err != nil {
		return err
	}
	next = next.KeepExtra(tok)
	next.RefreshToken = github
	a.applyAPIBase(next.AccessToken)
	return a.storeToken(next)
}

func (a *Adapter) applyAPIBase(session string) {
	if a.apiBaseLocked {
		return
	}
	if next := apiBaseFromSession(session, a.apiBase); next != "" {
		a.apiBase = next
	}
}

func (a *Adapter) setCopilotHeaders(req *http.Request) {
	req.Header.Set("Copilot-Integration-Id", IntegrationID)
	req.Header.Set("Editor-Version", EditorVersion)
	req.Header.Set("Editor-Plugin-Version", EditorPlugin)
	req.Header.Set("User-Agent", userAgent)
}

func (a *Adapter) copilotHeaders() map[string]string {
	return map[string]string{
		"Copilot-Integration-Id": IntegrationID,
		"Editor-Version":         EditorVersion,
		"Editor-Plugin-Version":  EditorPlugin,
		"User-Agent":             userAgent,
	}
}

func (a *Adapter) compat() (*oauthcompat.Tagged, error) {
	a.mu.Lock()
	tok := a.token
	base := a.apiBase
	id := a.id
	a.mu.Unlock()
	return oauthcompat.Open(adapter.Options{
		ID: id, BaseURL: base, APIKey: tok.AccessToken, Tier: catalog.TierPaid,
		ExtraHeaders: a.copilotHeaders(),
	}, Name)
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
	a.mu.Lock()
	tok := a.token
	base := a.apiBase
	id := a.id
	a.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	a.setCopilotHeaders(req)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot_oauth list models: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.HTTPError{Status: resp.StatusCode, Body: truncate(raw)}
	}
	var list struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	out := make([]catalog.Model, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" || !m.listed() {
			continue
		}
		mods := []string{"text"}
		if m.vision() {
			mods = append(mods, "image_in")
		}
		out = append(out, catalog.Model{
			ID:                m.ID,
			DisplayName:       m.Name,
			Provider:          Name,
			AccountID:         id,
			Tier:              catalog.TierPaid,
			Modalities:        mods,
			Status:            "ready",
			Exposed:           true,
			Routable:          true,
			SubscriptionOAuth: true,
		})
	}
	return out, nil
}

type copilotModel struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ModelPickerEnabled *bool  `json:"model_picker_enabled"`
	Policy             *struct {
		State string `json:"state"`
	} `json:"policy"`
	Capabilities *struct {
		Supports *struct {
			Vision bool `json:"vision"`
		} `json:"supports"`
	} `json:"capabilities"`
}

func (m copilotModel) listed() bool {
	if m.Policy != nil && strings.EqualFold(m.Policy.State, "disabled") {
		return false
	}
	if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
		return false
	}
	return true
}

func (m copilotModel) vision() bool {
	return m.Capabilities != nil && m.Capabilities.Supports != nil && m.Capabilities.Supports.Vision
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	if err := a.ensureToken(ctx); err != nil {
		return adapter.ChatResponse{}, err
	}
	inner, err := a.compat()
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return inner.Chat(ctx, req)
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	inner, err := a.compat()
	if err != nil {
		return err
	}
	return inner.ChatStream(ctx, req, w)
}

func apiBaseFromSession(token, fallback string) string {
	ep := ""
	for _, part := range strings.Split(token, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && k == "proxy-ep" {
			ep = strings.TrimSpace(v)
			break
		}
	}
	if ep == "" {
		return fallback
	}
	host := strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://")
	host = strings.TrimRight(host, "/")
	if strings.HasPrefix(host, "proxy.") {
		host = "api." + strings.TrimPrefix(host, "proxy.")
	}
	if host == "" {
		return fallback
	}
	return "https://" + host
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
