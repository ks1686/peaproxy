package translate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ResponsesSSEToOpenAI converts a Responses SSE stream into the Chat
// Completions SSE stream expected by adapter.ChatStream callers. It is shared
// with Copilot, whose Responses-only models use the same upstream wire.
func ResponsesSSEToOpenAI(r io.Reader, w io.Writer, model string) error {
	return responsesSSEToOpenAI(r, w, model)
}

// responsesBodyUsage reads the usage object off an assembled Responses body.
// Both halves or nothing, for the reason the streamed path does it: a one-sided
// count understates the call, and the ledger refuses an unmeasured call rather
// than pricing half of it.
func responsesBodyUsage(body []byte) *OpenAIChatUsage {
	var parsed struct {
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.Usage == nil {
		return nil
	}
	if parsed.Usage.InputTokens <= 0 || parsed.Usage.OutputTokens <= 0 {
		return nil
	}
	return &OpenAIChatUsage{
		PromptTokens:     parsed.Usage.InputTokens,
		CompletionTokens: parsed.Usage.OutputTokens,
		TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
	}

}

func responsesSSEToOpenAI(r io.Reader, w io.Writer, model string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	callIndex := map[string]int{}
	nextIndex := 0
	wroteRole := false
	hadToolCalls := false
	var reasoningRaw []json.RawMessage
	// Token counts, carried from response.completed. A Responses stream that
	// never completes leaves these zero and no usage frame is written, because
	// an unmeasured call is a different thing from a call measured at zero.
	promptTokens, completionTokens := 0, 0
	finishOverride := ""
	const id = "peaproxy-codex"
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		if items := reasoningItemsFromResponsesEvent(payload); len(items) > 0 {
			reasoningRaw = items
		}
		if u, ok := responsesEventUsage(payload); ok {
			promptTokens, completionTokens = u.prompt, u.completion
		}
		switch kind, detail := responsesTurnEnd(payload); kind {
		case "failed":
			// A failed turn must reach the client as a failure. Finishing it
			// with "stop" tells the client a partial answer is complete.
			if err := WriteOpenAIChatSSEError(w, "upstream response failed: "+detail); err != nil {
				return err
			}
			return fmt.Errorf("upstream response failed: %s", detail)
		case "incomplete":
			finishOverride = detail
			continue
		}
		chunk, ok, tools, err := responsesStreamChunkToChat(payload, model, callIndex, &nextIndex)
		if err != nil {
			return err
		}
		if !ok {
			// Reasoning and lifecycle events can run for a long time before the
			// first text delta. A comment keeps the prelude timer alive so a
			// live Codex stream is not canceled and failed over.
			if _, err := io.WriteString(w, ": ping\n"); err != nil {
				return err
			}
			continue
		}
		if tools {
			hadToolCalls = true
		}
		if !wroteRole {
			if err := WriteOpenAIChatSSERole(w, id, model); err != nil {
				return err
			}
			wroteRole = true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		// The upstream was cut after bytes had already reached the client.
		// Returning here leaves that client looking at a stream that simply
		// stops, which is what "stream ended without finish_reason" is. Say
		// what happened instead, and do not invent a completion: a client told
		// a truncated answer is finished will go on to use it.
		if wErr := WriteOpenAIChatSSEError(w, "upstream stream ended before the response was complete: "+err.Error()); wErr != nil {
			return wErr
		}
		return err
	}
	reason := "stop"
	if hadToolCalls {
		reason = "tool_calls"
	}
	if finishOverride != "" {
		reason = finishOverride
	}
	if len(reasoningRaw) > 0 {
		opaque, err := CarryResponsesReasoning(reasoningRaw)
		if err != nil {
			return err
		}
		if len(opaque) > 0 {
			if !wroteRole {
				if err := WriteOpenAIChatSSERole(w, id, model); err != nil {
					return err
				}
			}
			if err := WriteOpenAIChatSSEOpaque(w, id, model, opaque); err != nil {
				return err
			}
		}
	}
	var usage *OpenAIChatUsage
	// Both halves, or nothing: a one-sided count understates the call, and the
	// ledger refuses an unmeasured one rather than pricing half of it.
	if promptTokens > 0 && completionTokens > 0 {
		usage = &OpenAIChatUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}
	}
	return WriteOpenAIChatSSEFinishUsage(w, id, model, reason, usage)
}

