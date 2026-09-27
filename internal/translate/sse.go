package translate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// OpenAISSEToClaude converts chat.completion.chunk SSE into Anthropic Messages SSE.
func OpenAISSEToClaude(r io.Reader, w io.Writer, model string) error {
	started := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	writeEvent := func(event, data string) error {
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if !started {
			started = true
			id := chunk.ID
			if id == "" {
				id = "msg_peaproxy"
			}
			if err := writeEvent("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":%s,"type":"message","role":"assistant","model":%s,"content":[]}}`, jsonString(id), jsonString(model))); err != nil {
				return err
			}
			if err := writeEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`); err != nil {
				return err
			}
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			body := struct {
				Type  string `json:"type"`
				Index int    `json:"index"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}{Type: "content_block_delta", Index: 0}
			body.Delta.Type = "text_delta"
			body.Delta.Text = chunk.Choices[0].Delta.Content
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			if err := writeEvent("content_block_delta", string(raw)); err != nil {
				return err
			}
		}
	}
	if !started {
		// Empty upstream (or HTTPError before any bytes) — do not invent a turn.
		return sc.Err()
	}
	if err := writeEvent("content_block_stop", `{"type":"content_block_stop","index":0}`); err != nil {
		return err
	}
	if err := writeEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null}}`); err != nil {
		return err
	}
	if err := writeEvent("message_stop", `{"type":"message_stop"}`); err != nil {
		return err
	}
	return sc.Err()
}

type openAIChatDelta struct {
	Role      string                 `json:"role,omitempty"`
	Content   string                 `json:"content,omitempty"`
	ToolCalls []openAIStreamToolCall `json:"tool_calls,omitempty"`
}

type openAIStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type openAIChatSSEChunk struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Model   string                `json:"model"`
	Choices []openAIChatSSEChoice `json:"choices"`
}

type openAIChatSSEChoice struct {
	Index        int             `json:"index"`
	Delta        openAIChatDelta `json:"delta"`
	FinishReason string          `json:"finish_reason,omitempty"`
}

// WriteOpenAIChatSSERole writes the conventional first chat.completion.chunk
// with delta.role=assistant.
func WriteOpenAIChatSSERole(w io.Writer, id, model string) error {
	return writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{Role: "assistant"}, "")
}

// WriteOpenAIChatSSEFinish writes a terminal chat.completion.chunk with
// finish_reason, then data: [DONE].
func WriteOpenAIChatSSEFinish(w io.Writer, id, model, finishReason string) error {
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{}, finishReason); err != nil {
		return err
	}
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}

func writeOpenAIChatSSEChunk(w io.Writer, id, model string, delta openAIChatDelta, finishReason string) error {
	chunk := openAIChatSSEChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Model:   model,
		Choices: []openAIChatSSEChoice{{Index: 0, Delta: delta, FinishReason: finishReason}},
	}
	raw, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", raw)
	return err
}

func openAIChatFinishReason(hadToolCalls bool) string {
	if hadToolCalls {
		return "tool_calls"
	}
	return "stop"
}

