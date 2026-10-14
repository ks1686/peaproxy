// Package kilo_oauth implements Kilo Pass device authorization.
//
// The flow matches the public Kilo gateway client: POST /api/device-auth/codes,
// then GET that code until it is approved. Chat is OpenAI-compatible at
// /api/openrouter. See docs/OAUTH.md.
package kilo_oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
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
	Name    = "kilo_oauth"
	APIRoot = "https://api.kilo.ai"
	// APIBase is the OpenAI-compatible prefix (models and chat/completions).
	APIBase   = APIRoot + "/api/openrouter"
	UserAgent = "peaproxy"
	editor    = "PeaProxy"
)

// kiloEmbedded matches a base URL packed in front of a Kilo token, the form
// the official client accepts: "https://host[:port][/path]:<token>".
var kiloEmbedded = regexp.MustCompile(`^(https?://[^:]+(?::\d+)?(?:/[^:]*)?):`)

// Adapter is a Kilo subscription device-login client.
type Adapter struct {
	id           string
	authBase     string
	apiRoot      string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	pollInterval time.Duration
	mu           sync.Mutex
	token        oauth.Token
	pending      *kiloCode
}

type kiloCode struct {
	code      string
	expiresIn int
}

// New builds the adapter.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{
		id:           id,
		authBase:     APIRoot,
		apiRoot:      APIRoot,
		httpClient:   adapter.HTTPClient(30*time.Second, opts.ObserveHeaders),
		persist:      opts.PersistOAuth,
		token:        opts.OAuth,
		pollInterval: 3 * time.Second,
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

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.authBase+"/api/device-auth/codes", nil)
	if err != nil {
		return adapter.AuthSession{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return adapter.AuthSession{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return adapter.AuthSession{}, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return adapter.AuthSession{}, fmt.Errorf("kilo_oauth: too many pending authorization requests")
	}
	if resp.StatusCode >= 300 {
		return adapter.AuthSession{}, fmt.Errorf("kilo_oauth: device start HTTP %d: %s", resp.StatusCode, truncate(raw))
	}
	var body struct {
		Code            string `json:"code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verificationUrl"`
		ExpiresIn       int    `json:"expiresIn"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return adapter.AuthSession{}, fmt.Errorf("kilo_oauth: device start: %w", err)
	}
	if body.Code == "" || body.VerificationURL == "" {
		return adapter.AuthSession{}, fmt.Errorf("kilo_oauth: device start missing code or verification url")
	}
	user := body.UserCode
	if user == "" {
		user = body.Code
	}
	a.mu.Lock()
	a.pending = &kiloCode{code: body.Code, expiresIn: body.ExpiresIn}
	a.mu.Unlock()
	return adapter.AuthSession{
		Provider:        Name,
		LoginURL:        body.VerificationURL,
		UserCode:        user,
		VerificationURL: body.VerificationURL,
		ExpiresIn:       body.ExpiresIn,
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = session
	_ = code
	a.mu.Lock()
	pending := a.pending
	interval := a.pollInterval
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("kilo_oauth: no in-progress login (call AuthStart first)")
	}
	max := 15 * time.Minute
	if pending.expiresIn > 0 {
		max = time.Duration(pending.expiresIn) * time.Second
	}
	tok, err := oauth.PollDevice(ctx, interval, max, func() (oauth.DevicePollResult, error) {
		return a.poll(ctx, pending.code)
	})
	if err != nil {
		return err
	}
	return a.storeToken(tok)
}

func (a *Adapter) poll(ctx context.Context, code string) (oauth.DevicePollResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.authBase+"/api/device-auth/codes/"+url.PathEscape(code), nil)
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	switch resp.StatusCode {
	case http.StatusAccepted:
		return oauth.DevicePollResult{Pending: true}, nil
	case http.StatusForbidden:
		return oauth.DevicePollResult{Denied: true}, fmt.Errorf("kilo_oauth: authorization denied")
	case http.StatusGone:
		return oauth.DevicePollResult{Expired: true}, fmt.Errorf("kilo_oauth: device code expired")
	case http.StatusOK:
		var body struct {
			Status    string `json:"status"`
			Token     string `json:"token"`
			UserEmail string `json:"userEmail"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return oauth.DevicePollResult{}, fmt.Errorf("kilo_oauth: poll: %w", err)
		}
		switch body.Status {
		case "", "pending":
			if body.Token == "" {
				return oauth.DevicePollResult{Pending: true}, nil
			}
		case "denied":
			return oauth.DevicePollResult{Denied: true}, fmt.Errorf("kilo_oauth: authorization denied")
		case "expired":
			return oauth.DevicePollResult{Expired: true}, fmt.Errorf("kilo_oauth: device code expired")
		case "approved":
			if body.Token == "" {
				return oauth.DevicePollResult{}, fmt.Errorf("kilo_oauth: approved login missing token")
			}
		default:
			if body.Token == "" {
				return oauth.DevicePollResult{}, fmt.Errorf("kilo_oauth: unexpected poll status %q", body.Status)
			}
		}
		return oauth.DevicePollResult{Token: oauth.Token{AccessToken: body.Token, Email: body.UserEmail}}, nil
	default:
		return oauth.DevicePollResult{}, fmt.Errorf("kilo_oauth: poll HTTP %d: %s", resp.StatusCode, truncate(raw))
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

func (a *Adapter) compat() (*oauthcompat.Tagged, error) {
	a.mu.Lock()
	tok := a.token
	id := a.id
	root := a.apiRoot
	a.mu.Unlock()
	if !tok.Valid() {
		return nil, adapter.ErrAuthRequired
	}
	if root == "" {
		root = APIRoot
	}
	return oauthcompat.Open(adapter.Options{
		ID: id, BaseURL: openRouterBaseFrom(root, tok.AccessToken), APIKey: tok.AccessToken, Tier: catalog.TierPaid,
		ExtraHeaders: map[string]string{
			"User-Agent":            userAgent(),
			"X-KILOCODE-EDITORNAME": editor,
		},
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

func userAgent() string {
	if version.Version == "" || version.Version == "dev" {
		return UserAgent
	}
	return UserAgent + "/" + version.Version
}

// openRouterBase is the OpenAI-compatible origin for this token. A token that
// embeds a URL uses that host; otherwise the public Kilo openrouter prefix.
func openRouterBase(token string) string {
	return openRouterBaseFrom(APIRoot, token)
}

func openRouterBaseFrom(root, token string) string {
	raw := root
	if raw == "" {
		raw = APIRoot
	}
	if emb := embeddedKiloBase(token); emb != "" {
		raw = emb
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		u, _ = url.Parse(APIRoot)
	}
	var parts []string
	for _, p := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	api := -1
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "api" {
			api = i
			break
		}
	}
	prefix := parts
	if api >= 0 {
		prefix = parts[:api]
	}
	u.Path = "/" + strings.Join(append(prefix, "api", "openrouter"), "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func embeddedKiloBase(token string) string {
	m := kiloEmbedded.FindStringSubmatch(token)
	if m == nil {
		return ""
	}
	u, err := url.Parse(m[1])
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

func truncate(b []byte) string {
	const n = 240
	b = bytes.TrimSpace(b)
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
