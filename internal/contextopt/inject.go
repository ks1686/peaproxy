// Package contextopt lets PeaProxy carry context across providers so a
// request can be fulfilled more cheaply without being answered worse.
//
// The core rule of this package is that a tool it owns is a tool the client
// never asked for. PeaProxy's retrieval tool is executed by PeaProxy, so
// anything the model sends back about it has to be answered here. If the client
// cannot run tools, or the provider cannot make them, that contract cannot be
// kept and the tool is not offered at all.
package contextopt

import (
	"bytes"
	"encoding/json"
)

// ToolName is the proxy-owned retrieval tool. It is namespaced so it cannot
// collide with a caller tool, and so a transcript makes clear who asked for the
// call.
const ToolName = "pea_search"

// MaxRounds bounds how many times PeaProxy will answer its own tool in a single
// request. Without it, a model that keeps calling the tool turns one request
// into an unbounded loop against a metered provider.
const MaxRounds = 4

// Plan is the decision inputs for one request. Everything here is an observed
// fact about this request or this provider, never a guess.
type Plan struct {
	// Enabled is the user's opt-in.
	Enabled bool
	// ClientTools reports that the incoming request already uses tools, which
	// is what proves the client can execute a tool result.
	ClientTools bool
	// ProviderTools reports that the selected provider supports tool calls.
	ProviderTools bool
}

// toolSpec is the definition PeaProxy adds. It is deliberately small: the
// model is told it can search, not how to do everything else.
var toolSpec = map[string]any{
	"type": "function",
	"function": map[string]any{
		"name":        ToolName,
		"description": "Search previously gathered context for material relevant to the current request. Returns passages only; it never answers the request.",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "What to look for."},
			},
			"required": []string{"query"},
		},
	},
}

// Inject adds PeaProxy's retrieval tool to a request, or returns the body
// untouched.
//
// Injection is refused unless every condition holds, because a tool PeaProxy
// cannot answer is a broken turn rather than a useful one.
func Inject(body []byte, plan Plan) ([]byte, bool) {
	if !plan.Enabled || !plan.ClientTools || !plan.ProviderTools {
		return body, false
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return body, false
	}
	if _, ok := doc["tools"]; !ok {
		// The client declared tools via another wire's field, but this body has
		// none. Adding the key from scratch would be inventing a request shape.
		return body, false
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(doc["tools"], &tools); err != nil {
		return body, false
	}
	for _, raw := range tools {
		if toolNamed(raw, ToolName) {
			return body, false
		}
	}
	spec, err := json.Marshal(toolSpec)
	if err != nil {
		return body, false
	}
	tools = append(tools, spec)
	encoded, err := json.Marshal(tools)
	if err != nil {
		return body, false
	}
	doc["tools"] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return body, false
	}
	return out, true
}

func toolNamed(raw json.RawMessage, name string) bool {
	var t struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return false
	}
	return t.Function.Name == name || t.Name == name
}

// IsProxyToolCall reports whether a tool call in a response is one PeaProxy
// owns and therefore must answer itself.
func IsProxyToolCall(name string) bool { return bytes.Equal([]byte(name), []byte(ToolName)) }

// Budget bounds how many times PeaProxy answers its own tool in one request.
//
// It exists because the loop is attacker-shaped: a model asked to search can
// keep searching, and every round is a bill. When the budget runs out the turn
// is stopped and the stop is reported, rather than the model being left to
// continue against a provider that is still charging.
type Budget struct {
	used int
}

// NewBudget returns a full budget.
func NewBudget() *Budget { return &Budget{} }

// Take consumes one round, reporting whether it was available.
func (b *Budget) Take() bool {
	if b.used >= MaxRounds {
		return false
	}
	b.used++
	return true
}

// Exhausted reports whether the budget is spent.
func (b *Budget) Exhausted() bool { return b.used >= MaxRounds }

// Remaining reports the rounds still available.
func (b *Budget) Remaining() int {
	if b.used >= MaxRounds {
		return 0
	}
	return MaxRounds - b.used
}

// Stop explains why the turn is ending. It is returned to the caller so the
// reason reaches the user instead of the request simply going quiet.
func (b *Budget) Stop() string {
	if !b.Exhausted() {
		return ""
	}
	return "stopped after " + itoa(MaxRounds) + " " + ToolName + " calls: the model kept searching past the round limit"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
