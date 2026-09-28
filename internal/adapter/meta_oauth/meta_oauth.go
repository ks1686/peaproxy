// Package meta_oauth implements Meta Muse subscription device-code OAuth.
//
// Clean-room reimplementation inspired by CLIProxyAPI (MIT): RFC 8628 against
// auth.meta.com, mint an API key, then OpenAI-compat chat at api.meta.ai.
// See docs/OAUTH.md.
package meta_oauth

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
	"github.com/ks1686/peaproxy/internal/adapter/oauthcompat"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
)

const (
	Name      = "meta_oauth"
	ClientID  = "1031625952748946"
	DeviceURL = "https://auth.meta.com/oidc/device/authorization/"
	TokenURL  = "https://auth.meta.com/oidc/device/token/"
	MintURL   = "https://api.meta.ai/muse-code/key"
	APIBase   = "https://api.meta.ai/v1"
	UserAgent = "muse-code/1.0.2"
	dcaExtra  = "dca_token"
)

// Adapter is a Meta Muse subscription OAuth client.
type Adapter struct {
	id           string
	deviceURL    string
	tokenURL     string
	mintURL      string
	apiBase      string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	pollInterval time.Duration
	mu           sync.Mutex
	token        oauth.Token
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
		base = APIBase
	}
	return &Adapter{
		id:           id,
		deviceURL:    DeviceURL,
		tokenURL:     TokenURL,
		mintURL:      MintURL,
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
	form := url.Values{"client_id": {ClientID}}
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
		return fmt.Errorf("meta_oauth: no in-progress login (call AuthStart first)")
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
	dca := tok.AccessToken
	tok = tok.WithExtra(dcaExtra, dca)
	minted, merr := a.mintKey(ctx, dca)
	if merr == nil && minted.APIKey != "" {
		tok.AccessToken = minted.APIKey
		if minted.Email != "" {
			tok.Email = minted.Email
		}
		if minted.BaseURL != "" {
			a.mu.Lock()
			a.apiBase = strings.TrimRight(minted.BaseURL, "/")
			a.mu.Unlock()
		}
	}
	return a.storeToken(tok)
}

type mintedKey struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
	Email   string `json:"user_email"`
}

type mintRequest struct {
	DCAToken string `json:"dca_token"`
}

func (a *Adapter) mintKey(ctx context.Context, dca string) (mintedKey, error) {
	body, err := json.Marshal(mintRequest{DCAToken: dca})
	if err != nil {
		return mintedKey{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.mintURL, bytes.NewReader(body))
	if err != nil {
		return mintedKey{}, err
	}
	req.Header.Set("Authorization", "Bearer "+dca)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return mintedKey{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return mintedKey{}, err
	}
	if resp.StatusCode >= 300 {
		return mintedKey{}, adapter.NewHTTPError(resp, truncate(raw))
	}
	var minted mintedKey
	if err := json.Unmarshal(raw, &minted); err != nil {
		return mintedKey{}, err
	}
	return minted, nil
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
	req.Header.Set("User-Agent", UserAgent)
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
	_ = ctx
	a.mu.Lock()
	tok := a.token
	a.mu.Unlock()
	if !tok.Valid() {
		return adapter.ErrAuthRequired
	}
	return nil
}

func (a *Adapter) compat() (*oauthcompat.Tagged, error) {
	a.mu.Lock()
	tok := a.token
	base := a.apiBase
	id := a.id
	a.mu.Unlock()
	return oauthcompat.Open(adapter.Options{
		ID: id, BaseURL: base, APIKey: tok.AccessToken, Tier: catalog.TierPaid,
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
