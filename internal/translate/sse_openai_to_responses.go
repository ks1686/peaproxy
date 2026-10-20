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

	"github.com/ks1686/peaproxy/internal/jsonx"
)

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
					`{"type":"response.output_item.added","output_index":%d,"item":{"type":"message","id":%s,"role":"assistant","content":[]}}`,
					messageIndex, jsonx.Quote(messageItemID))); err != nil {
					return err
				}
				if err := emit("response.content_part.added", fmt.Sprintf(
					`{"type":"response.content_part.added","item_id":%s,"output_index":%d,"content_index":0,"part":{"type":"output_text","text":""}}`,
					jsonx.Quote(messageItemID), messageIndex)); err != nil {
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
			// Arguments that arrive before the item is announced are carried by
			// output_item.added; a delta for an unannounced item would have
			// nowhere to go in the client's accumulator.
			wasStarted := pc.Started
			if !wasStarted {
				pc.Arguments += tc.Function.Arguments
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
				}{Type: "function_call", ID: pc.ItemID, CallID: pc.ID, Name: pc.Name}
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
				// Announced with empty arguments, as OpenAI does; what was
				// buffered before the announcement follows as one delta.
				if pc.Arguments != "" {
					if err := emitArgumentsDelta(emit, pc.ItemID, pc.Index, pc.Arguments); err != nil {
						return err
					}
				}
			}
			if tc.Function.Arguments != "" && wasStarted {
				pc.Arguments += tc.Function.Arguments
				if err := emitArgumentsDelta(emit, pc.ItemID, pc.Index, tc.Function.Arguments); err != nil {
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
			`{"type":"response.function_call_arguments.done","item_id":%s,"output_index":%d,"arguments":%s}`,
			jsonx.Quote(pc.ItemID), pc.Index, jsonx.Quote(pc.Arguments))); err != nil {
			return err
		}
		if err := emit("response.output_item.done", fmt.Sprintf(
			`{"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":%s,"call_id":%s,"name":%s,"arguments":%s}}`,
			pc.Index, jsonx.Quote(pc.ItemID), jsonx.Quote(pc.ID), jsonx.Quote(pc.Name), jsonx.Quote(pc.Arguments))); err != nil {
			return err
		}
	}
	if messageStarted && !messageClosed {
		if err := emit("response.output_text.done", fmt.Sprintf(
			`{"type":"response.output_text.done","item_id":%s,"output_index":%d,"content_index":0,"text":%s}`,
			jsonx.Quote(messageItemID), messageIndex, jsonx.Quote(text.String()))); err != nil {
			return err
		}
		if err := emit("response.content_part.done", fmt.Sprintf(
			`{"type":"response.content_part.done","item_id":%s,"output_index":%d,"content_index":0,"part":{"type":"output_text","text":%s}}`,
			jsonx.Quote(messageItemID), messageIndex, jsonx.Quote(text.String()))); err != nil {
			return err
		}
		if err := emit("response.output_item.done", fmt.Sprintf(
			`{"type":"response.output_item.done","output_index":%d,"item":{"type":"message","id":%s,"role":"assistant","content":[{"type":"output_text","text":%s}]}}`,
			messageIndex, jsonx.Quote(messageItemID), jsonx.Quote(text.String()))); err != nil {
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

func emitArgumentsDelta(emit func(string, string) error, itemID string, index int, delta string) error {
	raw, err := json.Marshal(struct {
		Type        string `json:"type"`
		ItemID      string `json:"item_id"`
		OutputIndex int    `json:"output_index"`
		Delta       string `json:"delta"`
	}{Type: "response.function_call_arguments.delta", ItemID: itemID, OutputIndex: index, Delta: delta})
	if err != nil {
		return err
	}
	return emit("response.function_call_arguments.delta", string(raw))
}

// responseStub is the minimal response object carried by the lifecycle events
// that are not the terminal one. The client only reads identity off it; the
// full object arrives with response.completed.
func responseStub(id, model string) string {
	return fmt.Sprintf(
		`{"id":%s,"object":"response","status":"in_progress","model":%s}`,
		jsonx.Quote(id), jsonx.Quote(model))
}
