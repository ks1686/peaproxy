package translate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

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
	// Usage appears only on the final chunk of a stream that asked for it, the
	// shape OpenAI uses for stream_options.include_usage. Omitted everywhere
	// else so an ordinary delta does not claim to be a measurement.
	Usage *OpenAIChatUsage `json:"usage,omitempty"`
}

// OpenAIChatUsage is the OpenAI chat usage object. It is exported because each
// adapter translates from a different provider wire and every one of them has to
// be able to hand the counts to WriteOpenAIChatSSEFinishUsage.
type OpenAIChatUsage struct {
	PromptTokens        int                        `json:"prompt_tokens"`
	CompletionTokens    int                        `json:"completion_tokens"`
	TotalTokens         int                        `json:"total_tokens"`
	PromptTokensDetails *OpenAIPromptTokensDetails `json:"prompt_tokens_details,omitempty"`
	// CacheCreationInputTokens carries Anthropic cache writes, which chat has
	// no field for; they are billed at the cache-write rate and are not part of
	// PromptTokens.
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// OpenAIPromptTokensDetails is the cached-token detail of prompt_tokens.
type OpenAIPromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// claudeChatUsage maps Anthropic usage onto the chat shape. Anthropic reports
// cache reads and writes beside input_tokens; chat counts cached reads inside
// prompt_tokens (with cached_tokens as the detail), and cache writes travel as
// cache_creation_input_tokens so they are priced at their own rate.
func claudeChatUsage(input, cacheRead, cacheWrite, output int) *OpenAIChatUsage {
	prompt := input + cacheRead
	u := &OpenAIChatUsage{
		PromptTokens:             prompt,
		CompletionTokens:         output,
		TotalTokens:              prompt + cacheWrite + output,
		CacheCreationInputTokens: cacheWrite,
	}
	if cacheRead > 0 {
		u.PromptTokensDetails = &OpenAIPromptTokensDetails{CachedTokens: cacheRead}
	}
	return u
}

type openAIChatSSEChoice struct {
	Index        int             `json:"index"`
	Delta        openAIChatDelta `json:"delta"`
	FinishReason string          `json:"finish_reason,omitempty"`
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
	return WriteOpenAIChatSSEFinishUsage(w, id, model, finishReason, nil)
}

// WriteOpenAIChatSSEFinishUsage writes the terminal finish chunk, then the usage
// frame when one is known, then data: [DONE].
//
// The order matters and is not cosmetic. A client reading the OpenAI chat stream
// keys on finish_reason for the turn and on [DONE] for the end of the response,
// so the usage frame goes between them: after the finish chunk, so a client that
// stops at finish_reason has already seen the numbers, and before [DONE], so the
// stream still ends the way a client expects.
//
// A nil usage writes no frame at all rather than a frame of zeros. Zeros would
// read as a call that was measured and consumed nothing, which is the one thing
// the ledger cannot tell apart from a real zero-token call.
func WriteOpenAIChatSSEFinishUsage(w io.Writer, id, model, finishReason string, usage *OpenAIChatUsage) error {
	if finishReason == "" {
		finishReason = "stop"
	}
	if err := writeOpenAIChatSSEChunk(w, id, model, openAIChatDelta{}, finishReason); err != nil {
		return err
	}
	if usage != nil {
		if err := writeOpenAIChatSSEUsage(w, id, model, usage); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "data: [DONE]\n\n")
	return err
}

// writeOpenAIChatSSEUsage writes the usage-only chunk: the same object as every
// other chunk, with an empty choices array so it is not read as a content delta.
func writeOpenAIChatSSEUsage(w io.Writer, id, model string, usage *OpenAIChatUsage) error {
	raw, err := json.Marshal(openAIChatSSEChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Model:   model,
		Choices: []openAIChatSSEChoice{},
		Usage:   usage,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", raw)
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
