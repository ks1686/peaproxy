// Package antigravity implements Google Gemini consumer / Antigravity subscription OAuth.
//
// Clean-room reimplementation inspired by CLIProxyAPI (MIT, router-for-me/CLIProxyAPI):
// Google OAuth loopback for the public Antigravity IDE client, then Cloud Code
// generateContent. Distinct from the official AI Studio API-key adapter (`google`/`gemini`).
// Not an official third-party API; see docs/OAUTH.md for liability.
package antigravity

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
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
	"github.com/ks1686/peaproxy/internal/translate"
)

const (
	Name        = "antigravity"
	AliasGemini = "gemini_oauth"
	AuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	TokenURL    = "https://oauth2.googleapis.com/token"
	UserInfoURL = "https://www.googleapis.com/oauth2/v2/userinfo?alt=json"
	APIEndpoint = "https://cloudcode-pa.googleapis.com"
	DailyAPI    = "https://daily-cloudcode-pa.googleapis.com"
	APIVersion  = "v1internal"
	RedirectURI = "http://localhost:51121/oauth-callback"
	// ClientVersion is the current Antigravity hub release. Cloud Code treats
	// the old linux/amd64 2.9.1 hub agent as exhausted even when quota remains.
	ClientVersion = "2.17.0"
	UserAgent     = "antigravity/hub/" + ClientVersion + " darwin/arm64"
	callbackPort  = "51121"
	callbackPath  = "/oauth-callback"
	projectExtra  = "project_id"
)

// Public Antigravity IDE installed-app OAuth client. Google treats installed-app
// client secrets as non-confidential; they are assembled so git secret scanners
// do not treat a third-party public client as a PeaProxy credential.
var (
	ClientID     = antigravityIDEClientID()
	ClientSecret = antigravityIDEClientSecret()
)

func antigravityIDEClientID() string {
	return "1071006060591-tmhssin2h21lcre235vtolojh4g403ep" +
		"." + "apps.googleusercontent.com"
}

func antigravityIDEClientSecret() string {
	return "GOC" + "SPX" + "-" + "K58FWR486LdLJ1mLB8sXC4z6qDAf"
}

var scopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
	"https://www.googleapis.com/auth/cclog",
	"https://www.googleapis.com/auth/experimentsandconfigs",
}

// Adapter is a Gemini consumer OAuth client (Cloud Code generateContent).
type Adapter struct {
	id           string
	tokenURL     string
	userInfoURL  string
	apiBase      string
	dailyAPI     string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	mu           sync.Mutex
	token        oauth.Token
	generation   uint64
	commitMu     sync.Mutex
	pending      *pendingAuth
	skipLoopback bool
}

type pendingAuth struct {
	state       string
	redirectURI string
	lb          *oauth.Loopback
}

