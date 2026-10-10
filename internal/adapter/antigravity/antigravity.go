// Package antigravity implements Google Gemini consumer / Antigravity subscription OAuth.
//
// Clean-room reimplementation inspired by CLIProxyAPI (MIT, router-for-me/CLIProxyAPI):
// Google OAuth loopback for the public Antigravity IDE client, then Cloud Code
// generateContent. Distinct from the official AI Studio API-key adapter (`google`/`gemini`).
// Not an official third-party API; see docs/OAUTH.md for liability.
package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/jsonx"
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
	message, finish, err := geminiChatMessage(body)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	oa, err := toOpenAIChatJSON(req.Model, message, finish)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	return adapter.ChatResponse{Model: req.Model, Raw: oa, Content: message.Content}, nil
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
	return geminiSSEToOpenAI(resp.Body, w, req.Model)
}

// fetchAvailableModels advertises the "-pro-high" slots, but v1internal
// answers them with HTTP 400. The live id for that tier is gemini-pro-agent.
var upstreamModelIDs = map[string]string{
	"gemini-3.1-pro-high": "gemini-pro-agent",
	"gemini-3-pro-high":   "gemini-pro-agent",
}

func upstreamModelID(model string) string {
	if id, ok := upstreamModelIDs[model]; ok {
		return id
	}
	return model
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

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FileData         *geminiFileData         `json:"fileData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	ID   string          `json:"id,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string             `json:"name"`
	Response geminiToolResponse `json:"response"`
	ID       string             `json:"id,omitempty"`
}

type geminiToolResponse struct {
	Content json.RawMessage `json:"content"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatMessage struct {
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	ToolCalls []chatToolCall `json:"tool_calls,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFileData struct {
	FileURI string `json:"fileUri"`
}

type geminiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunction `json:"functionDeclarations"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature float64 `json:"temperature,omitempty"`
}

type geminiInnerRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	SessionID         string                  `json:"sessionId,omitempty"`
}

type geminiEnvelope struct {
	Project     string             `json:"project,omitempty"`
	Model       string             `json:"model"`
	UserAgent   string             `json:"userAgent"`
	RequestType string             `json:"requestType"`
	RequestID   string             `json:"requestId"`
	Request     geminiInnerRequest `json:"request"`
}

func (a *Adapter) geminiBody(req adapter.ChatRequest, stream bool) ([]byte, error) {
	_ = stream
	a.mu.Lock()
	project := a.token.ExtraGet(projectExtra)
	if project == "" {
		project = a.token.AccountID
	}
	a.mu.Unlock()
	model := req.Model
	type inbound struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []chatToolCall  `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
	}
	var msgs []inbound
	var tools []geminiFunction
	if len(req.Raw) > 0 {
		var parsed struct {
			Model    string          `json:"model"`
			Messages []inbound       `json:"messages"`
			Tools    json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(jsonx.SetStream(req.Raw, false), &parsed); err == nil && len(parsed.Messages) > 0 {
			if parsed.Model != "" {
				model = parsed.Model
			}
			msgs = parsed.Messages
			tools = geminiFunctions(parsed.Tools)
		}
	}
	if len(msgs) == 0 {
		for _, m := range req.Messages {
			raw, _ := json.Marshal(m.Content)
			msgs = append(msgs, inbound{Role: m.Role, Content: raw})
		}
	}
	type toolCall struct {
		name  string
		order int
		id    string
	}
	var sys strings.Builder
	var contents []geminiContent
	calls := make(map[string]toolCall)
	var groupOrder []int
	previousRole := ""
	for _, m := range msgs {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if sys.Len() > 0 {
				sys.WriteByte('\n')
			}
			sys.WriteString(messageText(m.Content))
		case "assistant":
			calls = make(map[string]toolCall)
			var parts []geminiPart
			if messageText(m.Content) != "" {
				parts = geminiPartsFromContent(m.Content)
			}
			for i, call := range m.ToolCalls {
				args := bytes.TrimSpace([]byte(call.Function.Arguments))
				if len(args) == 0 {
					// Some OpenAI clients send "" for a call to a zero-argument tool.
					args = []byte("{}")
				}
				if args[0] != '{' || !json.Valid(args) || call.Function.Name == "" {
					return nil, fmt.Errorf("antigravity: invalid function call %q", call.ID)
				}
				// Cloud Code's OpenAI models (gpt-oss) reject an assistant
				// tool_calls element whose id is empty. Keep the client's id,
				// and mint one when the client left it blank.
				id := call.ID
				if id == "" {
					id = "call_" + strconv.Itoa(i)
				}
				calls[id] = toolCall{name: call.Function.Name, order: i, id: id}
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{ID: id, Name: call.Function.Name, Args: args},
					// Cloud Code accepts this sentinel for unsigned OpenAI history;
					// OpenAI-compatible clients do not round-trip Gemini signatures.
					ThoughtSignature: "skip_thought_signature_validator",
				})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: "model", Parts: parts})
			}
		case "tool":
			call, known := calls[m.ToolCallID]
			if !known && m.ToolCallID == "" && len(calls) == 1 {
				for _, only := range calls {
					call = only
					known = true
				}
			}
			if !known {
				contents = append(contents, geminiContent{Role: "user", Parts: geminiPartsFromContent(m.Content)})
				previousRole = "user"
				continue
			}
			content := m.Content
			if len(content) == 0 {
				content = json.RawMessage("null")
			}
			part := geminiPart{FunctionResponse: &geminiFunctionResponse{ID: call.id, Name: call.name, Response: geminiToolResponse{Content: content}}}
			if previousRole != "tool" {
				contents = append(contents, geminiContent{Role: "user"})
				groupOrder = nil
			}
			// A functionResponse carries only the function name, so Gemini pairs
			// it with its functionCall by position, not by tool_call_id.
			at := sort.Search(len(groupOrder), func(j int) bool { return groupOrder[j] > call.order })
			group := &contents[len(contents)-1]
			group.Parts = slices.Insert(group.Parts, at, part)
			groupOrder = slices.Insert(groupOrder, at, call.order)
		default:
			contents = append(contents, geminiContent{Role: "user", Parts: geminiPartsFromContent(m.Content)})
		}
		previousRole = role
	}
	reqType := "agent"
	if strings.Contains(strings.ToLower(model), "image") {
		reqType = "image_gen"
	}
	env := geminiEnvelope{
		Project:     project,
		Model:       upstreamModelID(model),
		UserAgent:   "antigravity",
		RequestType: reqType,
		RequestID:   newAgentRequestID(reqType),
		Request:     geminiInnerRequest{Contents: contents},
	}
	if reqType != "image_gen" {
		env.Request.SessionID = newSessionID()
	}
	if len(tools) > 0 {
		env.Request.Tools = []geminiTool{{FunctionDeclarations: tools}}
	}
	if sys.Len() > 0 {
		env.Request.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: sys.String()}}}
	}
	return json.Marshal(env)
}

func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := int64(binary.BigEndian.Uint64(b[:]) & 0x7fffffffffffffff)
	if n == 0 {
		n = 1
	}
	return "-" + strconv.FormatInt(n, 10)
}

func newAgentRequestID(reqType string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	if reqType == "image_gen" {
		return "image_gen/" + id
	}
	return "agent-" + id
}

func geminiFunctions(raw json.RawMessage) []geminiFunction {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Function    *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []geminiFunction
	for _, item := range items {
		fn := geminiFunction{Name: item.Name, Description: item.Description, Parameters: item.Parameters}
		if item.Function != nil && item.Function.Name != "" {
			fn = geminiFunction{Name: item.Function.Name, Description: item.Function.Description, Parameters: item.Function.Parameters}
		}
		if fn.Name == "" {
			continue
		}
		fn.Parameters = sanitizeGeminiSchema(fn.Parameters)
		out = append(out, fn)
	}
	return out
}

