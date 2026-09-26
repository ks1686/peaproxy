package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultOrigin = "http://127.0.0.1:8317"

// VerifyResult is the outcome of hitting the local gateway like a harness would.
type VerifyResult struct {
	Name       string
	ModelsURL  string
	ModelsHTTP int
	ModelsN    int
	ChatURL    string
	ChatHTTP   int
	ChatModel  string
	ChatOK     bool
	Detail     string
}

// Verify GETs /v1/models and optionally POSTs a tiny chat on the preset's wire.
func Verify(ctx context.Context, name, origin string, doChat bool) (VerifyResult, error) {
	p, ok := Get(name)
	if !ok {
		return VerifyResult{}, fmt.Errorf("unknown client %q", name)
	}
	if origin == "" {
		origin = defaultOrigin
	}
	origin = strings.TrimRight(origin, "/")
	out := VerifyResult{Name: p.Name, ModelsURL: origin + "/v1/models"}
	client := &http.Client{Timeout: 8 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, out.ModelsURL, nil)
	if err != nil {
		return out, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("verify %s: is peaproxy serve running?\n  %v", p.Name, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = resp.Body.Close()
	out.ModelsHTTP = resp.StatusCode
	out.Detail = string(body)
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("GET /v1/models HTTP %d", resp.StatusCode)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	out.ModelsN = len(list.Data)
	if !doChat {
		return out, nil
	}
	model := ""
	if len(list.Data) > 0 {
		model = list.Data[0].ID
	}
	if model == "" {
		return out, fmt.Errorf("no models listed — add an account before --chat")
	}
	out.ChatModel = model
	type pingMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if p.Name == "claude-code" {
		out.ChatURL = origin + "/v1/messages"
		payload, err := json.Marshal(struct {
			Model     string    `json:"model"`
			MaxTokens int       `json:"max_tokens"`
			Messages  []pingMsg `json:"messages"`
		}{
			Model:     model,
			MaxTokens: 8,
			Messages:  []pingMsg{{Role: "user", Content: "ping"}},
		})
		if err != nil {
			return out, err
		}
		return postChat(ctx, client, out, payload)
	}
	out.ChatURL = origin + "/v1/chat/completions"
	payload, err := json.Marshal(struct {
		Model     string    `json:"model"`
		MaxTokens int       `json:"max_tokens"`
		Messages  []pingMsg `json:"messages"`
	}{
		Model:     model,
		MaxTokens: 8,
		Messages:  []pingMsg{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		return out, err
	}
	return postChat(ctx, client, out, payload)
}

func postChat(ctx context.Context, client *http.Client, out VerifyResult, payload []byte) (VerifyResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, out.ChatURL, bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return out, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	_ = resp.Body.Close()
	out.ChatHTTP = resp.StatusCode
	out.Detail = string(body)
	out.ChatOK = resp.StatusCode == http.StatusOK
	if !out.ChatOK {
		return out, fmt.Errorf("chat HTTP %d: %s", resp.StatusCode, truncate(string(body), 240))
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// FormatVerify is the CLI printer for Verify.
func FormatVerify(r VerifyResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "verify %s: GET %s HTTP %d models=%d\n", r.Name, r.ModelsURL, r.ModelsHTTP, r.ModelsN)
	if r.ChatURL != "" {
		fmt.Fprintf(&b, "chat %s model=%s HTTP %d ok=%v\n", r.ChatURL, r.ChatModel, r.ChatHTTP, r.ChatOK)
	}
	return b.String()
}
