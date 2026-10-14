// Package zai_oauth implements Z.AI Coding Plan login.
//
// The flow matches the public ZCode CLI poll used by 9router: POST
// zcode.z.ai/.../oauth/cli/init, open the returned authorize URL, poll until
// ready, exchange the Z.AI token for a business JWT, then mint a Coding Plan
// key. Chat is OpenAI-compatible at https://api.z.ai/api/coding/paas/v4.
// There is no refresh grant. A subscribed chat was not completed from
// PeaProxy. See docs/OAUTH.md.
package zai_oauth

import (
	"bytes"
	"context"
	"crypto/rand"
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
	"github.com/ks1686/peaproxy/internal/adapter/oauthcompat"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/oauth"
)

const (
	Name         = "zai_oauth"
	ProviderID   = "zai"
	PlanKeyName  = "zcode-api-key"
	authRoot     = "https://zcode.z.ai"
	apiRoot      = "https://api.z.ai"
	defaultChat  = apiRoot + "/api/coding/paas/v4"
	initPath     = "/api/v1/oauth/cli/init"
	pollPath     = "/api/v1/oauth/cli/poll/"
	loginPath    = "/api/auth/z/login"
	customerPath = "/api/biz/customer/getCustomerInfo"
)

// Adapter is a Z.AI Coding Plan poll-login client.
type Adapter struct {
	id           string
	authBase     string
	apiBase      string
	chatBase     string
	httpClient   *http.Client
	persist      func(oauth.Token) error
	pollInterval time.Duration
	mu           sync.Mutex
	token        oauth.Token
	pending      *zaiPending
}