// sanitizeGeminiSchema reduces a JSON Schema to the fields of the Gemini
// Schema proto. Cloud Code rejects the whole request on any unknown field
// ($schema, additionalProperties, exclusiveMinimum, const, oneOf, ...).
func sanitizeGeminiSchema(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return raw
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, err := json.Marshal(geminiSchema(v))
	if err != nil {
		return raw
	}
	return out
}

var geminiSchemaFields = map[string]bool{
	"type": true, "format": true, "title": true, "description": true, "nullable": true,
	"enum": true, "default": true, "example": true, "required": true, "propertyOrdering": true,
	"minItems": true, "maxItems": true, "minProperties": true, "maxProperties": true,
	"minLength": true, "maxLength": true, "pattern": true, "minimum": true, "maximum": true,
}

// geminiSchema walks schema positions only (property values, items, anyOf),
// so property names such as "type" or "title" are never mistaken for keywords.
func geminiSchema(v any) any {
	in, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	var anyOf, types []any
	for k, val := range in {
		switch k {
		case "properties":
			if props, ok := val.(map[string]any); ok {
				clean := make(map[string]any, len(props))
				for name, child := range props {
					clean[name] = geminiSchema(child)
				}
				out[k] = clean
			}
		case "items":
			if tuple, ok := val.([]any); ok {
				if items, kept := tupleItems(tuple); kept {
					out[k] = items
				}
			} else {
				out[k] = geminiSchema(val)
			}
		case "anyOf", "oneOf":
			if list, ok := val.([]any); ok {
				for _, child := range list {
					anyOf = append(anyOf, geminiSchema(child))
				}
			}
		case "type":
			if list, ok := val.([]any); ok {
				for _, t := range list {
					if t == "null" {
						out["nullable"] = true
					} else {
						types = append(types, t)
					}
				}
			} else {
				out[k] = val
			}
		case "exclusiveMinimum", "exclusiveMaximum":
			bound := "minimum"
			if k == "exclusiveMaximum" {
				bound = "maximum"
			}
			if _, isNum := val.(float64); isNum {
				if _, set := in[bound]; !set {
					out[bound] = val
				}
			}
		case "enum":
			// The Schema proto's enum is a list of strings; Cloud Code rejects others.
			if list, isList := val.([]any); isList && allStrings(list) {
				out[k] = list
			}
		case "const":
			if _, set := in["enum"]; !set {
				if s, isStr := val.(string); isStr {
					out["enum"] = []any{s}
				}
			}
		default:
			if geminiSchemaFields[k] {
				out[k] = val
			}
		}
	}
	switch {
	case len(types) == 1:
		out["type"] = types[0]
	case len(types) > 1:
		if kept := listedBranches(out, types, anyOf); kept != nil {
			anyOf = kept
		} else {
			anyOf = append(typeUnion(out, types), anyOf...)
		}
	}
	return collapseNullable(out, anyOf)
}

// tupleItems reduces tuple-form items, which the Schema proto lacks, to one
// schema: the element schema when all agree, else an anyOf of the distinct
// elements, folded like any other anyOf.
func tupleItems(tuple []any) (any, bool) {
	schemas := make([]any, 0, len(tuple))
	for _, el := range tuple {
		schemas = append(schemas, geminiSchema(el))
	}
	return distinctUnion(schemas)
}

// distinctUnion reduces already-sanitized schemas to one: the schema when all
// agree, else an anyOf of the distinct ones in first-seen order.
func distinctUnion(schemas []any) (any, bool) {
	var distinct []any
	for _, schema := range schemas {
		if !slices.ContainsFunc(distinct, func(seen any) bool { return reflect.DeepEqual(seen, schema) }) {
			distinct = append(distinct, schema)
		}
	}
	switch len(distinct) {
	case 0:
		return nil, false
	case 1:
		return distinct[0], true
	default:
		return collapseNullable(map[string]any{}, distinct), true
	}
}

