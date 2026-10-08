package server

import (
	"bytes"
	"encoding/json"
)

// Terminal classes are what a client's stream parser looks for to decide a
// turn ended properly. A chat client that finds none of these raises its own
// "stream ended without finish_reason" error, from bytes PeaProxy forwarded.
//
// These are markers only. PeaProxy must never write one of them itself to make
// a stream look finished: a truncated turn reported as a completed one is worse
// than a loud failure, because the client then runs on partial output.
const (
	TerminalNone             = "none"
	TerminalDone             = "done"                 // data: [DONE]
	TerminalChatFinish       = "chat_finish"          // finish_reason on a chat chunk
	TerminalResponsesDone    = "responses_completed"  // response.completed
	TerminalResponsesPartial = "responses_incomplete" // response.incomplete / response.failed
	TerminalMessagesStop     = "messages_stop"        // message_stop on the Claude wire
)

// terminalTailBytes is how much trailing stream text the classifier inspects.
// A terminal event is the last thing a well-formed stream writes, so a bounded
// tail is enough and keeps the writer's memory flat on a long stream.
const terminalTailBytes = 16 << 10

// classifyTerminal names the terminal event found in a stream tail.
//
// Frames are parsed as JSON and the field is read from them. Matching a literal
// marker instead would miss a stream that ended correctly but was formatted
// with different spacing, and report "none" for it -- which is the one answer
// that sends an investigation after a stream that never failed.
func classifyTerminal(tail []byte) string {
	var sawDone, sawChatFinish, sawResponsesDone, sawResponsesPartial, sawMessagesStop bool
	for _, frame := range sseFrames(tail) {
		if bytes.Equal(bytes.TrimSpace(frame), []byte("[DONE]")) {
			sawDone = true
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Event string `json:"event"`
			Error *struct {
				Type string `json:"type"`
			} `json:"error"`
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(frame, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.completed":
			sawResponsesDone = true
		case "response.incomplete", "response.failed":
			sawResponsesPartial = true
		case "message_stop":
			sawMessagesStop = true
		}
		for _, c := range ev.Choices {
			if c.FinishReason != nil && *c.FinishReason != "" {
				sawChatFinish = true
			}
		}
		if ev.Error != nil && ev.Error.Type != "" {
			sawResponsesPartial = true
		}
	}
	// A chat client keys on [DONE]; the Responses and Claude wires never send
	// one. Reporting the class a given parser looks for keeps the field
	// readable per wire.
	switch {
	case sawDone:
		return TerminalDone
	case sawResponsesDone:
		return TerminalResponsesDone
	case sawMessagesStop:
		return TerminalMessagesStop
	case sawChatFinish:
		return TerminalChatFinish
	case sawResponsesPartial:
		return TerminalResponsesPartial
	default:
		return TerminalNone
	}
}

// sseFrames returns the data payloads in a bounded stream tail. An SSE frame may
// span several lines, each carrying its own data: prefix, so consecutive data
// lines are joined back into one payload before parsing. A blank line ends a
// frame; event: and comment lines are skipped.
func sseFrames(tail []byte) [][]byte {
	var out [][]byte
	var pending [][]byte
	flush := func() {
		if len(pending) == 0 {
			return
		}
		out = append(out, bytes.Join(pending, []byte("\n")))
		pending = nil
	}
	for _, line := range bytes.Split(tail, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		switch {
		case len(trimmed) == 0:
			flush()
		case bytes.HasPrefix(trimmed, []byte("data:")):
			pending = append(pending, bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:"))))
		default:
			// event:, id:, retry: or a comment line: not payload.
		}
	}
	flush()
	return out
}