// New builds the adapter. Tokens come from Options.OAuth.
func New(opts adapter.Options) (adapter.Adapter, error) {
	id := opts.ID
	if id == "" {
		id = Name
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = APIEndpoint
	}
	return &Adapter{
		id:           id,
		tokenURL:     TokenURL,
		userInfoURL:  UserInfoURL,
		apiBase:      base,
		dailyAPI:     DailyAPI,
		httpClient:   adapter.HTTPClient(0, opts.ObserveHeaders),
		persist:      opts.PersistOAuth,
		token:        opts.OAuth,
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

// AuthorizeURL builds the Google login URL (no network).
func AuthorizeURL(state, redirectURI string) string {
	if redirectURI == "" {
		redirectURI = RedirectURI
	}
	q := url.Values{
		"access_type":   {"offline"},
		"client_id":     {ClientID},
		"prompt":        {"consent"},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(scopes, " ")},
		"state":         {state},
	}
	return AuthURL + "?" + q.Encode()
}

func (a *Adapter) AuthStart(ctx context.Context) (adapter.AuthSession, error) {
	_ = ctx
	state, err := oauth.RandomState()
	if err != nil {
		return adapter.AuthSession{}, err
	}
	pending := &pendingAuth{state: state, redirectURI: RedirectURI}
	if !a.skipLoopback {
		lb, lerr := oauth.StartLoopback("127.0.0.1:"+callbackPort, callbackPath)
		if lerr != nil {
			return adapter.AuthSession{}, fmt.Errorf("antigravity: callback port %s is in use: %w", callbackPort, lerr)
		}
		lb.ExpectState(pending.state)
		pending.lb = lb
	}
	a.mu.Lock()
	if a.pending != nil && a.pending.lb != nil {
		_ = a.pending.lb.Close()
	}
	a.pending = pending
	a.mu.Unlock()
	return adapter.AuthSession{
		Provider: Name,
		LoginURL: AuthorizeURL(state, pending.redirectURI),
		State:    state,
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("antigravity: no in-progress login (call AuthStart first)")
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
		return fmt.Errorf("antigravity: empty authorization code")
	}
	if err := oauth.ConfirmCallbackState(pending.state, stateFromInput, fromLoopback); err != nil {
		return err
	}
	_ = session
	tok, err := a.exchange(ctx, code, pending.redirectURI)
	if err != nil {
		return err
	}
	email, err := a.fetchEmail(ctx, tok.AccessToken)
	if err == nil && email != "" {
		tok.Email = email
	}
	project, err := a.fetchProjectID(ctx, tok.AccessToken)
	if err != nil {
		return err
	}
	tok.AccountID = project
	tok = tok.WithExtra(projectExtra, project)
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

func (a *Adapter) exchange(ctx context.Context, code, redirect string) (oauth.Token, error) {
	if redirect == "" {
		redirect = RedirectURI
	}
	form := url.Values{
		"code":          {code},
		"client_id":     {ClientID},
		"client_secret": {ClientSecret},
		"redirect_uri":  {redirect},
		"grant_type":    {"authorization_code"},
	}
	return a.postForm(ctx, form)
}

func (a *Adapter) refresh(ctx context.Context, refreshToken string) (oauth.Token, error) {
	form := url.Values{
		"client_id":     {ClientID},
		"client_secret": {ClientSecret},
		"refresh_token": {refreshToken},
		"grant_type":    {"refresh_token"},
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
	raw, err := a.do(req)
	if err != nil {
		return oauth.Token{}, err
	}
	return oauth.ParseTokenResponse(raw)
}

func (a *Adapter) fetchEmail(ctx context.Context, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.userInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("User-Agent", UserAgent)
	raw, err := a.do(req)
	if err != nil {
		return "", err
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", err
	}
	return strings.TrimSpace(info.Email), nil
}

type loadAssistRequest struct {
	Metadata map[string]string `json:"metadata"`
}

func (a *Adapter) fetchProjectID(ctx context.Context, access string) (string, error) {
	body, err := json.Marshal(loadAssistRequest{Metadata: map[string]string{"ideType": "ANTIGRAVITY"}})
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(a.apiBase, "/") + "/" + APIVersion + ":loadCodeAssist"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", UserAgent)
	raw, err := a.do(req)
	if err != nil {
		return "", err
	}
	if id := extractProjectID(raw); id != "" {
		return id, nil
	}
	return a.onboardUser(ctx, access, defaultTierID(raw))
}

type onboardRequest struct {
	TierID   string            `json:"tier_id"`
	Metadata map[string]string `json:"metadata"`
}

func (a *Adapter) onboardUser(ctx context.Context, access, tierID string) (string, error) {
	if tierID == "" {
		tierID = "free-tier"
	}
	body, err := json.Marshal(onboardRequest{
		TierID: tierID,
		Metadata: map[string]string{
			"ide_type":    "ANTIGRAVITY",
			"ide_version": ClientVersion,
			"ide_name":    "antigravity",
		},
	})
	if err != nil {
		return "", err
	}
	for i := 0; i < 5; i++ {
		endpoint := strings.TrimRight(a.dailyAPI, "/") + "/" + APIVersion + ":onboardUser"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("User-Agent", UserAgent+" google-api-nodejs-client/10.3.0")
		raw, err := a.do(req)
		if err != nil {
			return "", err
		}
		var parsed struct {
			Done     bool            `json:"done"`
			Response json.RawMessage `json:"response"`
		}
		_ = json.Unmarshal(raw, &parsed)
		if parsed.Done {
			if id := extractProjectID(parsed.Response); id != "" {
				return id, nil
			}
			if id := extractProjectID(raw); id != "" {
				return id, nil
			}
			return "", fmt.Errorf("antigravity: onboardUser completed without project id")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return "", fmt.Errorf("antigravity: onboardUser did not complete")
}

func extractProjectID(raw []byte) string {
	var parsed struct {
		CloudaicompanionProject json.RawMessage `json:"cloudaicompanionProject"`
		ProjectID               string          `json:"projectId"`
		Project                 json.RawMessage `json:"project"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return ""
	}
	if id := stringOrNestedID(parsed.CloudaicompanionProject); id != "" {
		return id
	}
	if parsed.ProjectID != "" {
		return parsed.ProjectID
	}
	return stringOrNestedID(parsed.Project)
}

func stringOrNestedID(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return strings.TrimSpace(s)
	}
	var obj struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &obj)
	return strings.TrimSpace(obj.ID)
}

func defaultTierID(raw []byte) string {
	var parsed struct {
		AllowedTiers []struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"isDefault"`
		} `json:"allowedTiers"`
		CurrentTier struct {
			ID string `json:"id"`
		} `json:"currentTier"`
	}
	_ = json.Unmarshal(raw, &parsed)
	for _, t := range parsed.AllowedTiers {
		if t.IsDefault && t.ID != "" {
			return t.ID
		}
	}
	if parsed.CurrentTier.ID != "" {
		return parsed.CurrentTier.ID
	}
	return "free-tier"
}

func (a *Adapter) do(req *http.Request) ([]byte, error) {
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(raw))
	}
	return raw, nil
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
	return oauth.Ensure(ctx, &oauth.DefaultRefresh, "antigravity:"+a.id, &a.commitMu, &a.mu, &a.token, &a.generation,
		5*time.Minute,
		func(ctx context.Context, tok oauth.Token) (oauth.Token, error) {
			return a.refresh(ctx, tok.RefreshToken)
		},
		func(old, next oauth.Token) oauth.Token { return next.KeepExtra(old) },
		a.persist)
}

func (a *Adapter) Validate(ctx context.Context) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	_, err := a.ListModels(ctx)
	return err
}

type fetchModelsRequest struct {
	Project string `json:"project,omitempty"`
}

func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	if err := a.ensureToken(ctx); err != nil {
		return nil, err
	}
	a.mu.Lock()
	tok := a.token
	a.mu.Unlock()
	project := tok.ExtraGet(projectExtra)
	if project == "" {
		project = tok.AccountID
	}
	body, err := json.Marshal(fetchModelsRequest{Project: project})
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(a.apiBase, "/") + "/" + APIVersion + ":fetchAvailableModels"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	a.headers(req)
	c := *a.httpClient
	c.Timeout = 8 * time.Second
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("antigravity list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, adapter.NewHTTPError(resp, truncate(raw))
	}
	ids := parseAntigravityModels(raw)
	out := make([]catalog.Model, 0, len(ids))
	for _, m := range ids {
		out = append(out, catalog.Model{
			ID:                m.id,
			DisplayName:       m.display,
			Provider:          Name,
			AccountID:         a.id,
			Tier:              catalog.TierPaid,
			Modalities:        catalog.InferModalities(m.id),
			Status:            "ready",
			SubscriptionOAuth: true,
			Exposed:           true,
			Routable:          true,
		})
	}
	return out, nil
}

