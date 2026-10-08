package openai_compat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/usage"
)

func newTestAdapter(t *testing.T, handler http.HandlerFunc, opts adapter.Options) (*Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	opts.BaseURL = srv.URL + "/v1"
	got, err := New(opts)
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	return got.(*Adapter), srv
}

func TestNewCapabilitiesAndValidate(t *testing.T) {
	if _, err := New(adapter.Options{}); err == nil {
		t.Fatal("New without BaseURL succeeded")
	}

	requests := 0
	a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/models" {
			t.Errorf("path = %s, want /v1/models", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	}, adapter.Options{Tier: catalog.TierLocal})
	defer srv.Close()

	if a.ID() != Name {
		t.Errorf("default ID = %q, want %q", a.ID(), Name)
	}
	caps := a.Capabilities()
	if !caps.Chat || !caps.Stream || !caps.Tools || !caps.VisionIn || !caps.ImageOut || !caps.Embeddings || !caps.ListModels || !caps.Local || caps.APIKey {
		t.Errorf("unexpected capabilities: %#v", caps)
	}
	if err := a.Validate(context.Background()); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if requests != 1 {
		t.Errorf("model requests = %d, want 1", requests)
	}
}

func TestListModelsOpenRouterHeadersPricingAndErrors(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
				t.Errorf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer secret" {
				t.Errorf("Authorization = %q", got)
			}
			if got := r.Header.Get("x-session-id"); got != "session" {
				t.Errorf("x-session-id = %q", got)
			}
			if got := r.Header.Get("X-Provider-Header"); got != "present" {
				t.Errorf("extra header = %q", got)
			}
			_, _ = io.WriteString(w, `{"data":[
				{"id":"vendor/model-free","architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"context_length":"128000","pricing":{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.0000003","input_cache_write":"0.00000375"}}
			]}`)
		}, adapter.Options{ID: "account", APIKey: "secret", SessionID: "session", ExtraHeaders: map[string]string{"X-Provider-Header": "present", "": "ignored", "Empty": ""}})
		defer srv.Close()
		a.SetProviderName("openrouter")

		models, err := a.ListModels(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 1 {
			t.Fatalf("got %d models", len(models))
		}
		m := models[0]
		if m.ID != "vendor/model-free" || m.Provider != "openrouter" || m.AccountID != "account" || m.Tier != catalog.TierFree || !m.Exposed || !m.Routable {
			t.Errorf("unexpected model: %#v", m)
		}
		if m.ContextWindow != 128000 || len(m.Modalities) == 0 {
			t.Errorf("context/modalities not retained: %#v", m)
		}
		if m.Price.Input == nil || *m.Price.Input != 3 || m.Price.Output == nil || *m.Price.Output != 15 || m.Price.CacheRead == nil || *m.Price.CacheRead != 0.3 || m.Price.CacheWrite == nil || *m.Price.CacheWrite != 3.75 {
			t.Errorf("price = %#v", m.Price)
		}
	})

	t.Run("http error retains retry metadata", func(t *testing.T) {
		a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "3")
			w.Header().Set("X-Ratelimit-Scope", "model")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, strings.Repeat("x", 300))
		}, adapter.Options{})
		defer srv.Close()

		_, err := a.ListModels(context.Background())
		var httpErr adapter.HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatalf("error = %T %v, want HTTPError", err, err)
		}
		if httpErr.Status != http.StatusTooManyRequests || httpErr.RetryAfter.Seconds() != 3 || httpErr.Scope != adapter.ScopeModel || !strings.HasSuffix(httpErr.Body, "…") {
			t.Errorf("HTTPError = %#v", httpErr)
		}
	})
}

func TestWorkersAIModelListingAndHelpers(t *testing.T) {
	a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/client/v4/accounts/a/ai/models/search" || r.URL.Query().Get("page") != "1" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("workers request = %s", r.URL.String())
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[
			{"name":"@cf/text","task":"text-generation"},
			{"name":"@cf/embed","task":{"name":"text-embedding"}},
			{"name":"@cf/image","task":"image-generation"},
			{"name":"@cf/text","id":"duplicate","task":"text"},
			{"id":"fallback","task":null}
		],"result_info":{"total_count":5}}`)
	}, adapter.Options{})
	defer srv.Close()
	a.baseURL = srv.URL + "/client/v4/accounts/a/ai/v1"
	a.SetProviderName("workers_ai")

	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 {
		t.Fatalf("models = %#v, want four deduplicated rows", models)
	}
	if models[0].ID != "@cf/text" || !hasModality(models[1].Modalities, "embeddings") || !hasModality(models[2].Modalities, "image_out") || models[3].ID != "fallback" || !hasModality(models[3].Modalities, "text") {
		t.Errorf("worker models = %#v", models)
	}
	if got := workersSearchURL("https://example.test/ai/v1", 2); got != "https://example.test/ai/models/search?page=2&per_page=100" {
		t.Errorf("workersSearchURL = %q", got)
	}
	if workersTaskName(json.RawMessage(`{"other":"x"}`)) != "" || workersTaskName(json.RawMessage(`invalid`)) != "" {
		t.Error("invalid worker tasks acquired a name")
	}
}

func hasModality(modalities []string, want string) bool {
	for _, modality := range modalities {
		if modality == want {
			return true
		}
	}
	return false
}

func TestChatForwardsOpenAIRequestAndUsage(t *testing.T) {
	a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("headers = %#v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("reasoning_opaque")) || bytes.Contains(body, []byte(`"stream":true`)) || !bytes.Contains(body, []byte(`"tool_choice":"auto"`)) {
			t.Errorf("forwarded body = %s", body)
		}
		_, _ = io.WriteString(w, `{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":4}}}`)
	}, adapter.Options{APIKey: "k"})
	defer srv.Close()

	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "ignored", Raw: []byte(`{"model":"actual","stream":true,"tool_choice":"auto","messages":[{"role":"assistant","content":"prior","reasoning_opaque":[{"kind":"private","signature":"secret"}]}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "ignored" || resp.Content != "hello" || !bytes.Contains(resp.Raw, []byte(`"cached_tokens":4`)) {
		t.Errorf("response = %#v", resp)
	}
	var event usage.Event
	usage.ApplyPublishedUsage(&event, resp.Raw, resp.CacheHit)
	if !event.TokensKnown || event.PromptTokens != 12 || event.CompletionTokens != 3 || event.CacheRead != 4 || !event.CacheReadNested {
		t.Errorf("preserved usage parsed incorrectly: %#v", event)
	}
}

func TestChatBuildsRequestAndReturnsHTTPError(t *testing.T) {
	a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !bytes.Contains(body, []byte(`"model":"m"`)) || !bytes.Contains(body, []byte(`"stream":false`)) || !bytes.Contains(body, []byte(`"Content":"hello"`)) {
			t.Errorf("built body = %s", body)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"bad model"}`)
	}, adapter.Options{})
	defer srv.Close()

	_, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Messages: []adapter.Message{{Role: "user", Content: "hello"}}})
	var httpErr adapter.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusBadRequest || httpErr.Body != `{"error":"bad model"}` {
		t.Errorf("error = %#v", err)
	}
}

