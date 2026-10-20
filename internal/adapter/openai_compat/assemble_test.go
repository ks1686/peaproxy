package openai_compat

import (
	"encoding/json"
	"testing"
)

// Zen free models are always streamed upstream and reassembled for a
// non-stream client. Keeping only the text turned a tool call into an empty
// "stop" answer and dropped the usage the ledger needs.
func TestAssembleOpenAIStreamKeepsToolCallsFinishAndUsage(t *testing.T) {
	stream := []byte("data: {\"id\":\"c1\",\"model\":\"big-pickle\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Let me \"}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"check.\",\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"ping\",\"arguments\":\"{\\\"a\\\":\"}}]}}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"1}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"id\":\"c1\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4,\"total_tokens\":15}}\n\n" +
		"data: [DONE]\n\n")
	content, assembled, err := assembleOpenAIStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if content != "Let me check." {
		t.Fatalf("content = %q", content)
	}
	var out struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(assembled, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason lost: %s", assembled)
	}
	tc := out.Choices[0].Message.ToolCalls
	if len(tc) != 1 || tc[0].ID != "call_1" || tc[0].Function.Name != "ping" || tc[0].Function.Arguments != `{"a":1}` || tc[0].Type != "function" {
		t.Fatalf("tool call lost or mangled: %s", assembled)
	}
	if out.Usage == nil || out.Usage.PromptTokens != 11 || out.Usage.CompletionTokens != 4 {
		t.Fatalf("usage lost: %s", assembled)
	}
	if out.ID != "c1" || out.Model != "big-pickle" {
		t.Fatalf("id/model lost: %s", assembled)
	}
}