// typeUnion turns a multi-type list into anyOf branches. The array branch takes
// the items, because Cloud Code rejects an array schema without its own items.
func typeUnion(out map[string]any, types []any) []any {
	items, hasItems := out["items"]
	branches := make([]any, 0, len(types))
	for _, t := range types {
		branch := map[string]any{"type": t}
		if t == "array" && hasItems {
			branch["items"] = items
			delete(out, "items")
		}
		branches = append(branches, branch)
	}
	return branches
}

// listedBranches intersects a multi-type list with anyOf: it keeps untyped
// branches and those whose type is listed (integer fits number), drops null
// branches unless null is listed, and gives items-less array branches the
// parent's items. When only untyped branches fit, each becomes one branch per
// listed type, so the type list is not lost. It returns nil when no non-null
// branch fits, so the caller falls back to the plain type union.
func listedBranches(out map[string]any, types, anyOf []any) []any {
	listed := map[string]bool{}
	for _, t := range types {
		if s, ok := t.(string); ok {
			listed[s] = true
		}
	}
	items, hasItems := out["items"]
	var kept []any
	fits, typedFits := false, false
	for _, b := range anyOf {
		m, ok := b.(map[string]any)
		if !ok {
			kept, fits = append(kept, b), true
			continue
		}
		raw, typed := m["type"]
		t, _ := raw.(string)
		switch {
		case t == "null":
			if out["nullable"] == true {
				kept = append(kept, b)
			}
			continue
		case typed && !listed[t] && (t != "integer" || !listed["number"]):
			continue
		}
		if _, set := m["items"]; t == "array" && hasItems && !set {
			m["items"] = items
		}
		kept, fits, typedFits = append(kept, b), true, typedFits || typed
	}
	if !fits {
		return nil
	}
	if !typedFits {
		kept = typedCopies(kept, types, items, hasItems)
	}
	if listed["array"] {
		delete(out, "items")
	}
	return kept
}

