package translate

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/ks1686/peaproxy/internal/jsonx"
)

const maxThinkingBudget = 31999

// SplitThinkingSuffix strips a trailing -thinking-N opt-in. The base name is
// what routing and upstream calls use. N is the requested budget.
func SplitThinkingSuffix(model string) (base string, budget int) {
	model = strings.TrimSpace(model)
	const mark = "-thinking-"
	i := strings.LastIndex(model, mark)
	if i <= 0 {
		return model, 0
	}
	n, err := strconv.Atoi(model[i+len(mark):])
	if err != nil || n <= 0 {
		return model, 0
	}
	return model[:i], n
}

// ApplyThinkingBudget sets Anthropic thinking on a Messages body and raises
// max_tokens so it stays above the budget. An existing thinking object is left
// in place. budget <= 0 is a no-op. This does not inject a Claude Code identity.
func ApplyThinkingBudget(raw []byte, budget int) []byte {
	if budget <= 0 || len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	if budget > maxThinkingBudget {
		budget = maxThinkingBudget
	}
	headroom := budget / 10
	if headroom < 1024 {
		headroom = 1024
	}
	need := budget + headroom
	if need > 32000 {
		need = 32000
	}
	var probe struct {
		MaxTokens int             `json:"max_tokens"`
		Thinking  json.RawMessage `json:"thinking"`
	}
	_ = json.Unmarshal(raw, &probe)
	if !thinkingEnabled(probe.Thinking) {
		obj, err := json.Marshal(struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		}{Type: "enabled", Budget: budget})
		if err != nil {
			return raw
		}
		raw = jsonx.SetTopLevelRaw(raw, "thinking", obj)
	}
	if probe.MaxTokens < need {
		raw = jsonx.SetTopLevelRaw(raw, "max_tokens", []byte(strconv.Itoa(need)))
	}
	return raw
}

func thinkingEnabled(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}
	var probe struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		return true
	}
	return strings.TrimSpace(probe.Type) != "" && !strings.EqualFold(probe.Type, "disabled")
}
