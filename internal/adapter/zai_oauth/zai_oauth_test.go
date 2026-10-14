package zai_oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
)

func TestPollLoginMintsCodingPlanKeyThenChats(t *testing.T) {
	var polls int
	var creates int
	var sawBearer bool
	var initAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/oauth/cli/init":
			initAuth = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"provider":"zai"}` {
				t.Errorf("init body %s", body)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"flow_id":"flow-1","authorize_url":"https://z.ai/oauth?client_id=server","poll_interval_sec":0,"expires_at":0}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/oauth/cli/poll/flow-1":
			if r.Header.Get("Authorization") != initAuth || !strings.HasPrefix(initAuth, "Bearer ") {
				t.Errorf("poll auth %s init %s", r.Header.Get("Authorization"), initAuth)
			}
			polls++
			if polls == 1 {
				_, _ = w.Write([]byte(`{"code":0,"data":{"status":"pending"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":"200","data":{"status":"ready","user":{"email":"a@z.ai"},"zai":{"access_token":"zai-oauth"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/auth/z/login":
			var body struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Token != "zai-oauth" {
				t.Errorf("login token %s", body.Token)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"biz-jwt"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/biz/customer/getCustomerInfo":
			if r.Header.Get("Authorization") != "Bearer biz-jwt" {
				t.Errorf("customer auth %s", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"organizations":[{"organizationId":"org-1","organizationName":"other","projects":[{"projectId":"team","projectName":"Team","projectType":"2"},{"projectId":"proj-1","projectName":"默认项目","projectType":1}]},{"organizationId":"org-default","organizationName":"默认机构","projects":[{"projectId":"proj-d","projectName":"work","projectType":"1"}]}]}}`))
		case r.URL.Path == "/api/biz/v1/organization/org-default/projects/proj-d/api_keys" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"code":0,"data":[{"name":"other","apiKey":"nope"},{"name":"zcode-api-key","apiKey":"kid"}]}`))
		case r.URL.Path == "/api/biz/v1/organization/org-default/projects/proj-d/api_keys" && r.Method == http.MethodPost:
			creates++
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/api/biz/v1/organization/org-default/projects/proj-d/api_keys/copy/kid":
			_, _ = w.Write([]byte(`{"code":0,"data":{"secretKey":"sek"}}`))
		case r.URL.Path == "/coding/models":
			if r.Header.Get("Authorization") != "Bearer kid.sek" {
				t.Errorf("models auth %s", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"glm-test"}]}`))
		case r.URL.Path == "/coding/chat/completions":
			sawBearer = r.Header.Get("Authorization") == "Bearer kid.sek"
			_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	raw, err := New(adapter.Options{ID: "zai"})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.authBase = srv.URL
	a.apiBase = srv.URL
	a.chatBase = srv.URL + "/coding"
	a.httpClient = srv.Client()
	a.pollInterval = time.Millisecond

	sess, err := a.AuthStart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sess.LoginURL != "https://z.ai/oauth?client_id=server" || sess.UserCode != "" {
		t.Fatalf("session %#v", sess)
	}
	if err := a.AuthComplete(context.Background(), sess, ""); err != nil {
		t.Fatal(err)
	}
	if creates != 0 {
		t.Fatalf("created a key that already existed: %d", creates)
	}
	if a.token.AccessToken != "kid.sek" || a.token.Email != "a@z.ai" || a.token.RefreshToken != "" {
		t.Fatalf("token email %s refresh %q", a.token.Email, a.token.RefreshToken)
	}
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "glm-test" || !models[0].SubscriptionOAuth || models[0].Provider != Name {
		t.Fatalf("models %#v", models)
	}
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "glm-test", Messages: []adapter.Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" || !sawBearer {
		t.Fatalf("content %q auth %v", resp.Content, sawBearer)
	}
}

func TestPollDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"code":0,"data":{"flow_id":"f","authorize_url":"https://z.ai/oauth"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"status":"failed"}}`))
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.authBase = srv.URL
	a.httpClient = srv.Client()
	a.pollInterval = time.Millisecond
	if _, err := a.AuthStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	err = a.AuthComplete(context.Background(), adapter.AuthSession{}, "")
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatal(err)
	}
}

func TestCreatesKeyWhenMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/oauth/cli/init":
			_, _ = w.Write([]byte(`{"data":{"flow_id":"f","authorize_url":"https://z.ai/oauth"}}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/oauth/cli/poll/"):
			_, _ = w.Write([]byte(`{"data":{"status":"ready","access_token":"zai-oauth"}}`))
		case r.URL.Path == "/api/auth/z/login":
			_, _ = w.Write([]byte(`{"data":{"access_token":"biz"}}`))
		case r.URL.Path == "/api/biz/customer/getCustomerInfo":
			_, _ = w.Write([]byte(`{"data":{"organizations":[{"organizationId":"o","organizationName":"default","projects":[{"projectId":"p","projectName":"default","projectType":"1"}]}]}}`))
		case r.URL.Path == "/api/biz/v1/organization/o/projects/p/api_keys" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.URL.Path == "/api/biz/v1/organization/o/projects/p/api_keys" && r.Method == http.MethodPost:
			var body struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name != PlanKeyName {
				t.Errorf("name %s", body.Name)
			}
			_, _ = w.Write([]byte(`{"data":{"apiKey":"new-kid","name":"zcode-api-key"}}`))
		case r.URL.Path == "/api/biz/v1/organization/o/projects/p/api_keys/copy/new-kid":
			_, _ = w.Write([]byte(`{"data":{"secretKey":"new-sek"}}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	raw, err := New(adapter.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := raw.(*Adapter)
	a.authBase = srv.URL
	a.apiBase = srv.URL
	a.httpClient = srv.Client()
	a.pollInterval = time.Millisecond
	if _, err := a.AuthStart(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.AuthComplete(context.Background(), adapter.AuthSession{}, ""); err != nil {
		t.Fatal(err)
	}
	if a.token.AccessToken != "new-kid.new-sek" {
		t.Fatalf("token %s", a.token.AccessToken)
	}
}

func TestNoPersonalProject(t *testing.T) {
	raw := []byte(`{"organizations":[{"organizationId":"o","projects":[{"projectId":"t","projectType":"2"}]}]}`)
	_, _, err := pickOrgAndProject(raw)
	if err == nil || !strings.Contains(err.Error(), "organization and project") {
		t.Fatal(err)
	}
}

func TestRelativeSeconds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if got := relativeSeconds(float64(now.Add(90*time.Second).Unix()), now); got != 90 && got != 89 && got != 91 {
		t.Fatalf("seconds %d", got)
	}
	if got := relativeSeconds(float64(now.Add(2*time.Minute).UnixMilli()), now); got < 119 || got > 121 {
		t.Fatalf("millis %d", got)
	}
	if relativeSeconds(0, now) != 0 {
		t.Fatal("missing expiry")
	}
}