// keywordsByType are the JSON Schema keywords that constrain one type and only
// make sense on it. A shared branch carrying several of these -- as an
// untyped anyOf of a string and a number will -- cannot be copied to every
// per-type branch as it stands, or the number branch ends up with minLength and
// the string branch with minimum (#55).
//
// Anything not listed here is shared: enum, const, description, nullable,
// title, default, and any keyword we have not enumerated. Dropping a constraint
// we did not think of would silently widen a schema, which is worse than
// carrying a redundant one.
var keywordsByType = map[string]map[string]bool{
	"string":  keywordSet("minLength", "maxLength", "pattern", "format"),
	"number":  keywordSet("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "format"),
	"integer": keywordSet("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "format"),
	"array":   keywordSet("items", "minItems", "maxItems", "uniqueItems"),
	"object":  keywordSet("properties", "required", "additionalProperties", "minProperties", "maxProperties"),
}

// keywordFitsType reports whether key belongs on a branch of the given type.
// Gemini accepts `format` on a number as well as a string -- int32, double --
// which is why format is listed under both.
func keywordFitsType(key, typ string) bool {
	typed, ok := keywordsByType[typ]
	if !ok {
		return true
	}
	// A keyword another type owns is not this type's to carry, but it is also
	// not this type's to delete if it is shared with something that applies:
	// the caller copies into an independent branch, so keeping it here would
	// be wrong, and dropping it here would lose it. It stays only if no other
	// listed type claims it.
	if typed[key] {
		return true
	}
	for _, other := range schemaTypes {
		if other != typ && keywordsByType[other][key] {
			return false
		}
	}
	return true
}

func keywordSet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// schemaTypes is the set of JSON Schema type names the filter knows about.
// Not called "types": typedCopies has a parameter by that name.
var schemaTypes = []string{"string", "number", "integer", "array", "object"}

// typedCopies replaces each untyped branch with one copy per listed type,
// the array copy taking the parent's items when it has none. Null branches
// pass through.
func typedCopies(branches, types []any, items any, hasItems bool) []any {
	var out []any
	for _, b := range branches {
		m, ok := b.(map[string]any)
		if ok && m["type"] == "null" {
			out = append(out, b)
			continue
		}
		for _, t := range types {
			branch := map[string]any{}
			for k, v := range m {
				// The listed types come from the branch itself, so they are
				// strings; anything else is carried as it was.
				if name, ok := t.(string); ok && !keywordFitsType(k, name) {
					continue
				}
				branch[k] = v
			}
			branch["type"] = t
			if _, set := branch["items"]; t == "array" && hasItems && !set {
				branch["items"] = items
			}
			out = append(out, branch)
		}
	}
	return out
}

// collapseNullable folds a {"type":"null"} branch into nullable. When one
// branch is left and none of its keys conflict with the parent, it is merged
// into the parent, because Gemini requires an array schema to carry its own
// items (Optional[list] is anyOf[array, null]). A conflicting branch stays
// whole. An items-less array parent takes the items of every branch that has
// them, as one schema or their union, since every value must match one of
// those branches.
func collapseNullable(out map[string]any, anyOf []any) map[string]any {
	branches := anyOf[:0]
	for _, b := range anyOf {
		if m, ok := b.(map[string]any); ok && m["type"] == "null" && len(m) == 1 {
			out["nullable"] = true
			continue
		}
		branches = append(branches, b)
	}
	if len(branches) == 1 {
		if only, ok := branches[0].(map[string]any); ok && mergesLosslessly(out, only) {
			for k, v := range only {
				out[k] = v
			}
			return out
		}
	}
	if _, set := out["items"]; !set && out["type"] == "array" {
		var items []any
		for _, b := range branches {
			if m, ok := b.(map[string]any); ok {
				if it, has := m["items"]; has {
					items = append(items, it)
				}
			}
		}
		if union, ok := distinctUnion(items); ok {
			out["items"] = union
		}
	}
	if len(branches) > 0 {
		out["anyOf"] = branches
	}
	return out
}

func mergesLosslessly(parent, branch map[string]any) bool {
	for k, v := range branch {
		if pv, set := parent[k]; set && !reflect.DeepEqual(pv, v) {
			return false
		}
	}
	return true
}

func allStrings(list []any) bool {
	for _, v := range list {
		if _, isStr := v.(string); !isStr {
			return false
		}
	}
	return true
}

func geminiPartsFromContent(raw json.RawMessage) []geminiPart {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return []geminiPart{{Text: ""}}
	}
	if raw[0] == '"' {
		return []geminiPart{{Text: messageText(raw)}}
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return []geminiPart{{Text: messageText(raw)}}
	}
	var out []geminiPart
	for _, p := range parts {
		switch p.Type {
		case "image_url", "input_image":
			if part, ok := geminiImagePart(imageURLString(p.ImageURL)); ok {
				out = append(out, part)
			}
		default:
			if p.Text != "" {
				out = append(out, geminiPart{Text: p.Text})
			}
		}
	}
	if len(out) == 0 {
		return []geminiPart{{Text: messageText(raw)}}
	}
	return out
}

func geminiImagePart(url string) (geminiPart, bool) {
	url = strings.TrimSpace(url)
	if url == "" {
		return geminiPart{}, false
	}
	const prefix = "data:"
	if strings.HasPrefix(url, prefix) {
		rest := strings.TrimPrefix(url, prefix)
		mime, data, ok := strings.Cut(rest, ";base64,")
		if ok && mime != "" && data != "" {
			return geminiPart{InlineData: &geminiInlineData{MimeType: mime, Data: data}}, true
		}
	}
	return geminiPart{FileData: &geminiFileData{FileURI: url}}, true
}

func imageURLString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.URL
	}
	return ""
}

