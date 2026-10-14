// Package minimax_oauth implements MiniMax Coding Plan device authorization.
//
// The flow matches the public MiniMax CLI: PKCE device code against
// account.minimax.io, then Anthropic Messages at api.minimax.io/anthropic.
// The China account host is not included. See docs/OAUTH.md.
package minimax_oauth

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
	"github.com/ks1686/peaproxy/internal/adapter/anthropic"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
)

const (
	Name          = "minimax_oauth"
	ClientID      = "659cf4c1-615c-45f6-a5f6-4bf15eb476e5"
	Scope         = "openid profile coding_plan"
	CodeURL       = "https://account.minimax.io/oauth2/device/code"
	TokenURL      = "https://account.minimax.io/oauth2/token"
	InferenceBase = "https://api.minimax.io/anthropic"
	resourceExtra = "resource_url"
	refreshSkew   = 5 * time.Minute
)

// Adapter is a MiniMax Coding Plan device-login client.
type Adapter struct {
	id           string
	codeURL      string
	tokenURL     string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	pollInterval time.Duration
	mu           sync.Mutex
	commitMu     sync.Mutex
	generation   uint64
	token        oauth.Token
	pending      *mmPending
}

type mmPending struct {
	verifier string
	userCode string
	interval time.Duration
	deadline time.Time
}

