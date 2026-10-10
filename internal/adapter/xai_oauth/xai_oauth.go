// Package xai_oauth implements xAI Grok subscription device-code OAuth.
//
// Clean-room reimplementation inspired by CLIProxyAPI (MIT): RFC 8628 against
// auth.x.ai, then OpenAI-compat chat via the Grok CLI proxy. See docs/OAUTH.md.
package xai_oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/oauthcompat"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
)

const (
	Name           = "xai_oauth"
	ClientID       = "b1a00492-073a-47ea-816f-4c329264a828"
	Scope          = "openid profile email offline_access grok-cli:access api:access"
	DiscoveryURL   = "https://auth.x.ai/.well-known/openid-configuration"
	DefaultAPIBase = "https://cli-chat-proxy.grok.com/v1"
	tokenExtra     = "token_endpoint"
	// defaultGrokCLIVersion is the Grok CLI build cli-chat-proxy accepts.
	// The proxy reports a missing x-grok-client-version as version "none"
	// and answers HTTP 426. PEAPROXY_XAI_CLI_VERSION overrides this when
	// xAI raises the floor before the next release. Stable on 2026-10-10
	// was 1.0.50; the error text names 1.0.13 as the minimum.
	defaultGrokCLIVersion = "1.0.50"
)

func grokCLIVersion() string {
	if v := strings.TrimSpace(os.Getenv("PEAPROXY_XAI_CLI_VERSION")); v != "" {
		return v
	}
	return defaultGrokCLIVersion
}

func grokCLIHeaders() map[string]string {
	v := grokCLIVersion()
	return map[string]string{
		"x-grok-client-version": v,
		"X-XAI-Token-Auth":      "xai-grok-cli",
		"User-Agent":            "xai-grok-workspace/" + v,
	}
}

// Adapter is an xAI Grok subscription OAuth client (device code + chat proxy).
type Adapter struct {
	id           string
	discoveryURL string
	deviceURL    string
	tokenURL     string
	apiBase      string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	pollInterval time.Duration
	mu           sync.Mutex
	token        oauth.Token
	generation   uint64
	commitMu     sync.Mutex
	pending      *oauth.DeviceCode
}

// New builds the adapter.
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
		discoveryURL: DiscoveryURL,
		apiBase:      base,
		httpClient:   adapter.HTTPClient(30*time.Second, opts.ObserveHeaders),
		persist:      opts.PersistOAuth,
		token:        opts.OAuth,
		pollInterval: 5 * time.Second,
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

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	if err := a.ensureEndpoints(ctx); err != nil {
		return adapter.AuthSession{}, err
	}
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
		return fmt.Errorf("xai_oauth: no in-progress login (call AuthStart first)")
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
	tok, err := oauth.PollDevice(ctx, interval, max, func() (oauth.DevicePollResult, error) {
		form := url.Values{
			"grant_type":  {oauth.DeviceGrantType},
			"device_code": {pending.DeviceCode},
			"client_id":   {ClientID},
		}
		raw, status, _, perr := a.postFormStatus(ctx, a.tokenURL, form)
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
	tok = tok.WithExtra(tokenExtra, a.tokenURL)
	return a.storeToken(tok)
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

func (a *Adapter) ensureEndpoints(ctx context.Context) error {
	if a.deviceURL != "" && a.tokenURL != "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.discoveryURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return adapter.NewHTTPError(resp, truncate(raw))
	}
	var disc struct {
		DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
		TokenEndpoint               string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(raw, &disc); err != nil {
		return err
	}
	if a.deviceURL == "" {
		a.deviceURL = disc.DeviceAuthorizationEndpoint
	}
	if a.tokenURL == "" {
		a.tokenURL = disc.TokenEndpoint
	}
	if a.deviceURL == "" || a.tokenURL == "" {
		return fmt.Errorf("xai_oauth: discovery missing device or token endpoint")
	}
	return nil
}

func (a *Adapter) refresh(ctx context.Context, refreshToken string) (oauth.Token, error) {
	if err := a.ensureEndpoints(ctx); err != nil {
		return oauth.Token{}, err
	}
	endpoint := a.token.ExtraGet(tokenExtra)
	if endpoint == "" {
		endpoint = a.tokenURL
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {refreshToken},
	}
	raw, err := a.postForm(ctx, endpoint, form)
	if err != nil {
		return oauth.Token{}, err
	}
	tok, err := oauth.ParseTokenResponse(raw)
	if err != nil {
		return oauth.Token{}, err
	}
	return tok.WithFallbackRefresh(refreshToken), nil
}

func (a *Adapter) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, error) {
	raw, status, hdr, err := a.postFormStatus(ctx, endpoint, form)
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, adapter.NewHTTPError(&http.Response{StatusCode: status, Header: hdr}, truncate(raw))
	}
	return raw, nil
}

func (a *Adapter) postFormStatus(ctx context.Context, endpoint string, form url.Values) ([]byte, int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, resp.Header.Clone(), err
	}
	return raw, resp.StatusCode, resp.Header.Clone(), nil
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
	return oauth.Ensure(ctx, &oauth.DefaultRefresh, "xai:"+a.id, &a.commitMu, &a.mu, &a.token, &a.generation,
		5*time.Minute,
		func(ctx context.Context, tok oauth.Token) (oauth.Token, error) {
			return a.refresh(ctx, tok.RefreshToken)
		},
		func(old, next oauth.Token) oauth.Token { return next.KeepExtra(old) },
		a.persist)
}

func (a *Adapter) compat() (*oauthcompat.Tagged, error) {
	a.mu.Lock()
	tok := a.token
	base := a.apiBase
	id := a.id
	a.mu.Unlock()
	return oauthcompat.Open(adapter.Options{
		ID: id, BaseURL: base, APIKey: tok.AccessToken, Tier: catalog.TierPaid,
		ExtraHeaders: grokCLIHeaders(),
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
	inner, err := a.compat()
	if err != nil {
		return nil, err
	}
	return inner.ListModels(ctx)
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

func truncate(b []byte) string {
	const n = 240
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
