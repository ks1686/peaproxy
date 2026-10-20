package translate

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ks1686/peaproxy/internal/jsonx"
)

// responsesStreamToJSON turns a Codex SSE body into the completed response
// object. A JSON object is returned unchanged so a non-stream payload still parses.
// ResponsesStreamToJSON assembles a Responses SSE body into its final JSON response.
func ResponsesStreamToJSON(body []byte) ([]byte, error) {
	return responsesStreamToJSON(body)
}

func responsesStreamToJSON(body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("empty responses body")
	}
	if trimmed[0] == '{' {
		return trimmed, nil
	}
	var completed []byte
	var completedType string
	var text strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(trimmed))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Type     string          `json:"type"`
			Delta    string          `json:"delta"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.completed", "response.incomplete", "response.failed":
			resp := bytes.TrimSpace(ev.Response)
			if len(resp) > 0 && resp[0] == '{' {
				completed = append([]byte(nil), resp...)
				completedType = ev.Type
			}
		}
		if strings.HasSuffix(ev.Type, "output_text.delta") {
			text.WriteString(ev.Delta)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if completedType == "response.failed" {
		return nil, fmt.Errorf("responses stream failed")
	}
	if completedType == "response.incomplete" {
		return nil, fmt.Errorf("responses stream incomplete")
	}
	if len(completed) > 0 {
		// Codex often completes with reasoning only. The answer arrived as
		// output_text.delta events, so keep that text when the object has none.
		if text.Len() > 0 && !responsesObjectHasText(completed) {
			return mergeResponsesDeltaText(completed, text.String())
		}
		return completed, nil
	}
	// No terminal event: the stream was cut off. Assembling its partial text
	// into a response would present a truncated answer as a finished one.
	return nil, fmt.Errorf("responses stream ended without a completed response")
}

func responsesObjectHasText(body []byte) bool {
	var parsed struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return false
	}
	if strings.TrimSpace(parsed.OutputText) != "" {
		return true
	}
	for _, item := range parsed.Output {
		for _, part := range item.Content {
			if strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
	}
	return false
}

func mergeResponsesDeltaText(completed []byte, text string) ([]byte, error) {
	quoted, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	msg, err := marshalResponsesMessage("assistant", "output_text", text)
	if err != nil {
		return nil, err
	}
	var existing struct {
		Output []json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(completed, &existing)
	items := append(existing.Output, msg)
	rawItems, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	out := jsonx.SetTopLevelRaw(completed, "output_text", quoted)
	return jsonx.SetTopLevelRaw(out, "output", rawItems), nil
}

// ResponsesToChatCompletion converts a completed native Responses body to the
// Chat Completions JSON shape, preserving tool calls, usage, and finish state.
func ResponsesToChatCompletion(model string, body []byte) ([]byte, string, error) {
	return responsesToChatCompletion(model, body)
}

func responsesToChatCompletion(model string, body []byte) ([]byte, string, error) {
	var parsed struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Choices []struct {
			Message struct {
				Content   string         `json:"content"`
				ToolCalls []chatToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, "", err
	}
	var outputRaw struct {
		Output []json.RawMessage `json:"output"`
	}
	_ = json.Unmarshal(body, &outputRaw)
	var reasoningItems []json.RawMessage
	for _, item := range outputRaw.Output {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &kind) == nil && kind.Type == "reasoning" {
			reasoningItems = append(reasoningItems, item)
		}
	}
	opaque, err := CarryResponsesReasoning(reasoningItems)
	if err != nil {
		return nil, "", err
	}
	var text strings.Builder
	if parsed.OutputText != "" {
		text.WriteString(parsed.OutputText)
	}
	var calls []chatToolCall
	for _, o := range parsed.Output {
		switch o.Type {
		case "reasoning":
			continue
		case "function_call":
			tc := chatToolCall{ID: o.CallID, Type: "function"}
			tc.Function.Name = o.Name
			tc.Function.Arguments = o.Arguments
			calls = append(calls, tc)
		default:
			for _, c := range o.Content {
				if c.Type == "output_text" || c.Type == "text" || c.Text != "" {
					if parsed.OutputText == "" {
						text.WriteString(c.Text)
					}
				}
			}
		}
	}
	if text.Len() == 0 && len(parsed.Choices) > 0 {
		text.WriteString(parsed.Choices[0].Message.Content)
		if len(calls) == 0 {
			calls = parsed.Choices[0].Message.ToolCalls
		}
	}
	content := text.String()
	finish := "stop"
	msg := chatChoiceMessage{Role: "assistant"}
	msg.ReasoningOpaque = opaque
	if len(calls) > 0 {
		msg.ToolCalls = calls
		finish = "tool_calls"
		if content == "" {
			msg.Content = json.RawMessage("null")
		} else {
			raw, err := json.Marshal(content)
			if err != nil {
				return nil, "", err
			}
			msg.Content = raw
		}
	} else {
		raw, err := json.Marshal(content)
		if err != nil {
			return nil, "", err
		}
		msg.Content = raw
	}
	out := struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int               `json:"index"`
			Message      chatChoiceMessage `json:"message"`
			FinishReason string            `json:"finish_reason"`
		} `json:"choices"`
		// Omitted rather than zeroed: a Responses object with no usage object
		// means the call was never measured, and a zero-filled usage would read
		// as a measured call that happened to cost nothing.
		Usage *OpenAIChatUsage `json:"usage,omitempty"`
	}{ID: "peaproxy-codex", Object: "chat.completion", Model: model, Usage: responsesBodyUsage(body)}
	out.Choices = make([]struct {
		Index        int               `json:"index"`
		Message      chatChoiceMessage `json:"message"`
		FinishReason string            `json:"finish_reason"`
	}, 1)
	out.Choices[0].Message = msg
	out.Choices[0].FinishReason = finish
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	return raw, content, nil
}
