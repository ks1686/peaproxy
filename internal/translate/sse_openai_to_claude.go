package translate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// OpenAISSEToClaude converts chat.completion.chunk SSE into Anthropic Messages SSE.
func OpenAISSEToClaude(r io.Reader, w io.Writer, model string) error {
	started := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lastWrite time.Time
	writeEvent := func(event, data string) error {
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		lastWrite = sseNow()
		return err
	}
	var carried []opaqueEntry
	// Anthropic consumers finalize a tool_use input at its content_block_stop,
	// but OpenAI may interleave argument deltas across parallel tool calls. Text
	// streams into block 0; tool calls are buffered per OpenAI index and written
	// whole, in first-seen order, after the text block closes.
	var calls []*pendingToolUse
	callsByIndex := map[int]*pendingToolUse{}
	finish := ""
	var usage *claudeUsage
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		if msg, ok := openAIStreamError(payload); ok {
			upstream := fmt.Errorf("upstream stream error: %s", msg)
			if !started {
				return upstream
			}
			if werr := writeClaudeStreamError(writeEvent, msg); werr != nil {
				return errors.Join(upstream, werr)
			}
			return upstream
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content         string                 `json:"content"`
					ToolCalls       []openAIStreamToolCall `json:"tool_calls"`
					ReasoningOpaque json.RawMessage        `json:"reasoning_opaque"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = &claudeUsage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens}
		}
		if !started {
			started = true
			id := chunk.ID
			if id == "" {
				id = "msg_peaproxy"
			}
			start, err := claudeMessageStartJSON(id, model)
			if err != nil {
				return err
			}
			if err := writeEvent("message_start", start); err != nil {
				return err
			}
			if err := writeEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`); err != nil {
				return err
			}
		}
		if len(chunk.Choices) > 0 && len(chunk.Choices[0].Delta.ReasoningOpaque) > 0 {
			entries, err := parseOpaque(chunk.Choices[0].Delta.ReasoningOpaque)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if e.Kind == kindAnthropicThinking || e.Kind == kindAnthropicRedacted {
					carried = append(carried, e)
				}
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finish = choice.FinishReason
		}
		if choice.Delta.Content != "" {
			if err := writeClaudeDelta(writeEvent, 0, map[string]string{"type": "text_delta", "text": choice.Delta.Content}); err != nil {
				return err
			}
		}
		for _, tc := range choice.Delta.ToolCalls {
			call, seen := callsByIndex[tc.Index]
			if !seen {
				call = &pendingToolUse{}
				callsByIndex[tc.Index] = call
				calls = append(calls, call)
			}
			if tc.ID != "" {
				call.id = tc.ID
			}
			if tc.Function.Name != "" {
				call.name = tc.Function.Name
			}
			call.arguments.WriteString(tc.Function.Arguments)
		}
		// Buffered calls reach the client only at turn end (message_start is
		// already out). A tool-call delta that arrives at least
		// claudePingInterval after the last event written sends a ping, so an
		// idle-timeout consumer does not drop a tool call whose arguments are
		// still streaming. There is no timer: an upstream that goes fully quiet
		// gets no ping.
		if len(choice.Delta.ToolCalls) > 0 && sseNow().Sub(lastWrite) >= claudePingInterval {
			if err := writeEvent("ping", claudePing); err != nil {
				return err
			}
		}
	}
	if !started {
		// Empty upstream (or HTTPError before any bytes) — do not invent a turn.
		return sc.Err()
	}
	if err := sc.Err(); err != nil {
		// The upstream broke off mid-turn. Flushing the buffered tool calls and
		// a stop_reason would hand the client truncated arguments to run, so
		// the stream ends with Anthropic's error event instead.
		if werr := writeClaudeStreamError(writeEvent, ""); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	// A clean EOF without [DONE] still finishes the turn. ClaudeSSEToOpenAI
	// (the Anthropic adapters' chat wire) writes no [DONE] for a turn that
	// produced nothing, and the openai-compatible presets relay upstream bytes
	// as-is, so [DONE] is not guaranteed on every healthy stream. A connection
	// cut mid-body reaches here as a read error (io.ErrUnexpectedEOF from a
	// truncated chunked body), not as a clean EOF.
	if err := writeEvent("content_block_stop", `{"type":"content_block_stop","index":0}`); err != nil {
		return err
	}
	for i, call := range calls {
		if err := writeClaudeToolUseBlock(writeEvent, i+1, call); err != nil {
			return err
		}
	}
	for i, e := range carried {
		if err := writeClaudeOpaqueBlock(writeEvent, len(calls)+1+i, e); err != nil {
			return err
		}
	}
	delta, err := claudeMessageDeltaJSON(claudeStopReason(finish, len(calls) > 0), usage)
	if err != nil {
		return err
	}
	if err := writeEvent("message_delta", delta); err != nil {
		return err
	}
	return writeEvent("message_stop", `{"type":"message_stop"}`)
}