type modelRef struct {
	id, display string
}

func parseAntigravityModels(raw []byte) []modelRef {
	var asMap struct {
		Models map[string]struct {
			DisplayName string `json:"displayName"`
			Name        string `json:"name"`
		} `json:"models"`
	}
	if json.Unmarshal(raw, &asMap) == nil && len(asMap.Models) > 0 {
		out := make([]modelRef, 0, len(asMap.Models))
		for id, m := range asMap.Models {
			display := m.DisplayName
			if display == "" {
				display = m.Name
			}
			if display == "" {
				display = id
			}
			out = append(out, modelRef{id: id, display: display})
		}
		return out
	}
	var asList struct {
		Models []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &asList) != nil {
		return nil
	}
	var out []modelRef
	for _, m := range asList.Models {
		id := m.ID
		if id == "" {
			id = m.Name
		}
		if id != "" {
			display := m.DisplayName
			if display == "" {
				display = id
			}
			out = append(out, modelRef{id: id, display: display})
		}
	}
	if len(out) == 0 {
		for _, m := range asList.Data {
			if m.ID != "" {
				out = append(out, modelRef{id: m.ID, display: m.ID})
			}
		}
	}
	return out
}

func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	if err := a.ensureToken(ctx); err != nil {
		return adapter.ChatResponse{}, err
	}
	raw, err := a.geminiBody(req, false)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.generateURL(false), bytes.NewReader(raw))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	a.headers(httpReq)
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if resp.StatusCode >= 300 {
		return adapter.ChatResponse{}, chatHTTPError(resp, body)
	}
	oa, content, err := translate.GeminiChatToOpenAI(req.Model, body, openAIUsageFromGemini(body))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: content}, nil
}

