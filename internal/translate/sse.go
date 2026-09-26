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

// ClaudeSSEToOpenAI converts Anthropic SSE into chat.completion.chunk SSE.
func ClaudeSSEToOpenAI(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	id := "chatcmpl-peaproxy"
	model := ""
	wrote := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			Message *struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"message"`
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
		case "content_block_delta":
			if ev.Delta.Text == "" {
				continue
			}
			chunk := struct {
				ID      string `json:"id"`
				Object  string `json:"object"`
				Model   string `json:"model"`
				Choices []struct {
					Index int `json:"index"`
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}{ID: id, Object: "chat.completion.chunk", Model: model}
			chunk.Choices = make([]struct {
				Index int `json:"index"`
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			}, 1)
			chunk.Choices[0].Delta.Content = ev.Delta.Text
			raw, err := json.Marshal(chunk)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return err
			}
			wrote = true
		default:
			// ignore ping, content_block_start/stop, message_delta, message_stop
		}
	}
	if wrote {
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}
	return sc.Err()
}

// OpenAISSEToResponses converts chat.completion.chunk SSE into Responses API SSE.
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
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			delta := chunk.Choices[0].Delta.Content
			text.WriteString(delta)
			ev := struct {
				Type  string `json:"type"`
				Delta string `json:"delta"`
			}{Type: "response.output_text.delta", Delta: delta}
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if err := writeEvent("response.output_text.delta", string(raw)); err != nil {
				return err
			}
		}
	}
	if !started {
		return sc.Err()
	}
	completed := struct {
		Type     string          `json:"type"`
		Response responsesOutput `json:"response"`
	}{
		Type: "response.completed",
		Response: responsesOutput{
			ID:     id,
			Object: "response",
			Status: "completed",
			Model:  model,
			Output: []responsesOutMsg{{
				Type: "message",
				Role: "assistant",
				Content: []responsesOutPart{{
					Type: "output_text",
					Text: text.String(),
				}},
			}},
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