func TestCompleteAssemblesStreamAndPreservesJSONResponses(t *testing.T) {
	t.Run("stream", func(t *testing.T) {
		a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Accept") != "text/event-stream" {
				t.Errorf("Accept = %q", r.Header.Get("Accept"))
			}
			_, _ = io.WriteString(w, "event: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\ndata: not-json\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n")
		}, adapter.Options{})
		defer srv.Close()
		resp, err := a.Complete(context.Background(), "m", []byte(`{"model":"m","stream":true}`))
		if err != nil {
			t.Fatal(err)
		}
		if resp.Content != "Hello" || extractContent(resp.Raw) != "Hello" {
			t.Errorf("assembled response = %#v", resp)
		}
	})

	t.Run("json", func(t *testing.T) {
		a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, " \n{\"choices\":[{\"message\":{\"content\":\"answer\"}}]} \n")
		}, adapter.Options{})
		defer srv.Close()
		resp, err := a.Complete(context.Background(), "m", []byte(`{"model":"m"}`))
		if err != nil || resp.Content != "answer" || string(resp.Raw) != `{"choices":[{"message":{"content":"answer"}}]}` {
			t.Errorf("response = %#v, err = %v", resp, err)
		}
	})
}

func TestImageAndEmbeddingEndpoints(t *testing.T) {
	a, srv := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/images/generations":
			if r.Header.Get("Content-Type") != "application/json" || !bytes.Contains(body, []byte(`"prompt":"draw"`)) {
				t.Errorf("generation request: headers=%#v body=%s", r.Header, body)
			}
			_, _ = io.WriteString(w, `{"created":123,"data":[{"url":"https://image"},{"b64_json":"encoded"}]}`)
		case "/v1/images/edits":
			if r.Header.Get("Content-Type") != "multipart/form-data; boundary=x" || string(body) != "edit-body" {
				t.Errorf("edit request: headers=%#v body=%s", r.Header, body)
			}
			_, _ = io.WriteString(w, `{"data":[{"url":"https://edited"}]}`)
		case "/v1/embeddings":
			if !bytes.Contains(body, []byte(`"model":"embed"`)) || !bytes.Contains(body, []byte(`"input":"text"`)) {
				t.Errorf("embedding request body=%s", body)
			}
			_, _ = io.WriteString(w, `{"model":"provider-embed","data":[{"embedding":[1,2,3]},{"embedding":[4,5,6]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}, adapter.Options{})
	defer srv.Close()

	generated, err := a.GenerateImage(context.Background(), adapter.ImageRequest{Model: "image", Prompt: "draw"})
	if err != nil || generated.Created != 123 || len(generated.URLs) != 1 || len(generated.B64) != 1 {
		t.Errorf("GenerateImage = %#v, %v", generated, err)
	}
	edited, err := a.EditImage(context.Background(), adapter.ImageRequest{Model: "image", Raw: []byte("edit-body"), ContentType: "multipart/form-data; boundary=x"})
	if err != nil || len(edited.URLs) != 1 || edited.URLs[0] != "https://edited" {
		t.Errorf("EditImage = %#v, %v", edited, err)
	}
	embedding, err := a.CreateEmbeddings(context.Background(), adapter.EmbeddingRequest{Model: "embed", Input: "text"})
	if err != nil || embedding.Model != "provider-embed" || embedding.Count != 2 || embedding.Dimensions != 3 {
		t.Errorf("CreateEmbeddings = %#v, %v", embedding, err)
	}
	if _, err := a.EditImage(context.Background(), adapter.ImageRequest{}); !errors.Is(err, adapter.ErrImageModelRequired) {
		t.Errorf("EditImage without raw error = %v", err)
	}
}
