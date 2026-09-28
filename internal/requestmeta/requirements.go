package requestmeta

import "encoding/json"

// RequirementsFromBody inspects a client body without returning or modifying
// it. Malformed/unknown fields remain unknown rather than being inferred.
func RequirementsFromBody(wire Wire, body []byte) Requirements {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return Requirements{}
	}
	var out Requirements
	if tools, ok := raw["tools"]; ok && jsonArrayHasValue(tools) {
		out.Tools = true
		out.StrictSchema = toolsContainStrictSchema(tools)
	}
	if parallel, ok := raw["parallel_tool_calls"]; ok {
		_ = json.Unmarshal(parallel, &out.ParallelTools)
	}
	if wire == WireResponses {
		if previous, ok := raw["previous_response_id"]; ok {
			var id string
			if json.Unmarshal(previous, &id) == nil && id != "" {
				out.Continuation = true
			}
		}
	}
	if messages, ok := raw["messages"]; ok {
		out.Vision = messagesContainImage(messages)
	}
	if input, ok := raw["input"]; ok && !out.Vision {
		out.Vision = inputContainsImage(input)
	}
	return out
}

func jsonArrayHasValue(raw json.RawMessage) bool {
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) > 0
}

func toolsContainStrictSchema(raw json.RawMessage) bool {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		if containsStrictTrue(tool) {
			return true
		}
	}
	return false
}

func containsStrictTrue(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	if strict, ok := object["strict"]; ok {
		var enabled bool
		if json.Unmarshal(strict, &enabled) == nil && enabled {
			return true
		}
	}
	for _, value := range object {
		if containsStrictTrue(value) {
			return true
		}
	}
	return false
}

func messagesContainImage(raw json.RawMessage) bool {
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &messages) != nil {
		return false
	}
	for _, message := range messages {
		if inputContainsImage(message.Content) {
			return true
		}
	}
	return false
}

func inputContainsImage(raw json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		switch block.Type {
		case "image", "image_url", "input_image":
			return true
		}
	}
	return false
}
