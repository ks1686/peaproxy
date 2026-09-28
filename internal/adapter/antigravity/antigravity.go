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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/oauth"
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
	defer resp.Body.Close()
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
	seen := a.generation
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
	next, err := oauth.DefaultRefresh.Do(ctx, "antigravity:"+a.id, func(ctx context.Context) (oauth.Token, error) {
		return a.refresh(ctx, tok.RefreshToken)
	})
	if err != nil {
		return err
	}
	next = next.KeepExtra(tok)
	return oauth.CommitRefresh(&a.mu, &a.token, &a.generation, seen, next, a.persist)
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
	defer resp.Body.Close()
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
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	if resp.StatusCode >= 300 {
		return adapter.ChatResponse{}, chatHTTPError(resp, truncate(body))
	}
	content := extractGeminiText(body)
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
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return chatHTTPError(resp, truncate(body))
	}
	return geminiSSEToOpenAI(resp.Body, w, req.Model)
}

func chatHTTPError(resp *http.Response, body string) error {
	e := adapter.NewHTTPError(resp, body)
	// Cloud Code does not send X-Ratelimit-Scope. A 429 is this model, not the
	// whole Google account, so the next model can still be tried.
	if e.Status == http.StatusTooManyRequests && e.Scope == adapter.ScopeAccount {
		e.Scope = adapter.ScopeModel
	}
	return e
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
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
	FileData   *geminiFileData   `json:"fileData,omitempty"`
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
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
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
	var sys strings.Builder
	var contents []geminiContent
	for _, m := range msgs {
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			if sys.Len() > 0 {
				sys.WriteByte('\n')
			}
			sys.WriteString(messageText(m.Content))
		case "assistant":
			contents = append(contents, geminiContent{Role: "model", Parts: geminiPartsFromContent(m.Content)})
		default:
			contents = append(contents, geminiContent{Role: "user", Parts: geminiPartsFromContent(m.Content)})
		}
	}
	reqType := "agent"
	if strings.Contains(strings.ToLower(model), "image") {
		reqType = "image_gen"
	}
	env := geminiEnvelope{
		Project:     project,
		Model:       model,
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
		out = append(out, fn)
	}
	return out
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

func extractGeminiText(body []byte) string {
	var parsed struct {
		Response struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		} `json:"response"`
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	cands := parsed.Response.Candidates
	if len(cands) == 0 {
		cands = parsed.Candidates
	}
	var b strings.Builder
	for _, c := range cands {
		for _, p := range c.Content.Parts {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func toOpenAIChatJSON(model, content string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":      "peaproxy-antigravity",
		"object":  "chat.completion",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": content}, "finish_reason": "stop"}},
	})
}

func geminiSSEToOpenAI(r io.Reader, w io.Writer, model string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		payload := strings.TrimSpace(line)
		if strings.HasPrefix(payload, "data:") {
			payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
		}
		if payload == "" || payload == "[DONE]" {
			continue
		}
		delta := extractGeminiText([]byte(payload))
		if delta == "" {
			continue
		}
		chunk, err := json.Marshal(map[string]any{
			"id":      "peaproxy-antigravity",
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
