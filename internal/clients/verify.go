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

// ClientWire is the HTTP path a harness uses for a tiny completion.
type ClientWire string

const (
	WireChat      ClientWire = "chat"
	WireMessages  ClientWire = "messages"
	WireResponses ClientWire = "responses"
)

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
	Wires      []VerifyHit
	Detail     string
}

// VerifyHit is one POST on a harness wire.
type VerifyHit struct {
	Wire ClientWire
	URL  string
	HTTP int
	OK   bool
}

func (w ClientWire) path() string {
	switch w {
	case WireChat:
		return "/v1/chat/completions"
	case WireMessages:
		return "/v1/messages"
	case WireResponses:
		return "/v1/responses"
	default:
		return "/v1/chat/completions"
	}
}

func wiresFor(name string) []ClientWire {
	switch name {
	case "claude-code":
		return []ClientWire{WireMessages}
	case "codex":
		return []ClientWire{WireResponses}
	case "pi":
		return []ClientWire{WireChat, WireMessages}
	case "amp":
		return []ClientWire{WireChat}
	case "cursor", "opencode", "continue", "cline":
		return []ClientWire{WireChat}
	default:
		return []ClientWire{WireChat}
	}
}

// Verify GETs /v1/models and optionally POSTs a tiny chat on the preset's wire(s).
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
	for i, wire := range wiresFor(p.Name) {
		hit, err := postWire(ctx, client, origin, wire, model)
		out.Wires = append(out.Wires, hit)
		if i == 0 {
			out.ChatURL = hit.URL
			out.ChatHTTP = hit.HTTP
			out.ChatOK = hit.OK
			out.Detail = ""
		}
		if err != nil {
			out.Detail = err.Error()
			return out, err
		}
	}
	return out, nil
}

func postWire(ctx context.Context, client *http.Client, origin string, wire ClientWire, model string) (VerifyHit, error) {
	hit := VerifyHit{Wire: wire, URL: origin + wire.path()}
	payload, err := pingPayload(wire, model)
	if err != nil {
		return hit, err
	}
	tmp := VerifyResult{ChatURL: hit.URL}
	tmp, err = postChat(ctx, client, tmp, payload)
	hit.HTTP = tmp.ChatHTTP
	hit.OK = tmp.ChatOK
	if err != nil {
		return hit, err
	}
	return hit, nil
}

func pingPayload(wire ClientWire, model string) ([]byte, error) {
	type pingMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	switch wire {
	case WireMessages:
		return json.Marshal(struct {
			Model     string    `json:"model"`
			MaxTokens int       `json:"max_tokens"`
			Messages  []pingMsg `json:"messages"`
		}{
			Model:     model,
			MaxTokens: 8,
			Messages:  []pingMsg{{Role: "user", Content: "ping"}},
		})
	case WireResponses:
		return json.Marshal(struct {
			Model           string `json:"model"`
			Input           string `json:"input"`
			MaxOutputTokens int    `json:"max_output_tokens"`
		}{
			Model:           model,
			Input:           "ping",
			MaxOutputTokens: 8,
		})
	case WireChat:
		return json.Marshal(struct {
			Model     string    `json:"model"`
			MaxTokens int       `json:"max_tokens"`
			Messages  []pingMsg `json:"messages"`
		}{
			Model:     model,
			MaxTokens: 8,
			Messages:  []pingMsg{{Role: "user", Content: "ping"}},
		})
	default:
		return json.Marshal(struct {
			Model     string    `json:"model"`
			MaxTokens int       `json:"max_tokens"`
			Messages  []pingMsg `json:"messages"`
		}{
			Model:     model,
			MaxTokens: 8,
			Messages:  []pingMsg{{Role: "user", Content: "ping"}},
		})
	}
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
	if len(r.Wires) > 0 {
		for _, hit := range r.Wires {
			fmt.Fprintf(&b, "%s %s model=%s HTTP %d ok=%v\n", hit.Wire, hit.URL, r.ChatModel, hit.HTTP, hit.OK)
		}
		return b.String()
	}
	if r.ChatURL != "" {
		fmt.Fprintf(&b, "chat %s model=%s HTTP %d ok=%v\n", r.ChatURL, r.ChatModel, r.ChatHTTP, r.ChatOK)
	}
	return b.String()
}
