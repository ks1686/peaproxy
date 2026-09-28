package anthropic_oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/oauth"
)

func TestAliasOAuthToolNames(t *testing.T) {
	raw := []byte(`{"model":"claude-opus-5","tools":[{"name":"todowrite","description":"keep me","input_schema":{"type":"object","properties":{"items":{"type":"array"},"name":{"type":"string","enum":["todowrite"]}}}},{"name":"mcp_manage","description":"servers","input_schema":{"type":"object","properties":{"action":{"type":"string"}}}},{"name":"bash","description":"shell","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"todowrite"},"messages":[{"role":"user","content":"please todowrite"},{"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"toolu_1","name":"todowrite","input":{"items":["a"],"name":"todowrite"}},{"type":"tool_use","id":"toolu_2","name":"mcp_manage","input":{"action":"list"}},{"type":"tool_use","id":"toolu_3","name":"bash","input":{}}]}]}`)

	out, reverse := aliasOAuthToolNames(raw)
	if reverse["TodoWrite"] != "todowrite" || reverse["use_mcp"] != "mcp_manage" {
		t.Fatalf("reverse = %#v", reverse)
	}
	if _, ok := reverse["bash"]; ok {
		t.Fatalf("bash must not be reversed: %#v", reverse)
	}

	names := toolNames(t, out)
	if strings.Join(names, ",") != "TodoWrite,use_mcp,bash" {
		t.Fatalf("tool names = %v", names)
	}
	if got := toolChoiceName(t, out); got != "TodoWrite" {
		t.Fatalf("tool_choice name = %q", got)
	}
	uses := toolUseNames(t, out)
	if strings.Join(uses, ",") != "TodoWrite,use_mcp,bash" {
		t.Fatalf("tool_use names = %v", uses)
	}
	text := string(out)
	for _, keep := range []string{
		`"description":"keep me"`,
		`"description":"servers"`,
		`"enum":["todowrite"]`,
		`"id":"toolu_1"`,
		`"items":["a"]`,
		`"name":"todowrite"`,
		`please todowrite`,
	} {
		if !strings.Contains(text, keep) {
			t.Fatalf("lost %s in %s", keep, text)
		}
	}
	if strings.Count(text, `"name":"todowrite"`) != 1 {
		t.Fatalf("tool input name must stay client-side, got %s", text)
	}
}

func TestRestoreOAuthToolNames(t *testing.T) {
	reverse := map[string]string{"TodoWrite": "todowrite", "use_mcp": "mcp_manage"}
	raw := []byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"TodoWrite","input":{"name":"TodoWrite"}},{"type":"tool_use","id":"toolu_2","name":"use_mcp","input":{}},{"type":"text","text":"TodoWrite"},{"type":"tool_use","id":"toolu_3","name":"lookup","input":{}}],"stop_reason":"tool_use"}`)
	out := restoreOAuthToolNames(raw, reverse)
	if strings.Join(responseToolNames(t, out), ",") != "todowrite,mcp_manage,lookup" {
		t.Fatalf("restored names in %s", out)
	}
	if strings.Count(string(out), `"name":"TodoWrite"`) != 1 {
		t.Fatalf("tool input must keep upstream spelling: %s", out)
	}
	if !strings.Contains(string(out), `"text":"TodoWrite"`) {
		t.Fatalf("text changed: %s", out)
	}
	if same := restoreOAuthToolNames(raw, nil); string(same) != string(raw) {
		t.Fatalf("empty reverse map changed %s", same)
	}

	event := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"use_mcp","input":{}}}`)
	restored := restoreOAuthToolNames(event, reverse)
	if !strings.Contains(string(restored), `"name":"mcp_manage"`) || strings.Contains(string(restored), `"name":"use_mcp"`) {
		t.Fatalf("sse event = %s", restored)
	}

	var buf bytes.Buffer
	filter := &oAuthToolSSEFilter{dst: &buf, reverse: reverse}
	if _, err := filter.Write([]byte("data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_4\",\"name\":\"Todo")); err != nil {
		t.Fatal(err)
	}
	if _, err := filter.Write([]byte("Write\"}}\n")); err != nil {
		t.Fatal(err)
	}
	if err := filter.Flush(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"name":"todowrite"`) || strings.Contains(buf.String(), "TodoWrite") {
		t.Fatalf("sse filter = %s", buf.String())
	}
}

func TestRestoreOAuthToolNamesOnWire(t *testing.T) {
	reqBody := []byte(`{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"ping"}],"tools":[{"name":"todowrite","description":"d","input_schema":{"type":"object"}},{"name":"mcp_manage","description":"d","input_schema":{"type":"object"}}]}`)
	var upstream string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		upstream = string(raw)
		if r.URL.Query().Get("beta") != "true" {
			t.Errorf("beta query %s", r.URL.RawQuery)
		}
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"TodoWrite\"}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"TodoWrite","input":{}},{"type":"tool_use","id":"toolu_2","name":"use_mcp","input":{}}],"stop_reason":"tool_use"}`)
	}))
	t.Cleanup(srv.Close)
	a := testAdapter(t, srv.URL)
	a.token = oauth.Token{AccessToken: "oauth-at", ExpiresAt: time.Now().Add(time.Hour)}

	out, err := a.Messages(context.Background(), reqBody)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(upstream, `"name":"TodoWrite"`) || !strings.Contains(upstream, `"name":"use_mcp"`) {
		t.Fatalf("upstream missing aliases: %s", upstream)
	}
	if strings.Contains(upstream, `"name":"todowrite"`) || strings.Contains(upstream, `"name":"mcp_manage"`) {
		t.Fatalf("upstream kept client names: %s", upstream)
	}
	if strings.Join(responseToolNames(t, out), ",") != "todowrite,mcp_manage" {
		t.Fatalf("client response = %s", out)
	}

	var streamed bytes.Buffer
	if err := a.MessagesStream(context.Background(), reqBody, &streamed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(streamed.String(), `"name":"todowrite"`) || strings.Contains(streamed.String(), "TodoWrite") {
		t.Fatalf("stream = %s", streamed.String())
	}
}

func TestAliasOAuthToolNamesSkipsCollision(t *testing.T) {
	raw := []byte(`{"tools":[{"name":"todowrite","description":"a","input_schema":{"type":"object"}},{"name":"TodoWrite","description":"b","input_schema":{"type":"object"}},{"name":"mcp_manage","description":"c","input_schema":{"type":"object"}},{"name":"use_mcp","description":"d","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"todowrite"},"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"todowrite","input":{}},{"type":"tool_use","id":"toolu_2","name":"mcp_manage","input":{}}]}]}`)
	out, reverse := aliasOAuthToolNames(raw)
	if len(reverse) != 0 {
		t.Fatalf("reverse = %#v, want empty", reverse)
	}
	if string(out) != string(raw) {
		t.Fatalf("collision changed body:\n%s", out)
	}
}

func responseToolNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var body struct {
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(body.Content))
	for _, block := range body.Content {
		if block.Type == "tool_use" {
			names = append(names, block.Name)
		}
	}
	return names
}

func toolNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var body struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(body.Tools))
	for i, tool := range body.Tools {
		names[i] = tool.Name
	}
	return names
}

func toolChoiceName(t *testing.T, raw []byte) string {
	t.Helper()
	var body struct {
		ToolChoice struct {
			Name string `json:"name"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body.ToolChoice.Name
}

func toolUseNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, msg := range body.Messages {
		if msg.Role != "assistant" {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			t.Fatal(err)
		}
		for _, block := range blocks {
			if block.Type == "tool_use" {
				names = append(names, block.Name)
			}
		}
	}
	return names
}