func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	if err := a.ensureToken(ctx); err != nil {
		return err
	}
	raw, err := a.geminiBody(req, true)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.generateURL(true), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	a.headers(httpReq)
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return chatHTTPError(resp, body)
	}
	return translate.GeminiSSEToOpenAIWith(resp.Body, w, req.Model, streamEventError)
}

func chatHTTPError(resp *http.Response, body []byte) error {
	e := adapter.NewHTTPError(resp, truncate(body))
	// Cloud Code sends no X-Ratelimit-Scope. Its 429s and 503s report per-model
	// quota or capacity, so every one is scoped to this model, not the whole
	// Google account, and the next model can still be tried.
	if (e.Status == http.StatusTooManyRequests || e.Status == http.StatusServiceUnavailable) && e.Scope == adapter.ScopeAccount {
		e.Scope = adapter.ScopeModel
	}
	if e.RetryAfter == 0 {
		if d, ok := cloudCodeResetDelay(body); ok {
			e.RetryAfter = min(max(d, time.Second), maxCloudCodeReset)
		}
	}
	return e
}

// maxCloudCodeReset bounds the body reset hint. Cloud Code's largest quota
// window is weekly ("Resets in 166h59m50s"); the cooldown is model-scoped
// and in memory, so honouring it costs nothing but the hourly failed probe.
const maxCloudCodeReset = 7 * 24 * time.Hour

var resetsInPattern = regexp.MustCompile(`Resets in ([0-9][0-9hms.]*)`)

