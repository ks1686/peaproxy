package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/usage"
)

// A non-stream Responses call answered by a chat-wire upstream is recorded from
// the translated body. Without usage there, the call was unmeasured spend.
func TestFromOpenAIChatCarriesUsage(t *testing.T) {
	raw := []byte(`{"id":"c","choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1200,"completion_tokens":30,"total_tokens":1230,"prompt_tokens_details":{"cached_tokens":1000}}}`)
	out, err := FromOpenAIChat(raw, "m")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body.Usage == nil || body.Usage.InputTokens != 1200 || body.Usage.OutputTokens != 30 {
		t.Fatalf("usage = %+v, want input 1200 output 30: %s", body.Usage, out)
	}
	if body.Usage.InputTokensDetails == nil || body.Usage.InputTokensDetails.CachedTokens != 1000 {
		t.Fatalf("cached tokens dropped: %s", out)
	}
	var e usage.Event
	usage.ApplyPublishedUsage(&e, out, false)
	if !e.Costable || e.PromptTokens != 1200 || e.CacheRead != 1000 {
		t.Fatalf("ledger reads %+v from the translated body", e)
	}
}

// Chat counts cached reads inside prompt_tokens. The streamed Responses bridge
// has to say which of its input tokens were cached, or they are priced as
// fresh input.
func TestStreamedResponsesCarryCachedInputTokens(t *testing.T) {
	in := "data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1100,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":1000}}}\n\n" +
		"data: [DONE]\n\n"
	var out strings.Builder
	if err := OpenAISSEToResponses(strings.NewReader(in), &out, "m"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"input_tokens_details":{"cached_tokens":1000}`) {
		t.Fatalf("terminal usage lost the cached input tokens:\n%s", out.String())
	}
}