// writeClaudeStreamError ends a started Messages stream with Anthropic's error
// event. An empty message becomes the generic upstream failure text.
func writeClaudeStreamError(writeEvent func(event, data string) error, message string) error {
	if message == "" {
		message = upstreamStreamFailed
	}
	raw, err := json.Marshal(struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}{Type: "error", Error: struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}{Type: "api_error", Message: message}})
	if err != nil {
		return err
	}
	return writeEvent("error", string(raw))
}

func writeClaudeDelta(writeEvent func(event, data string) error, index int, delta any) error {
	raw, err := json.Marshal(struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
		Delta any    `json:"delta"`
	}{"content_block_delta", index, delta})
	if err != nil {
		return err
	}
	return writeEvent("content_block_delta", string(raw))
}

func writeClaudeOpaqueBlock(writeEvent func(event, data string) error, index int, e opaqueEntry) error {
	var raw []byte
	var err error
	switch e.Kind {
	case kindAnthropicRedacted:
		raw, err = json.Marshal(struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type string `json:"type"`
				Data string `json:"data"`
			} `json:"content_block"`
		}{
			Type:  "content_block_start",
			Index: index,
			ContentBlock: struct {
				Type string `json:"type"`
				Data string `json:"data"`
			}{Type: "redacted_thinking", Data: e.Data},
		})
	default:
		raw, err = json.Marshal(struct {
			Type         string `json:"type"`
			Index        int    `json:"index"`
			ContentBlock struct {
				Type      string `json:"type"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature,omitempty"`
			} `json:"content_block"`
		}{
			Type:  "content_block_start",
			Index: index,
			ContentBlock: struct {
				Type      string `json:"type"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature,omitempty"`
			}{Type: "thinking", Thinking: e.Thinking, Signature: e.Signature},
		})
	}
	if err != nil {
		return err
	}
	if err := writeEvent("content_block_start", string(raw)); err != nil {
		return err
	}
	stop, err := json.Marshal(struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}{Type: "content_block_stop", Index: index})
	if err != nil {
		return err
	}
	return writeEvent("content_block_stop", string(stop))
}

func claudeMessageStartJSON(id, model string) (string, error) {
	payload := struct {
		Type    string `json:"type"`
		Message struct {
			ID           string          `json:"id"`
			Type         string          `json:"type"`
			Role         string          `json:"role"`
			Model        string          `json:"model"`
			Content      json.RawMessage `json:"content"`
			StopReason   json.RawMessage `json:"stop_reason"`
			StopSequence json.RawMessage `json:"stop_sequence"`
			Usage        struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}{Type: "message_start"}
	payload.Message.ID = id
	payload.Message.Type = "message"
	payload.Message.Role = "assistant"
	payload.Message.Model = model
	payload.Message.Content = json.RawMessage("[]")
	payload.Message.StopReason = json.RawMessage("null")
	payload.Message.StopSequence = json.RawMessage("null")
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func claudeMessageDeltaJSON(stop string, usage *claudeUsage) (string, error) {
	usageRaw := json.RawMessage(`{"output_tokens":0}`)
	if usage != nil {
		raw, err := json.Marshal(struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		}{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens})
		if err != nil {
			return "", err
		}
		usageRaw = raw
	}
	type deltaBody struct {
		StopReason   string          `json:"stop_reason"`
		StopSequence json.RawMessage `json:"stop_sequence"`
	}
	raw, err := json.Marshal(struct {
		Type  string          `json:"type"`
		Delta deltaBody       `json:"delta"`
		Usage json.RawMessage `json:"usage"`
	}{
		Type:  "message_delta",
		Delta: deltaBody{StopReason: stop, StopSequence: json.RawMessage("null")},
		Usage: usageRaw,
	})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