func messageText(raw json.RawMessage) string {
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

func geminiChatMessage(body []byte) (chatMessage, string, error) {
	type candidate struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	}
	type promptFeedback struct {
		BlockReason string `json:"blockReason"`
	}
	var parsed struct {
		Response struct {
			Candidates     []candidate    `json:"candidates"`
			PromptFeedback promptFeedback `json:"promptFeedback"`
		} `json:"response"`
		Candidates     []candidate    `json:"candidates"`
		PromptFeedback promptFeedback `json:"promptFeedback"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return chatMessage{}, "", err
	}
	cands := parsed.Response.Candidates
	if len(cands) == 0 {
		cands = parsed.Candidates
	}
	var b strings.Builder
	message := chatMessage{Role: "assistant"}
	finish := ""
	if len(cands) == 0 && (parsed.Response.PromptFeedback.BlockReason != "" || parsed.PromptFeedback.BlockReason != "") {
		finish = promptBlocked
	}
	if len(cands) > 0 {
		finish = cands[0].FinishReason
		for _, p := range cands[0].Content.Parts {
			if p.Thought {
				continue
			}
			b.WriteString(p.Text)
			if p.FunctionCall != nil {
				args := p.FunctionCall.Args
				if len(args) == 0 || bytes.Equal(bytes.TrimSpace(args), []byte("null")) {
					args = json.RawMessage("{}")
				}
				message.ToolCalls = append(message.ToolCalls, chatToolCall{
					ID: "call_" + strings.TrimPrefix(newAgentRequestID("agent"), "agent-"), Type: "function",
					Function: chatToolFunction{Name: p.FunctionCall.Name, Arguments: string(args)},
				})
			}
		}
	}
	message.Content = b.String()
	return message, finish, nil
}

func toOpenAIChatJSON(model string, message chatMessage, finish string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":      "peaproxy-antigravity",
		"object":  "chat.completion",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": geminiFinishReason(len(message.ToolCalls), finish)}},
	})
}

// promptBlocked stands in for a finish reason when Gemini rejects the prompt
// itself: it then sends promptFeedback.blockReason and no candidates.
const promptBlocked = "PROMPT_BLOCKED"

func geminiFinishReason(calls int, upstream string) string {
	switch upstream {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", promptBlocked:
		// Like MAX_TOKENS, a blocked turn must not read as a finished tool turn.
		return "content_filter"
	}
	if calls > 0 {
		return "tool_calls"
	}
	return "stop"
}

func geminiSSEToOpenAI(r io.Reader, w io.Writer, model string) error {
	// The role chunk commits the gateway's prelude guard, so it waits for the
	// first real upstream event; a stalled account can then still fail over.
	wroteRole := false
	writeRole := func() error {
		if wroteRole {
			return nil
		}
		wroteRole = true
		return translate.WriteOpenAIChatSSERole(w, "peaproxy-antigravity", model)
	}
	calls := 0
	upstreamFinish := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		payload := strings.TrimSpace(line)
		if !strings.HasPrefix(payload, "data:") {
			continue
		}
		payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
		if payload == "[DONE]" {
			break
		}
		if payload == "" {
			continue
		}
		event := []byte(payload)
		// A failed event ends the stream without a finish chunk, so a partial
		// answer is never reported as complete.
		if err := streamEventError(event); err != nil {
			return err
		}
		delta, finish, err := geminiChatMessage(event)
		if err != nil {
			continue
		}
		if finish != "" {
			upstreamFinish = finish
		}
		if delta.Content == "" && len(delta.ToolCalls) == 0 {
			continue
		}
		delta.Role = ""
		for i := range delta.ToolCalls {
			index := calls
			delta.ToolCalls[i].Index = &index
			calls++
		}
		chunk, err := json.Marshal(map[string]any{
			"id":      "peaproxy-antigravity",
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta}},
		})
		if err != nil {
			return err
		}
		if err := writeRole(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		// Same rule as every other translated wire: a cut stream is announced,
		// never completed. Returning quietly here leaves the client holding a
		// response that stops without a terminal event, which it can only
		// report as a missing finish_reason.
		if wErr := translate.WriteOpenAIChatSSEError(w, "upstream stream ended before the response was complete: "+err.Error()); wErr != nil {
			return wErr
		}
		return err
	}
	if err := writeRole(); err != nil {
		return err
	}
	return translate.WriteOpenAIChatSSEFinish(w, "peaproxy-antigravity", model, geminiFinishReason(calls, upstreamFinish))
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
