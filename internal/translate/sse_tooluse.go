package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// sseNow is the wall clock behind the Anthropic stream's keepalive pings;
// tests replace it to simulate a slow upstream.
var sseNow = time.Now

// claudePingInterval throttles keepalive pings. There is no timer: a ping is
// written only when a tool-call delta arrives and at least this long has passed
// since the last event written, so an upstream that goes fully quiet gets none.
const claudePingInterval = time.Second

// claudePing is the data of Anthropic's keepalive event, which consumers must
// accept anywhere in a Messages stream.
const claudePing = `{"type":"ping"}`

type pendingToolUse struct {
	id, name  string
	arguments strings.Builder
}

// claudeToolUseStart and claudeInputJSONDelta list their fields in the order
// Anthropic emits them; a map would sort the keys.
type claudeToolUseStart struct {
	Type         string             `json:"type"`
	Index        int                `json:"index"`
	ContentBlock claudeToolUseBlock `json:"content_block"`
}

type claudeInputJSONDelta struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

func writeClaudeToolUseBlock(writeEvent func(event, data string) error, index int, call *pendingToolUse) error {
	id := call.id
	if id == "" {
		id = fmt.Sprintf("toolu_peaproxy_%d", index)
	}
	raw, err := json.Marshal(claudeToolUseStart{
		Type:         "content_block_start",
		Index:        index,
		ContentBlock: claudeToolUseBlock{Type: "tool_use", ID: id, Name: call.name, Input: json.RawMessage(`{}`)},
	})
	if err != nil {
		return err
	}
	if err := writeEvent("content_block_start", string(raw)); err != nil {
		return err
	}
	if call.arguments.Len() > 0 {
		delta := claudeInputJSONDelta{Type: "input_json_delta", PartialJSON: call.arguments.String()}
		if err := writeClaudeDelta(writeEvent, index, delta); err != nil {
			return err
		}
	}
	return writeEvent("content_block_stop", fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index))
}
