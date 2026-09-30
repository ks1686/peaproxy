// Package kimi_oauth implements Moonshot Kimi subscription device-code OAuth.
//
// Clean-room reimplementation inspired by CLIProxyAPI (MIT): RFC 8628 against
// auth.kimi.com / auth.kimi.ai, then OpenAI-compat coding API. See docs/OAUTH.md.
package kimi_oauth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/adapter/oauthcompat"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/version"
)

const (
	Name        = "kimi_oauth"
	NameAI      = "kimi_ai_oauth"
	ClientID    = "17e5f671-d194-4dfb-9706-5516cb48c098"
	OAuthHost   = "https://auth.kimi.com"
	AIOAuthHost = "https://auth.kimi.ai"
	APIBase     = "https://api.kimi.com/coding/v1"
	AIAPIBase   = "https://api.kimi.ai/coding/v1"
	deviceExtra = "device_id"
	domainExtra = "domain"
)

// Adapter is a Kimi / Kimi.ai subscription OAuth client.
type Adapter struct {
	id           string
	provider     string
	oauthHost    string
	apiBase      string
	deviceID     string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	pollInterval time.Duration
	mu           sync.Mutex
	token        oauth.Token
	generation   uint64
	commitMu     sync.Mutex
	pending      *oauth.DeviceCode
}

// New builds a kimi.com adapter.
func New(opts adapter.Options) (adapter.Adapter, error) {
	return newAdapter(opts, false)
}

// NewAI builds a kimi.ai adapter.
func NewAI(opts adapter.Options) (adapter.Adapter, error) {
	return newAdapter(opts, true)
}

func newAdapter(opts adapter.Options, ai bool) (adapter.Adapter, error) {
	id := opts.ID
	provider := Name
	host := OAuthHost
	base := APIBase
	domain := "kimi.com"
	if ai {
		provider = NameAI
		host = AIOAuthHost
		base = AIAPIBase
		domain = "kimi.ai"
	}
	if id == "" {
		id = provider
	}
	if opts.BaseURL != "" {
		base = strings.TrimRight(opts.BaseURL, "/")
	}
	deviceID := opts.OAuth.ExtraGet(deviceExtra)
	if deviceID == "" {
		deviceID, _ = oauth.RandomState()
	}
	tok := opts.OAuth
	if tok.ExtraGet(domainExtra) == "" {
		tok = tok.WithExtra(domainExtra, domain)
	}
	if tok.ExtraGet(deviceExtra) == "" && deviceID != "" {
		tok = tok.WithExtra(deviceExtra, deviceID)
	}
	return &Adapter{
		id:           id,
		provider:     provider,
		oauthHost:    host,
		apiBase:      base,
		deviceID:     deviceID,
		httpClient:   adapter.HTTPClient(30*time.Second, opts.ObserveHeaders),
		persist:      opts.PersistOAuth,
		token:        tok,
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
	form := url.Values{"client_id": {ClientID}}
	raw, err := a.postForm(ctx, a.oauthHost+"/api/oauth/device_authorization", form)
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
		Provider:        a.provider,
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
	deviceID := a.deviceID
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("kimi_oauth: no in-progress login (call AuthStart first)")
	}
	interval := a.pollInterval
	if pending.Interval > 0 {
		d := time.Duration(pending.Interval) * time.Second
		if d > interval {
			interval = d
		}
	}
	max := 15 * time.Minute
	if pending.ExpiresIn > 0 {
		max = time.Duration(pending.ExpiresIn) * time.Second
	}
	tok, err := oauth.PollDevice(ctx, interval, max, func() (oauth.DevicePollResult, error) {
		form := url.Values{
			"client_id":   {ClientID},
			"device_code": {pending.DeviceCode},
			"grant_type":  {oauth.DeviceGrantType},
		}
		raw, status, _, perr := a.postFormStatus(ctx, a.oauthHost+"/api/oauth/token", form)
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
	tok = tok.WithExtra(deviceExtra, deviceID)
	a.mu.Lock()
	domain := a.token.ExtraGet(domainExtra)
	a.mu.Unlock()
	if domain != "" {
		tok = tok.WithExtra(domainExtra, domain)
	}
	return a.storeToken(tok)
}

func (a *Adapter) storeToken(tok oauth.Token) error {
	a.commitMu.Lock()
	defer a.commitMu.Unlock()
	a.mu.Lock()
	a.generation++
	a.token = tok
	if id := tok.ExtraGet(deviceExtra); id != "" {
		a.deviceID = id
	}
	a.pending = nil
	persist := a.persist
	a.mu.Unlock()
	if persist != nil {
		return persist(tok)
	}
	return nil
}

func (a *Adapter) refresh(ctx context.Context, refreshToken string) (oauth.Token, error) {
	form := url.Values{
		"client_id":     {ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	raw, err := a.postForm(ctx, a.oauthHost+"/api/oauth/token", form)
	if err != nil {
		return oauth.Token{}, err
	}
	tok, err := oauth.ParseTokenResponse(raw)
	if err != nil {
		return oauth.Token{}, err
	}
	return tok.WithFallbackRefresh(refreshToken), nil
}

func (a *Adapter) mshHeaders(req *http.Request) {
	a.mu.Lock()
	deviceID := a.deviceID
	if deviceID == "" {
		deviceID = a.token.ExtraGet(deviceExtra)
	}
	a.mu.Unlock()
	req.Header.Set("X-Msh-Platform", "PeaProxy")
	req.Header.Set("X-Msh-Version", version.Version)
	if h, err := os.Hostname(); err == nil {
		req.Header.Set("X-Msh-Device-Name", h)
	}
	req.Header.Set("X-Msh-Device-Model", runtime.GOOS+" "+runtime.GOARCH)
	if deviceID != "" {
		req.Header.Set("X-Msh-Device-Id", deviceID)
	}
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
	a.mshHeaders(req)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
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
	return oauth.Ensure(ctx, &oauth.DefaultRefresh, "kimi:"+a.id, &a.commitMu, &a.mu, &a.token, &a.generation,
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
	deviceID := a.deviceID
	if deviceID == "" {
		deviceID = tok.ExtraGet(deviceExtra)
	}
	provider := a.provider
	a.mu.Unlock()
	headers := map[string]string{
		"X-Msh-Platform":     "PeaProxy",
		"X-Msh-Version":      version.Version,
		"X-Msh-Device-Model": runtime.GOOS + " " + runtime.GOARCH,
	}
	if h, err := os.Hostname(); err == nil {
		headers["X-Msh-Device-Name"] = h
	}
	if deviceID != "" {
		headers["X-Msh-Device-Id"] = deviceID
	}
	return oauthcompat.Open(adapter.Options{
		ID: id, BaseURL: base, APIKey: tok.AccessToken, Tier: catalog.TierPaid,
		ExtraHeaders: headers,
	}, provider)
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