// New builds the adapter. Only the global MiniMax region is supported.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	return &Adapter{
		id:         id,
		codeURL:    CodeURL,
		tokenURL:   TokenURL,
		httpClient: adapter.HTTPClient(30*time.Second, opts.ObserveHeaders),
		persist:    opts.PersistOAuth,
		token:      opts.OAuth,
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

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	pkce, err := oauth.GeneratePKCE()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	state, err := oauth.RandomState()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	form := url.Values{
		"client_id":             {ClientID},
		"scope":                 {Scope},
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
	}
	raw, status, err := a.postForm(ctx, a.codeURL, form)
	if err != nil {
		return adapter.AuthSession{}, err
	}
	if status >= 300 {
		return adapter.AuthSession{}, fmt.Errorf("minimax_oauth: device start HTTP %d: %s", status, truncate(raw))
	}
	var body struct {
		BaseResp        *baseResp   `json:"base_resp"`
		UserCode        string      `json:"user_code"`
		VerificationURI string      `json:"verification_uri"`
		ExpiredIn       json.Number `json:"expired_in"`
		Interval        int         `json:"interval"`
		State           string      `json:"state"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return adapter.AuthSession{}, fmt.Errorf("minimax_oauth: device start: %w", err)
	}
	if msg := body.BaseResp.err(); msg != "" {
		return adapter.AuthSession{}, fmt.Errorf("minimax_oauth: device start: %s", msg)
	}
	if body.State != state {
		return adapter.AuthSession{}, fmt.Errorf("minimax_oauth: device start state mismatch")
	}
	if body.UserCode == "" || body.VerificationURI == "" {
		return adapter.AuthSession{}, fmt.Errorf("minimax_oauth: device start missing user code or verification url")
	}
	now := time.Now()
	deadline := expiryFrom(numberToInt64(body.ExpiredIn), now)
	if deadline.IsZero() {
		deadline = now.Add(15 * time.Minute)
	}
	interval := 3 * time.Second
	if body.Interval > 0 {
		interval = time.Duration(body.Interval) * time.Millisecond
	}
	a.mu.Lock()
	a.pending = &mmPending{
		verifier: pkce.Verifier,
		userCode: body.UserCode,
		interval: interval,
		deadline: deadline,
	}
	a.mu.Unlock()
	expiresIn := int(time.Until(deadline).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	return adapter.AuthSession{
		Provider:        Name,
		LoginURL:        body.VerificationURI,
		UserCode:        body.UserCode,
		VerificationURL: body.VerificationURI,
		ExpiresIn:       expiresIn,
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = session
	_ = code
	a.mu.Lock()
	pending := a.pending
	override := a.pollInterval
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("minimax_oauth: no in-progress login (call AuthStart first)")
	}
	interval := pending.interval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if override > 0 {
		interval = override
	}
	max := time.Until(pending.deadline)
	if max <= 0 {
		return fmt.Errorf("minimax_oauth: device code expired")
	}
	tok, err := oauth.PollDevice(ctx, interval, max, func() (oauth.DevicePollResult, error) {
		return a.poll(ctx, pending)
	})
	if err != nil {
		return err
	}
	return a.storeToken(tok)
}

func (a *Adapter) poll(ctx context.Context, pending *mmPending) (oauth.DevicePollResult, error) {
	form := url.Values{
		"grant_type":    {oauth.DeviceGrantType},
		"client_id":     {ClientID},
		"user_code":     {pending.userCode},
		"code_verifier": {pending.verifier},
	}
	raw, status, err := a.postForm(ctx, a.tokenURL, form)
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	if status != http.StatusOK {
		return oauth.DevicePollResult{}, fmt.Errorf("minimax_oauth: token HTTP %d: %s", status, truncate(raw))
	}
	tok, pendingPoll, err := parseTokenBody(raw)
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	if pendingPoll {
		return oauth.DevicePollResult{Pending: true}, nil
	}
	return oauth.DevicePollResult{Token: tok}, nil
}

func (a *Adapter) refresh(ctx context.Context, cur oauth.Token) (oauth.Token, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {cur.RefreshToken},
		"client_id":     {ClientID},
	}
	raw, status, err := a.postForm(ctx, a.tokenURL, form)
	if err != nil {
		return oauth.Token{}, err
	}
	if status >= 400 && status < 500 {
		return oauth.Token{}, fmt.Errorf("minimax_oauth: session expired; sign in again (HTTP %d)", status)
	}
	if status != http.StatusOK {
		return oauth.Token{}, fmt.Errorf("minimax_oauth: refresh HTTP %d: %s", status, truncate(raw))
	}
	tok, pendingPoll, err := parseTokenBody(raw)
	if err != nil {
		return oauth.Token{}, err
	}
	if pendingPoll || tok.AccessToken == "" {
		return oauth.Token{}, fmt.Errorf("minimax_oauth: refresh missing access token")
	}
	return tok.WithFallbackRefresh(cur.RefreshToken), nil
}

func (a *Adapter) ensureToken(ctx context.Context) error {
	a.mu.Lock()
	tok := a.token
	a.mu.Unlock()
	if !tok.NeedsRefresh(refreshSkew) {
		if !tok.Valid() {
			return adapter.ErrAuthRequired
		}
		return nil
	}
	if tok.RefreshToken == "" {
		return adapter.ErrAuthRequired
	}
	return oauth.Ensure(ctx, &oauth.DefaultRefresh, "minimax:"+a.id, &a.commitMu, &a.mu, &a.token, &a.generation,
		refreshSkew,
		func(ctx context.Context, tok oauth.Token) (oauth.Token, error) {
			return a.refresh(ctx, tok)
		},
		func(old, next oauth.Token) oauth.Token { return next.KeepExtra(old) },
		a.persist)
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

type messagesAPI interface {
	adapter.Adapter
	Messages(ctx context.Context, raw []byte) ([]byte, error)
	MessagesStream(ctx context.Context, raw []byte, w io.Writer) error
}

func (a *Adapter) wire(ctx context.Context) (messagesAPI, error) {
	if err := a.ensureToken(ctx); err != nil {
		return nil, err
	}
	a.mu.Lock()
	tok := a.token
	id := a.id
	a.mu.Unlock()
	if !tok.Valid() {
		return nil, adapter.ErrAuthRequired
	}
	inner, err := anthropic.New(adapter.Options{
		ID:      id,
		BaseURL: inferenceBase(tok.ExtraGet(resourceExtra)),
		APIKey:  tok.AccessToken,
	})
	if err != nil {
		return nil, err
	}
	wired, ok := inner.(messagesAPI)
	if !ok {
		return nil, fmt.Errorf("minimax_oauth: anthropic client")
	}
	return wired, nil
}

func (a *Adapter) Validate(ctx context.Context) error {
	_, err := a.ListModels(ctx)
	return err
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	inner, err := a.wire(ctx)
	if err != nil {
		return nil, err
	}
	models, err := inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	for i := range models {
		models[i].SubscriptionOAuth = true
		models[i].Provider = Name
		models[i].AccountID = a.id
		models[i].Tier = catalog.TierPaid
	}
	return models, nil
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	inner, err := a.wire(ctx)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return inner.Chat(ctx, req)
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	inner, err := a.wire(ctx)
	if err != nil {
		return err
	}
	return inner.ChatStream(ctx, req, w)
}

func (a *Adapter) Messages(ctx context.Context, raw []byte) ([]byte, error) {
	inner, err := a.wire(ctx)
	if err != nil {
		return nil, err
	}
	return inner.Messages(ctx, raw)
}

func (a *Adapter) MessagesStream(ctx context.Context, raw []byte, w io.Writer) error {
	inner, err := a.wire(ctx)
	if err != nil {
		return err
	}
	return inner.MessagesStream(ctx, raw, w)
}

func (a *Adapter) postForm(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

type baseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

func (b *baseResp) err() string {
	if b == nil || b.StatusCode == 0 {
		return ""
	}
	if b.StatusMsg != "" {
		return b.StatusMsg
	}
	return fmt.Sprintf("status %d", b.StatusCode)
}

func parseTokenBody(raw []byte) (oauth.Token, bool, error) {
	var body struct {
		BaseResp     *baseResp   `json:"base_resp"`
		Status       string      `json:"status"`
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		ExpiredIn    json.Number `json:"expired_in"`
		ResourceURL  string      `json:"resource_url"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return oauth.Token{}, false, fmt.Errorf("minimax_oauth: token: %w", err)
	}
	if body.Status == "pending" {
		return oauth.Token{}, true, nil
	}
	if msg := body.BaseResp.err(); msg != "" && body.Status != "success" {
		return oauth.Token{}, false, fmt.Errorf("minimax_oauth: %s", msg)
	}
	if body.Status != "success" || body.AccessToken == "" {
		if body.Status == "" {
			return oauth.Token{}, false, fmt.Errorf("minimax_oauth: token missing status")
		}
		return oauth.Token{}, false, fmt.Errorf("minimax_oauth: token status %q", body.Status)
	}
	tok := oauth.Token{
		AccessToken:  body.AccessToken,
		RefreshToken: body.RefreshToken,
		ExpiresAt:    expiryFrom(numberToInt64(body.ExpiredIn), time.Now()),
	}
	if body.ResourceURL != "" {
		tok = tok.WithExtra(resourceExtra, body.ResourceURL)
	}
	return tok, false, nil
}

