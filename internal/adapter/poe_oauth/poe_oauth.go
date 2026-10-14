// Package poe_oauth implements Poe subscription login.
//
// The flow matches the public OpenCode client: PKCE on an ephemeral
// 127.0.0.1 port, then POST https://api.poe.com/token for an API key.
// Chat is OpenAI-compatible at https://api.poe.com/v1. There is no refresh
// token. A subscribed chat was not completed from PeaProxy. See docs/OAUTH.md.
package poe_oauth

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
	Name     = "poe_oauth"
	ClientID = "client_728290227fc048cc9262091a1ea197ea"
	Issuer   = "https://poe.com"
	TokenURL = "https://api.poe.com/token"
	APIBase  = "https://api.poe.com/v1"
	Scope    = "apikey:create"
)

// Adapter is a Poe subscription browser-login client.
type Adapter struct {
	id         string
	tokenURL   string
	apiBase    string
	httpClient *http.Client
	persist    func(oauth.Token) error
	skip       bool
	mu         sync.Mutex
	token      oauth.Token
	pending    *poePending
}

type poePending struct {
	pkce     oauth.PKCE
	state    string
	redirect string
	lb       *oauth.Loopback
}

// New builds the adapter.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{
		id:         id,
		tokenURL:   TokenURL,
		apiBase:    APIBase,
		httpClient: adapter.HTTPClient(30*time.Second, opts.ObserveHeaders),
		persist:    opts.PersistOAuth,
		token:      opts.OAuth,
		skip:       opts.SkipLoopback,
	}, nil
}

func (a *Adapter) ID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	a.mu.Lock()
	defer a.mu.Unlock()
	return adapter.Capabilities{
		Chat: true, Stream: true, Tools: true, VisionIn: true,
		ListModels: true, OAuth: true, NeedsAuth: !a.token.Valid(),
	}
}

// AuthorizeURL builds the Poe login URL (no network).
func AuthorizeURL(redirect, challenge, state string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {ClientID},
		"redirect_uri":          {redirect},
		"scope":                 {Scope},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	return Issuer + "/oauth/authorize?" + q.Encode()
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	_ = ctx
	if a.skip {
		return adapter.AuthSession{}, fmt.Errorf("poe_oauth: --print-url cannot complete this login because the callback port is chosen when the browser opens; run without --print-url")
	}
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	state, err := oauth.RandomState()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	lb, err := oauth.StartLoopback("127.0.0.1:0", "/callback")
	if err != nil {
		return adapter.AuthSession{}, fmt.Errorf("poe_oauth: callback listen: %w", err)
	}
	lb.ExpectState(state)
	redirect := "http://" + lb.Addr() + "/callback"
	a.mu.Lock()
	if a.pending != nil && a.pending.lb != nil {
		_ = a.pending.lb.Close()
	}
	a.pending = &poePending{pkce: pkce, state: state, redirect: redirect, lb: lb}
	a.mu.Unlock()
	return adapter.AuthSession{
		Provider: Name,
		LoginURL: AuthorizeURL(redirect, pkce.Challenge, state),
		State:    state,
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = session
	_ = code
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending == nil || pending.lb == nil {
		return fmt.Errorf("poe_oauth: no in-progress login (call AuthStart first)")
	}
	defer func() { _ = pending.lb.Close() }()
	res, err := pending.lb.Wait(ctx)
	if err != nil {
		return err
	}
	if res.Iss != "" && res.Iss != Issuer {
		return fmt.Errorf("poe_oauth: invalid oauth issuer")
	}
	if strings.TrimSpace(res.Code) == "" {
		return fmt.Errorf("poe_oauth: missing authorization code")
	}
	tok, err := a.exchange(ctx, res.Code, pending.redirect, pending.pkce.Verifier)
	if err != nil {
		return err
	}
	return a.storeToken(tok)
}

func (a *Adapter) exchange(ctx context.Context, code, redirect, verifier string) (oauth.Token, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
	}
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oauth.Token{}, fmt.Errorf("poe_oauth: token exchange failed (%d)%s", resp.StatusCode, safeDetail(raw, code, verifier))
	}
	var body struct {
		APIKey    string `json:"api_key"`
		ExpiresIn *int   `json:"api_key_expires_in"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return oauth.Token{}, fmt.Errorf("poe_oauth: token exchange: %w", err)
	}
	key := strings.TrimSpace(body.APIKey)
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return oauth.Token{}, fmt.Errorf("poe_oauth: token response missing api key")
	}
	tok := oauth.Token{AccessToken: key}
	if body.ExpiresIn != nil {
		if *body.ExpiresIn < 0 {
			return oauth.Token{}, fmt.Errorf("poe_oauth: invalid api key expiry")
		}
		sec := int64(*body.ExpiresIn)
		d := time.Duration(sec) * time.Second
		if sec > 0 && d/time.Second != time.Duration(sec) {
			return oauth.Token{}, fmt.Errorf("poe_oauth: invalid api key expiry")
		}
		if sec > 0 {
			tok.ExpiresAt = time.Now().Add(d)
		}
	}
	return tok, nil
}

func safeDetail(raw []byte, secrets ...string) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}
	var body struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	detail := body.Description
	if detail == "" {
		detail = body.Error
	}
	if detail == "" {
		return ""
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(detail, secret) {
			return ""
		}
	}
	if len(detail) > 240 {
		detail = detail[:240] + "..."
	}
	return ": " + detail
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

func (a *Adapter) compat() (*oauthcompat.Tagged, error) {
	a.mu.Lock()
	tok := a.token
	id := a.id
	base := a.apiBase
	a.mu.Unlock()
	if !tok.Valid() {
		return nil, adapter.ErrAuthRequired
	}
	if !tok.ExpiresAt.IsZero() && time.Now().After(tok.ExpiresAt) {
		return nil, fmt.Errorf("poe_oauth: api key expired; sign in again")
	}
	if base == "" {
		base = APIBase
	}
	return oauthcompat.Open(adapter.Options{
		ID: id, BaseURL: base, APIKey: tok.AccessToken, Tier: catalog.TierPaid,
	}, Name)
}

func (a *Adapter) Validate(ctx context.Context) error {
	_, err := a.ListModels(ctx)
	return err
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	inner, err := a.compat()
	if err != nil {
		return nil, err
	}
	return inner.ListModels(ctx)
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	inner, err := a.compat()
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return inner.Chat(ctx, req)
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	inner, err := a.compat()
	if err != nil {
		return err
	}
	return inner.ChatStream(ctx, req, w)
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
