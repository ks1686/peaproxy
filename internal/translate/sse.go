package translate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
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

const upstreamStreamFailed = "upstream stream failed"

// openAIStreamError reports whether an OpenAI-wire data payload carries a
// non-null top-level error, and its message. OpenAI sends
// {"error":{"message","type","code"}}; OpenRouter adds it to a chunk that also
// has choices (finish_reason "error"); some servers send a bare string.
func openAIStreamError(payload string) (string, bool) {
	var body struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(payload), &body) != nil || len(body.Error) == 0 || string(body.Error) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(body.Error, &s) == nil {
		if s == "" {
			s = upstreamStreamFailed
		}
		return s, true
	}
	var obj struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code"`
	}
	if json.Unmarshal(body.Error, &obj) != nil {
		return string(body.Error), true
	}
	switch {
	case obj.Message != "":
		return obj.Message, true
	case obj.Type != "":
		return obj.Type, true
	case len(obj.Code) > 0 && string(obj.Code) != "null":
		return strings.Trim(string(obj.Code), `"`), true
	}
	return upstreamStreamFailed, true
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

// writeResponsesFailed ends a started Responses stream with response.failed.
// An empty message becomes the generic upstream failure text.
func writeResponsesFailed(writeEvent func(event, data string) error, id, model, message string) error {
	if message == "" {
		message = upstreamStreamFailed
	}
	failed := struct {
		Type     string `json:"type"`
		Response struct {
			ID     string `json:"id"`
			Object string `json:"object"`
			Status string `json:"status"`
			Model  string `json:"model"`
			Error  struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}{Type: "response.failed"}
	failed.Response.ID = id
	failed.Response.Object = "response"
	failed.Response.Status = "failed"
	failed.Response.Model = model
	failed.Response.Error.Code = "server_error"
	failed.Response.Error.Message = message
	raw, err := json.Marshal(failed)
	if err != nil {
		return err
	}
	return writeEvent("response.failed", string(raw))
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

type openAIChatDelta struct {
	Role            string                 `json:"role,omitempty"`
	Content         string                 `json:"content,omitempty"`
	ToolCalls       []openAIStreamToolCall `json:"tool_calls,omitempty"`
	ReasoningOpaque json.RawMessage        `json:"reasoning_opaque,omitempty"`
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

// WriteOpenAIChatSSEOpaque writes one assistant delta that carries reasoning_opaque
// and no visible text.
func WriteOpenAIChatSSEOpaque(w io.Writer, id, model string, opaque json.RawMessage) error {
	if len(opaque) == 0 {
		return nil
	}
	return writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{ReasoningOpaque: opaque}, "")
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

// openAIFinishFromClaude checks truncation and refusal before tool use: a turn
// cut off or blocked inside a tool call must not be handed to the client to run.
func openAIFinishFromClaude(stopReason string, hadToolCalls bool) string {
	switch stopReason {
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	}
	return openAIChatFinishReason(hadToolCalls || stopReason == "tool_use")
}

const openAIStreamFailedChunk = `data: {"error":{"message":"upstream stream failed","type":"api_error"}}` + "\n\n"

// ClaudeSSEToOpenAI converts Anthropic SSE into chat.completion.chunk SSE.
func ClaudeSSEToOpenAI(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	id := "chatcmpl-peaproxy"
	model := ""
	wrote := false
	wroteRole := false
	hadToolCalls := false
	stopReason := ""
	type thinkAcc struct {
		kind, thinking, signature, data string
	}
	accs := map[int]*thinkAcc{}
	var accOrder []int
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
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Message *struct {
				ID    string `json:"id"`
				Model string `json:"model"`
			} `json:"message"`
			ContentBlock *struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				Data      string `json:"data"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
			} `json:"content_block"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "error":
			upstream := fmt.Errorf("anthropic stream error: %s", payload)
			if ev.Error != nil {
				upstream = fmt.Errorf("anthropic stream error: %s: %s", ev.Error.Type, ev.Error.Message)
			}
			if wroteRole {
				if _, err := io.WriteString(w, openAIStreamFailedChunk); err != nil {
					return errors.Join(upstream, err)
				}
			}
			return upstream
		case "message_start":
			if ev.Message != nil {
				if ev.Message.ID != "" {
					id = ev.Message.ID
				}
				model = ev.Message.Model
			}
		case "content_block_start":
			if ev.ContentBlock != nil && (ev.ContentBlock.Type == "thinking" || ev.ContentBlock.Type == "redacted_thinking") {
				kind := kindAnthropicThinking
				if ev.ContentBlock.Type == "redacted_thinking" {
					kind = kindAnthropicRedacted
				}
				if _, ok := accs[ev.Index]; !ok {
					accOrder = append(accOrder, ev.Index)
				}
				accs[ev.Index] = &thinkAcc{
					kind:      kind,
					thinking:  ev.ContentBlock.Thinking,
					signature: ev.ContentBlock.Signature,
					data:      ev.ContentBlock.Data,
				}
				continue
			}
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
			if ev.Delta.Type == "thinking_delta" || ev.Delta.Type == "signature_delta" {
				acc := accs[ev.Index]
				if acc == nil {
					acc = &thinkAcc{kind: kindAnthropicThinking}
					accs[ev.Index] = acc
					accOrder = append(accOrder, ev.Index)
				}
				if ev.Delta.Type == "thinking_delta" {
					acc.thinking += ev.Delta.Thinking
				} else if ev.Delta.Signature != "" {
					acc.signature = ev.Delta.Signature
				}
				continue
			}
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
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
			if ev.Delta.StopReason == "tool_use" {
				hadToolCalls = true
				wrote = true
			}
		default:
			// ignore ping, content_block_stop, message_stop
		}
	}
	if err := sc.Err(); err != nil {
		// The upstream broke off mid-turn: a finish chunk would hand the client
		// truncated tool arguments to run.
		if wroteRole {
			if _, werr := io.WriteString(w, openAIStreamFailedChunk); werr != nil {
				return errors.Join(err, werr)
			}
		}
		return err
	}
	var entries []opaqueEntry
	for _, idx := range accOrder {
		acc := accs[idx]
		if acc == nil {
			continue
		}
		if acc.kind == kindAnthropicRedacted {
			entries = append(entries, opaqueEntry{Kind: kindAnthropicRedacted, Data: acc.data})
			continue
		}
		entries = append(entries, opaqueEntry{Kind: kindAnthropicThinking, Thinking: acc.thinking, Signature: acc.signature})
	}
	opaqueRaw, err := marshalOpaque(entries)
	if err != nil {
		return err
	}
	if len(opaqueRaw) > 0 {
		wrote = true
	}
	if wrote {
		if err := writeRole(); err != nil {
			return err
		}
		if len(opaqueRaw) > 0 {
			if err := writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{ReasoningOpaque: opaqueRaw}, ""); err != nil {
				return err
			}
		}
		if err := WriteOpenAIChatSSEFinish(w, id, model, openAIFinishFromClaude(stopReason, hadToolCalls)); err != nil {
			return err
		}
	}
	return nil
}

