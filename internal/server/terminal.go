package server

import "strings"

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
// Order matters: a stream that writes [DONE] after its finish chunk reports
// "done", which is what a chat parser keys on, while the specific reason is
// still visible in the request log body when the inspector is enabled.
func classifyTerminal(tail []byte) string {
	if len(tail) == 0 {
		return TerminalNone
	}
	text := string(tail)
	switch {
	case strings.Contains(text, "data: [DONE]"):
		return TerminalDone
	case strings.Contains(text, `"finish_reason":"stop"`) ||
		strings.Contains(text, `"finish_reason":"tool_calls"`) ||
		strings.Contains(text, `"finish_reason":"length"`) ||
		strings.Contains(text, `"finish_reason":"content_filter"`) ||
		strings.Contains(text, `"finish_reason": "stop"`) ||
		strings.Contains(text, `"finish_reason": "tool_calls"`) ||
		strings.Contains(text, `"finish_reason": "length"`) ||
		strings.Contains(text, `"finish_reason": "content_filter"`):
		return TerminalChatFinish
	case strings.Contains(text, `"type":"response.completed"`) ||
		strings.Contains(text, `"type": "response.completed"`):
		return TerminalResponsesDone
	case strings.Contains(text, `"type":"response.incomplete"`) ||
		strings.Contains(text, `"type": "response.incomplete"`) ||
		strings.Contains(text, `"type":"response.failed"`) ||
		strings.Contains(text, `"type": "response.failed"`):
		return TerminalResponsesPartial
	case strings.Contains(text, `"type":"message_stop"`) ||
		strings.Contains(text, `"type": "message_stop"`):
		return TerminalMessagesStop
	default:
		return TerminalNone
	}
}
