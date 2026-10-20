package translate

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
	"github.com/ks1686/peaproxy/internal/jsonx"
)

type responsesInput struct {
	Role    string          `json:"role"`
	Content []responsesPart `json:"content"`
}

type responsesPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesBody struct {
	Model        string            `json:"model"`
	Instructions string            `json:"instructions,omitempty"`
	Input        []json.RawMessage `json:"input"`
	Stream       bool              `json:"stream"`
	Tools        json.RawMessage   `json:"tools,omitempty"`
	ToolChoice   json.RawMessage   `json:"tool_choice,omitempty"`

	ServiceTier       string              `json:"service_tier,omitempty"`
	Reasoning         *responsesReasoning `json:"reasoning,omitempty"`
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"`
	// Text carries responses-only fields. It is omitted entirely when empty so
	// an absent verbosity does not add a "text":{} block to every request.
	Text *responsesText `json:"text,omitempty"`
	// PromptCacheKey is what keeps a long conversation's prefix cached across
	// turns. Codex always sends one.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

type responsesText struct {
	Verbosity string `json:"verbosity,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

// chatCarry holds the chat request fields that have a Responses equivalent.
type chatCarry struct {
	ServiceTier       string
	ReasoningEffort   string
	ParallelToolCalls *bool
	// Verbosity is only carried for a model that implements it; PromptCacheKey
	// only when it is usable. Both are validated in chatToResponses, so nothing
	// downstream has to check them (#60).
	Verbosity      string
	PromptCacheKey string
}

func (c chatCarry) apply(body *responsesBody) {
	body.ServiceTier = normalizeServiceTier(c.ServiceTier)
	if c.ReasoningEffort != "" {
		body.Reasoning = &responsesReasoning{Effort: c.ReasoningEffort}
	}
	body.ParallelToolCalls = c.ParallelToolCalls
	if c.Verbosity != "" {
		body.Text = &responsesText{Verbosity: c.Verbosity}
	}
	body.PromptCacheKey = c.PromptCacheKey
}

// verbosityLevels are the three values Codex sends. Anything else would be
// rejected upstream, so it is dropped rather than forwarded.
var verbosityLevels = map[string]bool{"low": true, "medium": true, "high": true}

// maxPromptCacheKey is the bound upstream puts on the field. It travels in a
// header, so an unbounded key is a way to send an unbounded header.
const maxPromptCacheKey = 256

// carryVerbosity keeps verbosity only for a model that implements it. o3,
// o4-mini and gpt-4.1 have no text.verbosity and reject the field.
func carryVerbosity(requested, model string) string {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if !verbosityLevels[requested] || !catalog.SupportsVerbosity(model) {
		return ""
	}
	return requested
}

// carryPromptCacheKey keeps the key when it is short enough and free of control
// bytes. A control byte in a header-sized field is an injection attempt, not a
// cache key.
func carryPromptCacheKey(key string) string {
	if key == "" || len(key) > maxPromptCacheKey {
		return ""
	}
	for _, b := range []byte(key) {
		if b < 0x20 || b == 0x7f {
			return ""
		}
	}
	return key
}

// normalizeServiceTier maps a chat service_tier onto the values the ChatGPT
// backend accepts. Codex omits the field for the default tier, so every other
// value is dropped rather than risk a 400.
func normalizeServiceTier(s string) string {
	switch s {
	case "fast", "priority":
		return "priority"
	case "flex":
		return "flex"
	}
	return ""
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionCallOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatInboundMessage struct {
	Role            string          `json:"role"`
	Content         json.RawMessage `json:"content"`
	ToolCalls       []chatToolCall  `json:"tool_calls"`
	ToolCallID      string          `json:"tool_call_id"`
	ReasoningOpaque json.RawMessage `json:"reasoning_opaque"`
}

type chatStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type chatStreamDelta struct {
	Content   string               `json:"content,omitempty"`
	ToolCalls []chatStreamToolCall `json:"tool_calls,omitempty"`
}

type chatChoiceMessage struct {
	Role            string          `json:"role"`
	Content         json.RawMessage `json:"content"`
	ToolCalls       []chatToolCall  `json:"tool_calls,omitempty"`
	ReasoningOpaque json.RawMessage `json:"reasoning_opaque,omitempty"`
}

// ChatToResponses converts a Chat Completions request to the native Responses
// request shape, preserving tools, images, prior assistant turns, and opaque reasoning.
func ChatToResponses(raw []byte, model string, stream bool) ([]byte, error) {
	return chatToResponses(raw, model, stream)
}

func chatToResponses(raw []byte, model string, stream bool) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if bytes.Contains(raw, []byte(`"input"`)) && !bytes.Contains(raw, []byte(`"messages"`)) {
		return PrepareResponses(raw, stream), nil
	}
	var parsed struct {
		Model             string               `json:"model"`
		Tools             json.RawMessage      `json:"tools"`
		ToolChoice        json.RawMessage      `json:"tool_choice"`
		Messages          []chatInboundMessage `json:"messages"`
		ServiceTier       string               `json:"service_tier"`
		ReasoningEffort   string               `json:"reasoning_effort"`
		ParallelToolCalls *bool                `json:"parallel_tool_calls"`
		Verbosity         string               `json:"verbosity"`
		PromptCacheKey    string               `json:"prompt_cache_key"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed.Model != "" {
		model = parsed.Model
	}
	carry := chatCarry{
		ServiceTier:       parsed.ServiceTier,
		ReasoningEffort:   parsed.ReasoningEffort,
		ParallelToolCalls: parsed.ParallelToolCalls,
		Verbosity:         carryVerbosity(parsed.Verbosity, model),
		PromptCacheKey:    carryPromptCacheKey(parsed.PromptCacheKey),
	}
	if len(parsed.Tools) > 0 || len(parsed.ToolChoice) > 0 || chatMessagesHaveTools(parsed.Messages) || chatMessagesHaveOpaque(parsed.Messages) || chatMessagesHaveImages(parsed.Messages) {
		return chatToResponsesWithTools(model, parsed.Messages, parsed.Tools, parsed.ToolChoice, carry, stream)
	}
	msgs := make([]adapter.Message, 0, len(parsed.Messages))
	for _, m := range parsed.Messages {
		msgs = append(msgs, adapter.Message{Role: m.Role, Content: messageContentString(m.Content)})
	}
	return chatToResponsesFromMessages(model, msgs, carry, stream)
}

func chatMessagesHaveOpaque(msgs []chatInboundMessage) bool {
	for _, m := range msgs {
		raw := bytes.TrimSpace(m.ReasoningOpaque)
		if len(raw) > 0 && string(raw) != "null" && string(raw) != "[]" {
			return true
		}
	}
	return false
}

func chatMessagesHaveTools(msgs []chatInboundMessage) bool {
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 || m.ToolCallID != "" || strings.EqualFold(m.Role, "tool") {
			return true
		}
	}
	return false
}