// OpenAISSEToResponses converts chat.completion.chunk SSE into Responses API SSE.
// Text deltas become response.output_text.delta. Chat tool_calls are mapped to
// function_call output items on response.completed (plus argument deltas when
// present); a length or content_filter finish ends on response.incomplete
// instead. This does not execute tools or invent tool results.
func OpenAISSEToResponses(r io.Reader, w io.Writer, model string) error {
	started := false
	id := "resp_peaproxy"
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Every Responses event carries a strictly increasing sequence_number. An
	// accumulator that reorders on it then rebuilds the same turn the terminal
	// array describes (#62).
	var seq int
	// emit writes one event, stamping the next sequence_number into the
	// payload. The payload is built with a placeholder so the counter is
	// allocated exactly once per event.
	emit := func(event, data string) error {
		// Insert the counter straight after the opening brace rather than
		// splicing onto the end: every event here is a flat object, and the end
		// may be a nested object whose braces must survive.
		if body := strings.TrimPrefix(data, "{"); body != data {
			data = fmt.Sprintf(`{"sequence_number":%d,%s`, seq, body)
		}
		seq++
		_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		return err
	}

	var text strings.Builder
	// Token counts from the upstream stream, carried into the terminal event.
	promptTokens, completionTokens := 0, 0
	type pendingCall struct {
		ID, Name, Arguments string
		Started             bool
		ItemID              string
		Index               int
		Closed              bool
	}
	calls := map[int]*pendingCall{}
	var order []int
	var carried []responsesOutMsg
	// messageStarted gates the message item's own lifecycle events, and
	// messageIndex is fixed the first time a text delta arrives -- never
	// renumbered afterwards.
	messageStarted, messageClosed := false, false
	messageIndex, messageItemID := 0, "msg_0"
	// itemAt records the output_index each item id was announced under, so the
	// terminal array can be assembled in the same order. It is per translation
	// rather than package level on purpose: a shared map would race between
	// concurrent streams and grow without bound.
	itemAt := map[string]int{}
	handedOut := 0
	nextIndex := func() int { return handedOut }
	itemIndex := func(id string) int {
		if n, ok := itemAt[id]; ok {
			return n
		}
		return handedOut
	}
	lastFinish := ""
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
			if werr := writeResponsesFailed(emit, id, model, msg); werr != nil {
				return errors.Join(upstream, werr)
			}
			return upstream
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content         string          `json:"content"`
					ReasoningOpaque json.RawMessage `json:"reasoning_opaque"`
					ToolCalls       []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		// Providers report usage on the final chunk, but not always the last
		// one, so it is taken as the last non-zero reading rather than only on
		// the terminal frame.
		if u := chunk.Usage; u.PromptTokens > 0 || u.CompletionTokens > 0 {
			promptTokens, completionTokens = u.PromptTokens, u.CompletionTokens
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
			if err := emit("response.created", string(raw)); err != nil {
				return err
			}
			if err := emit("response.in_progress", fmt.Sprintf(`{"type":"response.in_progress","response":%s}`, responseStub(id, model))); err != nil {
				return err
			}
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if f := chunk.Choices[0].FinishReason; f != "" {
			lastFinish = f
		}
		delta := chunk.Choices[0].Delta
		if len(delta.ReasoningOpaque) > 0 {
			items, err := ResponsesReasoningItems(delta.ReasoningOpaque)
			if err != nil {
				return err
			}
			for _, item := range items {
				var wire responsesReasoningWire
				if err := json.Unmarshal(item, &wire); err != nil {
					return err
				}
				rid := wire.ID
				if rid == "" {
					rid = fmt.Sprintf("rs_%d", len(carried))
				}
				itemAt[rid] = nextIndex()
				handedOut++
				carried = append(carried, responsesOutMsg{
					Type:             "reasoning",
					ID:               rid,
					EncryptedContent: wire.EncryptedContent,
					Summary:          wire.Summary,
					Status:           wire.Status,
				})
			}
		}
		if delta.Content != "" {
			if !messageStarted {
				// The message item gets its index now, once, and keeps it. Every
				// later delta quotes the same item_id and output_index, which is
				// what lets an accumulator place this text in the terminal array.
				messageStarted = true
				messageIndex = nextIndex()
				handedOut++
				itemAt[messageItemID] = messageIndex
				if err := emit("response.output_item.added", fmt.Sprintf(
					`{"type":"response.output_item.added","output_index":%d,"item":{"type":"message","id":%q,"role":"assistant","content":[]}}`,
					messageIndex, messageItemID)); err != nil {
					return err
				}
				if err := emit("response.content_part.added", fmt.Sprintf(
					`{"type":"response.content_part.added","item_id":%q,"output_index":%d,"content_index":0,"part":{"type":"output_text","text":""}}`,
					messageItemID, messageIndex)); err != nil {
					return err
				}
			}
			text.WriteString(delta.Content)
			ev := struct {
				Type         string `json:"type"`
				ItemID       string `json:"item_id"`
				OutputIndex  int    `json:"output_index"`
				ContentIndex int    `json:"content_index"`
				Delta        string `json:"delta"`
			}{Type: "response.output_text.delta", ItemID: messageItemID, OutputIndex: messageIndex, Delta: delta.Content}
			raw, err := json.Marshal(ev)
			if err != nil {
				return err
			}
			if err := emit("response.output_text.delta", string(raw)); err != nil {
				return err
			}
		}
		for _, tc := range delta.ToolCalls {
			pc, ok := calls[tc.Index]
			if !ok {
				pc = &pendingCall{}
				// The index is fixed here, when the item is created, so the
				// argument delta below quotes the same output_index as the
				// output_item.added that follows it.
				pc.Index = nextIndex()
				handedOut++
				pc.ItemID = "fc_" + strconv.Itoa(pc.Index)
				itemAt[pc.ItemID] = pc.Index
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
					Type        string `json:"type"`
					ItemID      string `json:"item_id"`
					OutputIndex int    `json:"output_index"`
					Delta       string `json:"delta"`
				}{Type: "response.function_call_arguments.delta", ItemID: pc.ItemID, OutputIndex: pc.Index, Delta: tc.Function.Arguments}
				raw, err := json.Marshal(ev)
				if err != nil {
					return err
				}
				if err := emit("response.function_call_arguments.delta", string(raw)); err != nil {
					return err
				}
			}
			if !pc.Started && (pc.ID != "" || pc.Name != "") {
				// The item was created (and indexed) on the first delta; this is
				// only the point at which it has enough to be announced.
				pc.Started = true
				item := struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Type: "function_call", ID: pc.ItemID, CallID: pc.ID, Name: pc.Name, Arguments: pc.Arguments}
				added := struct {
					Type        string `json:"type"`
					OutputIndex int    `json:"output_index"`
					Item        struct {
						Type      string `json:"type"`
						ID        string `json:"id"`
						CallID    string `json:"call_id"`
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"item"`
				}{Type: "response.output_item.added", OutputIndex: pc.Index, Item: item}
				raw, err := json.Marshal(added)
				if err != nil {
					return err
				}
				if err := emit("response.output_item.added", string(raw)); err != nil {
					return err
				}
			}
		}
	}
	if !started {
		return sc.Err()
	}
	if err := sc.Err(); err != nil {
		// The upstream broke off mid-turn: response.completed would hand the
		// client the truncated function_call arguments to run. Emit
		// response.failed so a Responses client sees a terminal event instead
		// of a bare EOF after response.created.
		if werr := writeResponsesFailed(emit, id, model, ""); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
	// Close each item before the terminal event, in the same order the indices
	// were handed out. An accumulator that has been tracking item_ids needs the
	// .done events to know the arguments are complete rather than truncated
	// because the socket closed.
	for _, idx := range order {
		pc := calls[idx]
		if pc.Closed || !pc.Started {
			continue
		}
		pc.Closed = true
		if err := emit("response.function_call_arguments.done", fmt.Sprintf(
			`{"type":"response.function_call_arguments.done","item_id":%q,"output_index":%d,"arguments":%q}`,
			pc.ItemID, pc.Index, pc.Arguments)); err != nil {
			return err
		}
		if err := emit("response.output_item.done", fmt.Sprintf(
			`{"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":%q,"call_id":%q,"name":%q,"arguments":%q}}`,
			pc.Index, pc.ItemID, pc.ID, pc.Name, pc.Arguments)); err != nil {
			return err
		}
	}
	if messageStarted && !messageClosed {
		if err := emit("response.output_text.done", fmt.Sprintf(
			`{"type":"response.output_text.done","item_id":%q,"output_index":%d,"content_index":0,"text":%q}`,
			messageItemID, messageIndex, text.String())); err != nil {
			return err
		}
		if err := emit("response.content_part.done", fmt.Sprintf(
			`{"type":"response.content_part.done","item_id":%q,"output_index":%d,"content_index":0,"part":{"type":"output_text","text":%q}}`,
			messageItemID, messageIndex, text.String())); err != nil {
			return err
		}
		if err := emit("response.output_item.done", fmt.Sprintf(
			`{"type":"response.output_item.done","output_index":%d,"item":{"type":"message","id":%q,"role":"assistant","content":[{"type":"output_text","text":%q}]}}`,
			messageIndex, messageItemID, text.String())); err != nil {
			return err
		}
	}

	// A clean EOF without [DONE] completes, as in OpenAISSEToClaude.
	//
	// The array is assembled in index order, not grouped by kind: grouping it
	// is what used to make the streamed indices disagree with the terminal
	// object, and an accumulator that trusts them would rebuild a different
	// turn than the provider described (#62).
	items := make([]responsesOutMsg, 0, len(carried)+len(order)+1)
	if messageStarted {
		items = append(items, responsesOutMsg{
			Type: "message",
			ID:   messageItemID,
			Role: "assistant",
			Content: []responsesOutPart{{
				Type: "output_text",
				Text: text.String(),
			}},
		})
	}
	for _, idx := range order {
		pc := calls[idx]
		items = append(items, responsesOutMsg{
			Type:      "function_call",
			ID:        pc.ItemID,
			CallID:    pc.ID,
			Name:      pc.Name,
			Arguments: pc.Arguments,
		})
	}
	items = append(items, carried...)
	if !messageStarted {
		// No text arrived, so the message item was never opened and no index
		// was spent on it. It still has to appear, so give it the slot the
		// client expects: first.
		items = append([]responsesOutMsg{{
			Type: "message",
			ID:   messageItemID,
			Role: "assistant",
			Content: []responsesOutPart{{
				Type: "output_text",
				Text: text.String(),
			}},
		}}, items...)
	}
	// Put them in index order. A stable sort with an unknown index last keeps
	// anything the stream never announced where the client already expects it.
	sort.SliceStable(items, func(a, b int) bool { return itemIndex(items[a].ID) < itemIndex(items[b].ID) })
	output := items
	status, details := responsesStatus(lastFinish)
	event := "response.completed"
	if status == "incomplete" {
		event = "response.incomplete"
	}
	terminal := struct {
		Type     string          `json:"type"`
		Response responsesOutput `json:"response"`
	}{
		Type: event,
		Response: responsesOutput{
			ID:                id,
			Object:            "response",
			Status:            status,
			IncompleteDetails: details,
			Model:             model,
			Output:            output,
			OutputText:        text.String(),
			Usage:             responsesUsageFor(promptTokens, completionTokens),
		},
	}
	raw, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	return emit(event, string(raw))
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

// responseStub is the minimal response object carried by the lifecycle events
// that are not the terminal one. The client only reads identity off it; the
// full object arrives with response.completed.
func responseStub(id, model string) string {
	return fmt.Sprintf(`{"id":%q,"object":"response","status":"in_progress","model":%q}`, id, model)
}

// WriteOpenAIChatSSEError reports a failure that happened after the stream had
// already started.
//
// A cut stream must be told apart from a quiet finish. Emitting a
// finish_reason here would tell the client the answer is complete when it is
// not, which is the one thing this proxy must never do: a client that believes
// a truncated answer is finished will act on it. So no terminal event is
// written, only an error the client can surface and the operator can find in
// the log.
func WriteOpenAIChatSSEError(w io.Writer, message string) error {
	payload, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_stream_error",
			"code":    "stream_incomplete",
		},
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, "data: "+string(payload)+"\n\n")
	return err
}