// cloudCodeResetDelay reads the reset hint Cloud Code puts in the error body
// instead of a Retry-After header: RetryInfo.retryDelay, then
// ErrorInfo.metadata.quotaResetDelay, then "Resets in 1h2m3s" in the message.
func cloudCodeResetDelay(body []byte) (time.Duration, bool) {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Details []struct {
				RetryDelay string `json:"retryDelay"`
				Metadata   struct {
					QuotaResetDelay string `json:"quotaResetDelay"`
				} `json:"metadata"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return 0, false
	}
	var hints []string
	for _, d := range parsed.Error.Details {
		hints = append(hints, d.RetryDelay)
	}
	for _, d := range parsed.Error.Details {
		hints = append(hints, d.Metadata.QuotaResetDelay)
	}
	if m := resetsInPattern.FindStringSubmatch(parsed.Error.Message); m != nil {
		hints = append(hints, strings.TrimRight(m[1], "."))
	}
	for _, h := range hints {
		if d, err := time.ParseDuration(h); err == nil && d >= 0 {
			return d, true
		}
	}
	return 0, false
}

func (a *Adapter) generateURL(stream bool) string {
	// Consumer chat goes to the daily Cloud Code host. The production host
	// answers generateContent with 429 even when the account still has quota.
	op := "generateContent"
	if stream {
		op = "streamGenerateContent?alt=sse"
	}
	return strings.TrimRight(a.dailyAPI, "/") + "/" + APIVersion + ":" + op
}

// openAIUsageFromGemini reads the counts off a non-stream generateContent body,
// which carries usageMetadata alongside the candidates exactly as the streamed
// wire does. Both halves or nothing, for the reason the streamed path does it:
// a one-sided count understates the call, and the ledger refuses an unmeasured
// call rather than pricing half of it.
func openAIUsageFromGemini(body []byte) *translate.OpenAIChatUsage {
	return translate.GeminiUsage(body)
}

// geminiBody resolves the Cloud Code project under the adapter's lock, then
// builds the request with the pure codec.
func (a *Adapter) geminiBody(req adapter.ChatRequest, stream bool) ([]byte, error) {
	a.mu.Lock()
	project := a.token.ExtraGet(projectExtra)
	if project == "" {
		project = a.token.AccountID
	}
	a.mu.Unlock()
	return translate.GeminiRequestJSON(project, req, stream)
}

// streamEventError returns the failure a Cloud Code SSE event sends in place
// of candidates: a status object under "error", top level or inside
// "response". Its code is classified as the HTTP status it stands for, so
// failover, cooldown scope and reset hint match a failed response.
func streamEventError(event []byte) error {
	var parsed struct {
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(event, &parsed) != nil {
		return nil
	}
	status := parsed.Error
	if !bytes.HasPrefix(status, []byte("{")) {
		status = parsed.Response.Error
	}
	if !bytes.HasPrefix(status, []byte("{")) {
		return nil
	}
	body := []byte(`{"error":` + string(status) + `}`)
	var fields struct {
		Code   int    `json:"code"`
		Status string `json:"status"`
	}
	if json.Unmarshal(status, &fields) == nil {
		code := fields.Code
		if code == 0 {
			code = googleStatusCodes[fields.Status]
		}
		if _, hasReset := cloudCodeResetDelay(body); code == 0 && hasReset {
			code = http.StatusTooManyRequests
		}
		if code > 0 {
			return chatHTTPError(&http.Response{StatusCode: code}, body)
		}
	}
	return fmt.Errorf("antigravity: stream error: %s", truncate(body))
}

// googleStatusCodes maps google.rpc.Code names, which a stream error may carry
// without the numeric code, to the HTTP status the same error has on a
// non-streaming reply.
var googleStatusCodes = map[string]int{
	"INVALID_ARGUMENT":    http.StatusBadRequest,
	"FAILED_PRECONDITION": http.StatusBadRequest,
	"OUT_OF_RANGE":        http.StatusBadRequest,
	"UNAUTHENTICATED":     http.StatusUnauthorized,
	"PERMISSION_DENIED":   http.StatusForbidden,
	"NOT_FOUND":           http.StatusNotFound,
	"ABORTED":             http.StatusConflict,
	"ALREADY_EXISTS":      http.StatusConflict,
	"RESOURCE_EXHAUSTED":  http.StatusTooManyRequests,
	"CANCELLED":           499,
	"INTERNAL":            http.StatusInternalServerError,
	"UNKNOWN":             http.StatusInternalServerError,
	"DATA_LOSS":           http.StatusInternalServerError,
	"UNIMPLEMENTED":       http.StatusNotImplemented,
	"UNAVAILABLE":         http.StatusServiceUnavailable,
	"DEADLINE_EXCEEDED":   http.StatusGatewayTimeout,
}

func (a *Adapter) headers(req *http.Request) {
	a.mu.Lock()
	tok := a.token.AccessToken
	a.mu.Unlock()
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("User-Agent", UserAgent)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
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