func PrepareResponses(raw []byte, stream bool) []byte {
	// chatgpt.com Codex OAuth rejects stream_options, max_output_tokens, and
	// any store value other than false (omit or true → HTTP 400).
	// OpenAI accepts input as a string; Codex requires a message list.
	raw = jsonx.DropTopLevelKeys(raw, "stream_options", "max_output_tokens", "store")
	raw = coerceResponsesInput(raw)
	raw = dropInputPartKey(raw, "prompt_cache_breakpoint")
	raw = jsonx.SetStream(raw, stream)
	return jsonx.SetBool(raw, "store", false)
}

// dropInputPartKey removes key from every content part of every input item.
// Codex also rejects the explicit prompt-cache breakpoints api.openai.com
// accepts on input parts.
func dropInputPartKey(raw []byte, key string) []byte {
	if !bytes.Contains(raw, []byte(`"`+key+`"`)) {
		return raw
	}
	var probe struct {
		Input []json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.Input) == 0 {
		return raw
	}
	changed := false
	for i, item := range probe.Input {
		next := jsonx.DropKeyInArray(item, "content", key)
		if !bytes.Equal(next, bytes.TrimSpace(item)) {
			probe.Input[i] = next
			changed = true
		}
	}
	if !changed {
		return raw
	}
	// Joined by hand: json.Marshal would HTML-escape the items' text.
	items := make([][]byte, len(probe.Input))
	for i, item := range probe.Input {
		items[i] = item
	}
	list := append([]byte{'['}, bytes.Join(items, []byte{','})...)
	return jsonx.SetTopLevelRaw(raw, "input", append(list, ']'))
}

func coerceResponsesInput(raw []byte) []byte {
	var probe struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return raw
	}
	in := bytes.TrimSpace(probe.Input)
	if len(in) == 0 || in[0] != '"' {
		return raw
	}
	var text string
	if json.Unmarshal(in, &text) != nil {
		return raw
	}
	item, err := marshalResponsesMessage("user", "input_text", text)
	if err != nil {
		return raw
	}
	list, err := json.Marshal([]json.RawMessage{item})
	if err != nil {
		return raw
	}
	return jsonx.SetTopLevelRaw(raw, "input", list)
}

// ChatToResponsesFromMessages converts already-parsed chat messages into a
// Responses request. It is the fallback for a caller that has messages but no
// raw chat body.
func ChatToResponsesFromMessages(model string, msgs []adapter.Message, stream bool) ([]byte, error) {
	return chatToResponsesFromMessages(model, msgs, chatCarry{}, stream)
}

func chatToResponsesFromMessages(model string, msgs []adapter.Message, carry chatCarry, stream bool) ([]byte, error) {
	var instr strings.Builder
	var input []json.RawMessage
	for _, m := range msgs {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if instr.Len() > 0 {
				instr.WriteByte('\n')
			}
			instr.WriteString(m.Content)
		case "assistant":
			raw, err := marshalResponsesMessage("assistant", "output_text", m.Content)
			if err != nil {
				return nil, err
			}
			input = append(input, raw)
		default:
			raw, err := marshalResponsesMessage("user", "input_text", m.Content)
			if err != nil {
				return nil, err
			}
			input = append(input, raw)
		}
	}
	body := responsesBody{
		Model:        model,
		Instructions: instr.String(),
		Input:        input,
		Stream:       stream,
	}
	carry.apply(&body)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return PrepareResponses(raw, stream), nil
}