// expiryFrom reads MiniMax expired_in. Values of at least 1e12 are unix
// milliseconds. Smaller positive values are a TTL in seconds.
func expiryFrom(raw int64, now time.Time) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	if raw >= 1_000_000_000_000 {
		return time.UnixMilli(raw)
	}
	return now.Add(time.Duration(raw) * time.Second)
}

func numberToInt64(n json.Number) int64 {
	if n == "" {
		return 0
	}
	v, err := n.Int64()
	if err == nil {
		return v
	}
	f, ferr := n.Float64()
	if ferr != nil {
		return 0
	}
	return int64(f)
}

// inferenceBase is the Anthropic Messages origin. resource_url from the token
// endpoint is the API host; the CLI appends /anthropic. A value that already
// ends in /anthropic is used as-is.
func inferenceBase(resource string) string {
	resource = strings.TrimRight(strings.TrimSpace(resource), "/")
	if resource == "" {
		return InferenceBase
	}
	lower := strings.ToLower(resource)
	if strings.HasSuffix(lower, "/anthropic") || strings.HasSuffix(lower, "/anthropic/v1") {
		return resource
	}
	return resource + "/anthropic"
}

func truncate(b []byte) string {
	const n = 240
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

var _ adapter.Adapter = (*Adapter)(nil)
var _ adapter.Authenticator = (*Adapter)(nil)
var _ adapter.NativeMessages = (*Adapter)(nil)