// ClaudeSSEToOpenAI converts Anthropic SSE into chat.completion.chunk SSE.
func ClaudeSSEToOpenAI(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	id := "chatcmpl-peaproxy"
	model := ""
	wrote := false
	wroteRole := false
	hadToolCalls := false
	writeRole := func() error {
		if wroteRole {
			return nil
		}
		if err := WriteOpenAIChatSSERole(w, id, model); err != nil {
			return err
		}
		wroteRole = true
		return nil
	}
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message *struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"message"`
			ContentBlock *struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				if ev.Message.ID != "" {
					id = ev.Message.ID
				}
				model = ev.Message.Model
			}
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				hadToolCalls = true
				wrote = true
				if err := writeRole(); err != nil {
					return err
				}
				tc := openAIStreamToolCall{Index: ev.Index, ID: ev.ContentBlock.ID, Type: "function"}
				tc.Function.Name = ev.ContentBlock.Name
				if err := writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{ToolCalls: []openAIStreamToolCall{tc}}, ""); err != nil {
					return err
				}
			}
		case "content_block_delta":
			if ev.Delta.Type == "input_json_delta" || ev.Delta.PartialJSON != "" {
				hadToolCalls = true
				wrote = true
				if err := writeRole(); err != nil {
					return err
				}
				tc := openAIStreamToolCall{Index: ev.Index}
				tc.Function.Arguments = ev.Delta.PartialJSON
				if err := writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{ToolCalls: []openAIStreamToolCall{tc}}, ""); err != nil {
					return err
				}
				continue
			}
			if ev.Delta.Text == "" {
				continue
			}
			if err := writeRole(); err != nil {
				return err
			}
			if err := writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{Content: ev.Delta.Text}, ""); err != nil {
				return err
			}
			wrote = true
		case "message_delta":
			if ev.Delta.StopReason == "tool_use" {
				hadToolCalls = true
				wrote = true
			}
		default:
			// ignore ping, content_block_stop, message_stop
		}
	}
	if wrote {
		if err := writeRole(); err != nil {
			return err
		}
		if err := WriteOpenAIChatSSEFinish(w, id, model, openAIChatFinishReason(hadToolCalls)); err != nil {
			return err
		}
	}
	return sc.Err()
}

// OpenAISSEToResponses converts chat.completion.chunk SSE into Responses API SSE.
// Text deltas become response.output_text.delta. Chat tool_calls are mapped to
// function_call output items on response.completed (plus argument deltas when
// present). This does not execute tools or invent tool results.
func OpenAISSEToResponses(r io.Reader, w io.Writer, model string) error {
	started := false
	id := "resp_peaproxy"
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	writeEvent := func(event, data string) error {
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		return err
	}
	var text strings.Builder
	type pendingCall struct {
		ID, Name, Arguments string
		Started             bool
	}
	calls := map[int]*pendingCall{}
	var order []int
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if !started {
			started = true
			created := struct {
				Type     string `json:"type"`
				Response struct {
					ID     string `json:"id"`
					Object string `json:"object"`
					Status string `json:"status"`
					Model  string `json:"model"`
				} `json:"response"`
			}{Type: "response.created"}
			created.Response.ID = id
			created.Response.Object = "response"
			created.Response.Status = "in_progress"
			created.Response.Model = model
			raw, err := json.Marshal(created)
			if err != nil {
				return err
			}
			if err := writeEvent("response.created", string(raw)); err != nil {
				return err
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			text.WriteString(delta.Content)
			ev := struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			}{Type: "response.output_text.delta", Delta: delta.Content}
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if err := writeEvent("response.output_text.delta", string(raw)); err != nil {
				return err
			}
		}
		for _, tc := range delta.ToolCalls {
			pc, ok := calls[tc.Index]
			if !ok {
				pc = &pendingCall{}
				calls[tc.Index] = pc
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				pc.ID = tc.ID
			}
			if tc.Function.Name != "" {
				pc.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				pc.Arguments += tc.Function.Arguments
				ev := struct {
					Type  string `json:"type"`
					Delta string `json:"delta"`
				}{Type: "response.function_call_arguments.delta", Delta: tc.Function.Arguments}
				raw, err := json.Marshal(ev)
				if err != nil {
					return err
				}
				if err := writeEvent("response.function_call_arguments.delta", string(raw)); err != nil {
					return err
				}
			}
			if !pc.Started && (pc.ID != "" || pc.Name != "") {
				pc.Started = true
				item := struct {
					Type      string `json:"type"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Type: "function_call", CallID: pc.ID, Name: pc.Name, Arguments: pc.Arguments}
				added := struct {
					Type string `json:"type"`
					Item struct {
						Type      string `json:"type"`
						CallID    string `json:"call_id"`
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"item"`
				}{Type: "response.output_item.added", Item: item}
				raw, err := json.Marshal(added)
				if err != nil {
					return err
				}
				if err := writeEvent("response.output_item.added", string(raw)); err != nil {
					return err
				}
			}
		}
	}
	if !started {
		return sc.Err()
	}
	var output []responsesOutMsg
	for _, idx := range order {
		pc := calls[idx]
		output = append(output, responsesOutMsg{
			Type:      "function_call",
			CallID:    pc.ID,
			Name:      pc.Name,
			Arguments: pc.Arguments,
		})
	}
	if text.Len() > 0 || len(output) == 0 {
		output = append(output, responsesOutMsg{
			Type: "message",
			Role: "assistant",
			Content: []responsesOutPart{{
				Type: "output_text",
				Text: text.String(),
			}},
		})
	}
	completed := struct {
		Type     string          `json:"type"`
		Response responsesOutput `json:"response"`
	}{
		Type: "response.completed",
		Response: responsesOutput{
			ID:         id,
			Object:     "response",
			Status:     "completed",
			Model:      model,
			Output:     output,
			OutputText: text.String(),
		},
	}
	raw, err := json.Marshal(completed)
	if err != nil {
		return err
	}
	if err := writeEvent("response.completed", string(raw)); err != nil {
		return err
	}
	return sc.Err()
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
