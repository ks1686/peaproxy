package usage

import (
	"bytes"
	"encoding/json"
)

// ParsePublishedUsage reads a provider usage object from a JSON body or an
// SSE chunk. Token counts are returned only when the provider sent the field,
// including an explicit 0. cost is set only for a numeric usage.cost.
// A body with no usage object returns found=false and a nil cost.
func ParsePublishedUsage(body []byte) (prompt, completion int, cost *float64, found bool) {
	if p, c, price, ok := decodeUsageBody(body); ok {
		return p, c, price, true
	}
	var lastP, lastC int
	var lastCost *float64
	saw := false
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		line = bytes.TrimPrefix(line, []byte("data:"))
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		p, c, price, ok := decodeUsageBody(line)
		if !ok {
			continue
		}
		lastP, lastC, lastCost, saw = p, c, price, true
	}
	return lastP, lastC, lastCost, saw
}

func decodeUsageBody(body []byte) (prompt, completion int, cost *float64, found bool) {
	var wrap struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &wrap) != nil {
		return 0, 0, nil, false
	}
	raw := bytes.TrimSpace(wrap.Usage)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, 0, nil, false
	}
	var u struct {
		Prompt     *int     `json:"prompt_tokens"`
		Input      *int     `json:"input_tokens"`
		Completion *int     `json:"completion_tokens"`
		Output     *int     `json:"output_tokens"`
		Cost       *float64 `json:"cost"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return 0, 0, nil, false
	}
	if u.Prompt == nil && u.Input == nil && u.Completion == nil && u.Output == nil {
		return 0, 0, nil, false
	}
	if u.Prompt != nil {
		prompt = *u.Prompt
	} else if u.Input != nil {
		prompt = *u.Input
	}
	if u.Completion != nil {
		completion = *u.Completion
	} else if u.Output != nil {
		completion = *u.Output
	}
	return prompt, completion, u.Cost, true
}