// responsesEventUsage reads the usage object the Responses wire publishes on
// response.completed. input_tokens/output_tokens are the names that wire uses;
// prompt/completion are what an OpenAI chat client expects.
func responsesEventUsage(payload string) (struct{ prompt, completion int }, bool) {
	var out struct{ prompt, completion int }
	var ev struct {
		Response *struct {
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(payload), &ev) != nil || ev.Response == nil || ev.Response.Usage == nil {
		return out, false
	}
	return struct{ prompt, completion int }{ev.Response.Usage.InputTokens, ev.Response.Usage.OutputTokens}, true
}

func reasoningItemsFromResponsesEvent(payload string) []json.RawMessage {
	var ev struct {
		Type     string          `json:"type"`
		Item     json.RawMessage `json:"item"`
		Response struct {
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return nil
	}
	var fromOutput []json.RawMessage
	for _, item := range ev.Response.Output {
		if responsesEventItemType(item) == "reasoning" {
			fromOutput = append(fromOutput, item)
		}
	}
	if len(fromOutput) > 0 {
		return fromOutput
	}
	if strings.Contains(ev.Type, "output_item.added") && responsesEventItemType(ev.Item) == "reasoning" {
		return []json.RawMessage{ev.Item}
	}
	return nil
}

func responsesEventItemType(item json.RawMessage) string {
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(item, &kind) != nil {
		return ""
	}
	return kind.Type
}

func responsesStreamChunkToChat(payload, model string, callIndex map[string]int, nextIndex *int) ([]byte, bool, bool, error) {
	var ev struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Text  string `json:"text"`
		Item  *struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			ID        string `json:"id"`
		} `json:"item"`
		OutputIndex int    `json:"output_index"`
		ItemID      string `json:"item_id"`
	}
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return nil, false, false, nil
	}
	var delta chatStreamDelta
	switch {
	case strings.Contains(ev.Type, "reasoning"):
		// response.reasoning_summary_text.delta and response.reasoning_text.delta
		// also end in "text.delta"; reasoning is carried as opaque items, never
		// as answer text.
	case strings.Contains(ev.Type, "output_text.delta"), strings.Contains(ev.Type, "text.delta"):
		if ev.Delta != "" {
			delta.Content = ev.Delta
		} else {
			delta.Content = ev.Text
		}
	case strings.Contains(ev.Type, "output_item.added") && ev.Item != nil && ev.Item.Type == "function_call":
		// Responses uses the output item id (fc_…) on later argument
		// deltas and the call id (call_…) on the item itself. output_index
		// also counts reasoning items, so it is not a chat tool slot.
		idx, found := resolveCallIndex(callIndex, ev.Item.CallID, ev.Item.ID, ev.OutputIndex)
		if !found {
			idx = *nextIndex
			*nextIndex++
		}
		rememberCallIndex(callIndex, idx, ev.Item.CallID, ev.Item.ID, ev.OutputIndex)
		id := ev.Item.CallID
		if id == "" {
			id = ev.Item.ID
		}
		tc := chatStreamToolCall{Index: idx, ID: id, Type: "function"}
		tc.Function.Name = ev.Item.Name
		tc.Function.Arguments = ev.Item.Arguments
		delta.ToolCalls = []chatStreamToolCall{tc}
	case strings.Contains(ev.Type, "function_call_arguments.delta"):
		idx, found := resolveCallIndex(callIndex, "", ev.ItemID, ev.OutputIndex)
		if !found {
			break
		}
		tc := chatStreamToolCall{Index: idx}
		tc.Function.Arguments = ev.Delta
		if tc.Function.Arguments == "" {
			tc.Function.Arguments = ev.Text
		}
		delta.ToolCalls = []chatStreamToolCall{tc}
	default:
		if ev.Delta != "" && (ev.Type == "" || strings.Contains(ev.Type, "delta")) && !strings.Contains(ev.Type, "function_call") {
			delta.Content = ev.Delta
		}
	}
	if delta.Content == "" && len(delta.ToolCalls) == 0 {
		return nil, false, false, nil
	}
	chunk := struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Index int             `json:"index"`
			Delta chatStreamDelta `json:"delta"`
		} `json:"choices"`
	}{ID: "peaproxy-codex", Object: "chat.completion.chunk", Model: model}
	chunk.Choices = make([]struct {
		Index int             `json:"index"`
		Delta chatStreamDelta `json:"delta"`
	}, 1)
	chunk.Choices[0].Delta = delta
	raw, err := json.Marshal(chunk)
	if err != nil {
		return nil, false, false, err
	}
	return raw, true, len(delta.ToolCalls) > 0, nil
}

// callIndex keys a chat tool slot by every id Codex may use for that call.
// output-index is stored only after the call is known, so a bare responses
// output_index cannot open a new slot that has no string id.
func rememberCallIndex(callIndex map[string]int, idx int, callID, itemID string, outputIndex int) {
	for _, key := range callIndexKeys(callID, itemID, outputIndex) {
		callIndex[key] = idx
	}
}

func resolveCallIndex(callIndex map[string]int, callID, itemID string, outputIndex int) (int, bool) {
	for _, key := range callIndexKeys(callID, itemID, outputIndex) {
		if idx, ok := callIndex[key]; ok {
			return idx, true
		}
	}
	return 0, false
}

func callIndexKeys(callID, itemID string, outputIndex int) []string {
	keys := make([]string, 0, 3)
	if callID != "" {
		keys = append(keys, callID)
	}
	if itemID != "" && itemID != callID {
		keys = append(keys, itemID)
	}
	keys = append(keys, fmt.Sprintf("output-index:%d", outputIndex))
	return keys
}

// responsesTurnEnd classifies the Responses events that end a turn
// without completing it. "failed" covers response.failed and a bare error
// event, with the upstream message as detail; "incomplete" carries the chat
// finish_reason the incomplete_details reason maps to.
func responsesTurnEnd(payload string) (kind, detail string) {
	var ev struct {
		Type     string `json:"type"`
		Message  string `json:"message"`
		Code     string `json:"code"`
		Response *struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return "", ""
	}
	switch ev.Type {
	case "response.failed":
		msg := "response.failed"
		if ev.Response != nil && ev.Response.Error != nil {
			msg = firstNonEmpty(ev.Response.Error.Message, ev.Response.Error.Code, msg)
		}
		return "failed", msg
	case "error":
		return "failed", firstNonEmpty(ev.Message, ev.Code, "error event")
	case "response.incomplete":
		if ev.Response != nil && ev.Response.IncompleteDetails != nil && ev.Response.IncompleteDetails.Reason == "content_filter" {
			return "incomplete", "content_filter"
		}
		return "incomplete", "length"
	}
	return "", ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