type zaiPending struct {
	flowID    string
	pollToken string
	interval  time.Duration
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
		authBase:     authRoot,
		apiBase:      apiRoot,
		chatBase:     defaultChat,
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
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return adapter.AuthSession{}, err
	}
	pollToken := hex.EncodeToString(buf)
	raw, status, err := a.doJSON(ctx, http.MethodPost, a.authBase+initPath, pollToken, []byte(`{"provider":"`+ProviderID+`"}`))
	if err != nil {
		return adapter.AuthSession{}, err
	}
	if status < 200 || status >= 300 {
		return adapter.AuthSession{}, fmt.Errorf("zai_oauth: init HTTP %d: %s", status, truncate(raw))
	}
	var body struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data struct {
			FlowID       string  `json:"flow_id"`
			AuthorizeURL string  `json:"authorize_url"`
			PollInterval int     `json:"poll_interval_sec"`
			ExpiresAt    float64 `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return adapter.AuthSession{}, fmt.Errorf("zai_oauth: init: %w", err)
	}
	if !successCode(body.Code) || body.Data.FlowID == "" {
		msg := strings.TrimSpace(body.Msg)
		if msg == "" {
			msg = "init returned no data"
		}
		return adapter.AuthSession{}, fmt.Errorf("zai_oauth: %s", msg)
	}
	if body.Data.AuthorizeURL == "" {
		return adapter.AuthSession{}, fmt.Errorf("zai_oauth: init missing flow id or authorize url")
	}
	interval := a.pollInterval
	if body.Data.PollInterval > 0 {
		interval = time.Duration(body.Data.PollInterval) * time.Second
	}
	expiresIn := relativeSeconds(body.Data.ExpiresAt, time.Now())
	a.mu.Lock()
	a.pending = &zaiPending{
		flowID:    body.Data.FlowID,
		pollToken: pollToken,
		interval:  interval,
		expiresIn: expiresIn,
	}
	a.mu.Unlock()
	return adapter.AuthSession{
		Provider:        Name,
		LoginURL:        body.Data.AuthorizeURL,
		VerificationURL: body.Data.AuthorizeURL,
		ExpiresIn:       expiresIn,
	}, nil
}

func (a *Adapter) AuthComplete(ctx context.Context, session adapter.AuthSession, code string) error {
	_ = session
	_ = code
	a.mu.Lock()
	pending := a.pending
	a.mu.Unlock()
	if pending == nil {
		return fmt.Errorf("zai_oauth: no in-progress login (call AuthStart first)")
	}
	max := 15 * time.Minute
	if pending.expiresIn > 0 {
		max = time.Duration(pending.expiresIn) * time.Second
	}
	tok, err := oauth.PollDevice(ctx, pending.interval, max, func() (oauth.DevicePollResult, error) {
		return a.poll(ctx, pending)
	})
	if err != nil {
		return err
	}
	return a.storeToken(tok)
}

func (a *Adapter) poll(ctx context.Context, pending *zaiPending) (oauth.DevicePollResult, error) {
	raw, status, err := a.doJSON(ctx, http.MethodGet, a.authBase+pollPath+url.PathEscape(pending.flowID), pending.pollToken, nil)
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	if status < 200 || status >= 300 {
		return oauth.DevicePollResult{}, fmt.Errorf("zai_oauth: poll HTTP %d: %s", status, truncate(raw))
	}
	var body struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return oauth.DevicePollResult{}, fmt.Errorf("zai_oauth: poll: %w", err)
	}
	if !successCode(body.Code) {
		msg := strings.TrimSpace(body.Msg)
		if msg == "" {
			msg = "poll failed"
		}
		return oauth.DevicePollResult{}, fmt.Errorf("zai_oauth: %s", msg)
	}
	access, email, st, err := readyAccess(body.Data)
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	switch st {
	case "", "pending":
		return oauth.DevicePollResult{Pending: true}, nil
	case "failed":
		return oauth.DevicePollResult{Denied: true}, fmt.Errorf("zai_oauth: authorization denied")
	case "ready":
		if access == "" {
			return oauth.DevicePollResult{}, fmt.Errorf("zai_oauth: poll response missing access token")
		}
	default:
		return oauth.DevicePollResult{Pending: true}, nil
	}
	key, err := a.codingPlanKey(ctx, access)
	if err != nil {
		return oauth.DevicePollResult{}, err
	}
	return oauth.DevicePollResult{Token: oauth.Token{AccessToken: key, Email: email}}, nil
}

func readyAccess(raw json.RawMessage) (access, email, status string, err error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return "", "", "pending", nil
	}
	var data struct {
		Status      string `json:"status"`
		ProviderID  string `json:"providerId"`
		AccessToken string `json:"accessToken"`
		AccessSnake string `json:"access_token"`
		User        struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", "", "", fmt.Errorf("zai_oauth: poll: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", "", "", fmt.Errorf("zai_oauth: poll: %w", err)
	}
	access = firstNonEmpty(tokenField(fields["zai"]), tokenField(fields[data.ProviderID]), data.AccessToken, data.AccessSnake)
	return access, data.User.Email, data.Status, nil
}

func tokenField(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var pair struct {
		AccessToken string `json:"access_token"`
		AccessCamel string `json:"accessToken"`
	}
	if json.Unmarshal(raw, &pair) != nil {
		return ""
	}
	return firstNonEmpty(pair.AccessToken, pair.AccessCamel)
}

func (a *Adapter) codingPlanKey(ctx context.Context, zaiAccess string) (string, error) {
	loginRaw, err := a.business(ctx, http.MethodPost, a.apiBase+loginPath, "", []byte(`{"token":`+strconv.Quote(zaiAccess)+`}`))
	if err != nil {
		return "", err
	}
	var login struct {
		AccessToken string `json:"access_token"`
		AccessCamel string `json:"accessToken"`
	}
	if err := json.Unmarshal(loginRaw, &login); err != nil {
		return "", fmt.Errorf("zai_oauth: business login: %w", err)
	}
	business := strings.TrimSpace(firstNonEmpty(login.AccessToken, login.AccessCamel))
	if business == "" {
		return "", fmt.Errorf("zai_oauth: business login response is missing access token")
	}
	infoRaw, err := a.business(ctx, http.MethodGet, a.apiBase+customerPath, business, nil)
	if err != nil {
		return "", err
	}
	org, project, err := pickOrgAndProject(infoRaw)
	if err != nil {
		return "", err
	}
	listURL := a.apiBase + "/api/biz/v1/organization/" + url.PathEscape(org) + "/projects/" + url.PathEscape(project) + "/api_keys"
	keysRaw, err := a.business(ctx, http.MethodGet, listURL, business, nil)
	if err != nil {
		return "", err
	}
	apiKey := namedKey(keysRaw, PlanKeyName)
	if apiKey == "" {
		created, cerr := a.business(ctx, http.MethodPost, listURL, business, []byte(`{"name":"`+PlanKeyName+`"}`))
		if cerr != nil {
			return "", cerr
		}
		apiKey = namedKey(created, "")
		if apiKey == "" {
			var one struct {
				APIKey string `json:"apiKey"`
			}
			if json.Unmarshal(created, &one) == nil {
				apiKey = strings.TrimSpace(one.APIKey)
			}
		}
	}
	if apiKey == "" {
		return "", fmt.Errorf("zai_oauth: api keys response is missing api key")
	}
	secretRaw, err := a.business(ctx, http.MethodGet, listURL+"/copy/"+url.PathEscape(apiKey), business, nil)
	if err != nil {
		return "", err
	}
	var secret struct {
		SecretKey string `json:"secretKey"`
	}
	if err := json.Unmarshal(secretRaw, &secret); err != nil {
		return "", fmt.Errorf("zai_oauth: api key copy: %w", err)
	}
	secretKey := strings.TrimSpace(secret.SecretKey)
	if secretKey == "" {
		return "", fmt.Errorf("zai_oauth: api key copy response is missing secret key")
	}
	return apiKey + "." + secretKey, nil
}

func namedKey(raw json.RawMessage, name string) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var list []struct {
		Name   string `json:"name"`
		APIKey string `json:"apiKey"`
	}
	if json.Unmarshal(raw, &list) == nil {
		for _, item := range list {
			if name == "" || item.Name == name {
				if key := strings.TrimSpace(item.APIKey); key != "" {
					return key
				}
			}
		}
	}
	return ""
}

func pickOrgAndProject(raw json.RawMessage) (string, string, error) {
	var info struct {
		Organizations []struct {
			OrganizationID   string `json:"organizationId"`
			OrganizationName string `json:"organizationName"`
			Projects         []struct {
				ProjectID   string          `json:"projectId"`
				ProjectName string          `json:"projectName"`
				ProjectType json.RawMessage `json:"projectType"`
			} `json:"projects"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return "", "", fmt.Errorf("zai_oauth: customer info: %w", err)
	}
	type choice struct {
		orgName string
		orgID   string
		project string
		name    string
	}
	var choices []choice
	for _, org := range info.Organizations {
		if strings.TrimSpace(org.OrganizationID) == "" {
			continue
		}
		for _, project := range org.Projects {
			if projectType(project.ProjectType) == "2" || strings.TrimSpace(project.ProjectID) == "" {
				continue
			}
			choices = append(choices, choice{
				orgName: org.OrganizationName,
				orgID:   org.OrganizationID,
				project: project.ProjectID,
				name:    project.ProjectName,
			})
		}
	}
	if len(choices) == 0 {
		return "", "", fmt.Errorf("zai_oauth: unable to resolve organization and project for the coding plan")
	}
	picked := choices[0]
	for _, c := range choices {
		if isDefaultName(c.orgName) {
			picked = c
			break
		}
	}
	if isDefaultName(picked.orgName) {
		for _, c := range choices {
			if c.orgID == picked.orgID && isDefaultName(c.name) {
				picked = c
				break
			}
		}
	} else {
		for _, c := range choices {
			if c.orgID == picked.orgID && isDefaultName(c.name) {
				picked = c
				break
			}
		}
	}
	return picked.orgID, picked.project, nil
}