func chatToResponsesWithTools(model string, msgs []chatInboundMessage, tools, toolChoice json.RawMessage, carry chatCarry, stream bool) ([]byte, error) {
	var instr strings.Builder
	var input []json.RawMessage
	for _, m := range msgs {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if instr.Len() > 0 {
				instr.WriteByte('\n')
			}
			instr.WriteString(messageContentString(m.Content))
		case "tool":
			item, err := json.Marshal(responsesFunctionCallOutput{
				Type:   "function_call_output",
				CallID: m.ToolCallID,
				Output: messageContentString(m.Content),
			})
			if err != nil {
				return nil, err
			}
			input = append(input, item)
		case "assistant":
			items, err := ResponsesReasoningItems(m.ReasoningOpaque)
			if err != nil {
				return nil, err
			}
			input = append(input, items...)
			text := messageContentString(m.Content)
			if text != "" {
				raw, err := marshalResponsesMessage("assistant", "output_text", text)
				if err != nil {
					return nil, err
				}
				input = append(input, raw)
			}
			for _, tc := range m.ToolCalls {
				item, err := json.Marshal(responsesFunctionCall{
					Type:      "function_call",
					CallID:    tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
				if err != nil {
					return nil, err
				}
				input = append(input, item)
			}
			if text == "" && len(m.ToolCalls) == 0 && len(items) == 0 {
				raw, err := marshalResponsesMessage("assistant", "output_text", "")
				if err != nil {
					return nil, err
				}
				input = append(input, raw)
			}
		default:
			raw, err := marshalResponsesParts("user", contentParts(m.Content, "input_text"))
			if err != nil {
				return nil, err
			}
			input = append(input, raw)
		}
	}
	body := responsesBody{
		Model:        model,
		Instructions: instr.String(),
		Input:        input,
		Stream:       stream,
		Tools:        chatToolsToResponses(tools),
		ToolChoice:   chatToolChoiceToResponses(toolChoice),
	}
	carry.apply(&body)
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return PrepareResponses(raw, stream), nil
}

func marshalResponsesMessage(role, partType, text string) (json.RawMessage, error) {
	return marshalResponsesParts(role, []responsesPart{{Type: partType, Text: text}})
}

func marshalResponsesParts(role string, parts []responsesPart) (json.RawMessage, error) {
	if len(parts) == 0 {
		parts = []responsesPart{{Type: "input_text", Text: ""}}
	}
	return json.Marshal(responsesInput{Role: role, Content: parts})
}

func chatMessagesHaveImages(msgs []chatInboundMessage) bool {
	for _, m := range msgs {
		for _, part := range contentParts(m.Content, "input_text") {
			if part.Type == "input_image" {
				return true
			}
		}
	}
	return false
}

func contentParts(raw json.RawMessage, textType string) []responsesPart {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return []responsesPart{{Type: textType, Text: s}}
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return []responsesPart{{Type: textType, Text: messageContentString(raw)}}
	}
	var out []responsesPart
	for _, p := range parts {
		switch p.Type {
		case "image_url", "input_image":
			if url := imageURLValue(p.ImageURL); url != "" {
				out = append(out, responsesPart{Type: "input_image", ImageURL: url})
			}
		default:
			if p.Text != "" {
				out = append(out, responsesPart{Type: textType, Text: p.Text})
			}
		}
	}
	if len(out) == 0 {
		if text := messageContentString(raw); text != "" {
			return []responsesPart{{Type: textType, Text: text}}
		}
	}
	return out
}

func imageURLValue(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.URL
	}
	return ""
}

func chatToolsToResponses(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		var parsed struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Function    *struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if json.Unmarshal(item, &parsed) != nil {
			out = append(out, item)
			continue
		}
		if parsed.Function != nil && parsed.Function.Name != "" {
			flat := struct {
				Type        string          `json:"type"`
				Name        string          `json:"name"`
				Description string          `json:"description,omitempty"`
				Parameters  json.RawMessage `json:"parameters,omitempty"`
			}{Type: "function", Name: parsed.Function.Name, Description: parsed.Function.Description, Parameters: parsed.Function.Parameters}
			b, err := json.Marshal(flat)
			if err != nil {
				out = append(out, item)
				continue
			}
			out = append(out, b)
			continue
		}
		out = append(out, item)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return raw
	}
	return b
}

func chatToolChoiceToResponses(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		return raw
	}
	var parsed struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return raw
	}
	if parsed.Function != nil && parsed.Function.Name != "" {
		b, err := json.Marshal(struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "function", Name: parsed.Function.Name})
		if err != nil {
			return raw
		}
		return b
	}
	return raw
}

func messageContentString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}
