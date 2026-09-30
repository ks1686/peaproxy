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
			if err := writeEvent("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":%s,"type":"message","role":"assistant","model":%s,"content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`, jsonString(id), jsonString(model))); err != nil {
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
		if werr := writeEvent("error", `{"type":"error","error":{"type":"api_error","message":"upstream stream failed"}}`); werr != nil {
			return errors.Join(err, werr)
		}
		return err
	}
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
	deltaUsage := `{"output_tokens":0}`
	if usage != nil {
		deltaUsage = fmt.Sprintf(`{"input_tokens":%d,"output_tokens":%d}`, usage.InputTokens, usage.OutputTokens)
	}
	if err := writeEvent("message_delta", fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":%s,"stop_sequence":null},"usage":%s}`, jsonString(claudeStopReason(finish, len(calls) > 0)), deltaUsage)); err != nil {
		return err
	}
	return writeEvent("message_stop", `{"type":"message_stop"}`)
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
	var carried []responsesOutMsg
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
				carried = append(carried, responsesOutMsg{
					Type:             "reasoning",
					ID:               wire.ID,
					EncryptedContent: wire.EncryptedContent,
					Summary:          wire.Summary,
					Status:           wire.Status,
				})
			}
		}
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
	if err := sc.Err(); err != nil {
		// The upstream broke off mid-turn: response.completed would hand the
		// client the truncated function_call arguments to run.
		return err
	}
	output := make([]responsesOutMsg, 0, len(carried)+len(order)+1)
	output = append(output, carried...)
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
		},
	}
	raw, err := json.Marshal(terminal)
	if err != nil {
		return err
	}
	return writeEvent(event, string(raw))
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
