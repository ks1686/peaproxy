package translate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

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
	// Anthropic splits usage across two events: input on message_start, the
	// cumulative output on message_delta. Both are read so the stream can
	// report the same numbers the non-stream path reports, which matters because
	// the ledger is what a spend ceiling is judged on and an unmeasured call
	// fails it closed.
	promptTokens, completionTokens := 0, 0
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
				Usage *struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			// usage on message_delta is the cumulative output count; the input
			// side was published on message_start and is kept from there.
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
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
				if ev.Message.Usage != nil {
					promptTokens = ev.Message.Usage.InputTokens
				}
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
			if ev.Usage != nil && ev.Usage.OutputTokens > 0 {
				completionTokens = ev.Usage.OutputTokens
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
		var usage *OpenAIChatUsage
		// Both halves have to be published for the pair to be a measurement.
		// One side alone leaves real tokens uncounted, and reporting it would
		// understate the call rather than admit it is unmeasured.
		if promptTokens > 0 && completionTokens > 0 {
			usage = &OpenAIChatUsage{
				PromptTokens:     promptTokens,
				CompletionTokens: completionTokens,
				TotalTokens:      promptTokens + completionTokens,
			}
		}
		if err := WriteOpenAIChatSSEFinishUsage(w, id, model, openAIFinishFromClaude(stopReason, hadToolCalls), usage); err != nil {
			return err
		}
	}
	return nil
}