func projectType(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return strconv.FormatInt(int64(n), 10)
	}
	return ""
}

func isDefaultName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(normalized, "默认机构") || strings.Contains(normalized, "默认项目") || normalized == "default"
}

func (a *Adapter) business(ctx context.Context, method, endpoint, bearer string, body []byte) (json.RawMessage, error) {
	raw, status, err := a.doJSON(ctx, method, endpoint, bearer, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("zai_oauth: business HTTP %d: %s", status, truncate(raw))
	}
	var env struct {
		Code    json.RawMessage `json:"code"`
		Msg     string          `json:"msg"`
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("zai_oauth: business: %w", err)
	}
	if !successCode(env.Code) || (env.Success != nil && !*env.Success) {
		msg := strings.TrimSpace(env.Msg)
		if msg == "" {
			msg = "business error"
		}
		return nil, fmt.Errorf("zai_oauth: %s", msg)
	}
	if len(bytes.TrimSpace(env.Data)) == 0 {
		return raw, nil
	}
	return env.Data, nil
}

func (a *Adapter) doJSON(ctx context.Context, method, endpoint, bearer string, body []byte) ([]byte, int, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, r)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
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
	base := a.chatBase
	a.mu.Unlock()
	if !tok.Valid() {
		return nil, adapter.ErrAuthRequired
	}
	if base == "" {
		base = defaultChat
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

func successCode(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n == 0 || n == 200
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == "0" || s == "200"
	}
	return false
}

func relativeSeconds(expiresAt float64, now time.Time) int {
	if expiresAt <= 0 {
		return 0
	}
	ms := expiresAt
	if expiresAt <= 1e12 {
		ms = expiresAt * 1000
	}
	sec := int(time.UnixMilli(int64(ms)).Sub(now) / time.Second)
	if sec < 0 {
		return 0
	}
	return sec
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
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
