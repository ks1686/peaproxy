package usage

import (
	"bytes"
	"encoding/json"
)

// Usage counters as a provider reported them. A nil field means the provider
// did not publish that counter, which is not the same as publishing zero.
type counters struct {
	prompt     *int
	completion *int
	cacheRead  *int
	cacheWrite *int
	cost       *float64
}

func (c counters) empty() bool {
	return c.prompt == nil && c.completion == nil && c.cacheRead == nil && c.cacheWrite == nil && c.cost == nil
}

// merge keeps the highest value seen for each counter.
//
// Stream frames repeat and update counters rather than restate them: Anthropic
// reports input tokens in message_start and running output tokens in
// message_delta, and later frames often carry only what changed. Taking the
// maximum keeps a real number from being erased by a later frame that omits it
// or reports zero. Frames from different attempts are never merged together --
// the caller sums attempts instead.
func (c *counters) merge(o counters) {
	c.prompt = maxInt(c.prompt, o.prompt)
	c.completion = maxInt(c.completion, o.completion)
	c.cacheRead = maxInt(c.cacheRead, o.cacheRead)
	c.cacheWrite = maxInt(c.cacheWrite, o.cacheWrite)
	if o.cost != nil {
		c.cost = o.cost
	}
}

func maxInt(a, b *int) *int {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case *b > *a:
		return b
	default:
		return a
	}
}

// ApplyPublishedUsage copies provider usage onto an event.
// A cache hit records that tokens are known and zero, and does not replay the cached body's usage.
func ApplyPublishedUsage(e *Event, body []byte, cacheHit bool) {
	if e == nil {
		return
	}
	if cacheHit {
		e.CacheHit = true
		e.TokensKnown = true
		return
	}
	c, ok := ParseCounters(body)
	if !ok {
		return
	}
	if c.prompt != nil {
		e.PromptTokens = *c.prompt
	}
	if c.completion != nil {
		e.CompletionTokens = *c.completion
	}
	if c.cacheRead != nil {
		e.CacheRead = *c.cacheRead
	}
	if c.cacheWrite != nil {
		e.CacheWrite = *c.cacheWrite
	}
	e.TokensKnown = true
	e.CostUSD = c.cost
}

// ParsePublishedUsage reads a provider usage object from a JSON body or an
// SSE chunk. Token counts are returned only when the provider sent the field,
// including an explicit 0. cost is set only for a numeric usage.cost.
// A body with no usage object returns found=false and a nil cost.
func ParsePublishedUsage(body []byte) (prompt, completion int, cost *float64, found bool) {
	c, ok := ParseCounters(body)
	if !ok {
		return 0, 0, nil, false
	}
	if c.prompt != nil {
		prompt = *c.prompt
	}
	if c.completion != nil {
		completion = *c.completion
	}
	return prompt, completion, c.cost, true
}

// ParseCounters reads every usage counter from a body that may be one JSON
// document or a whole SSE stream.
func ParseCounters(body []byte) (counters, bool) {
	var out counters
	found := false
	if c, ok := decodeUsageBody(body); ok {
		out.merge(c)
		found = true
	}
	if found && len(body) > 0 && !bytes.Contains(body, []byte("data:")) {
		return out, true
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		line = bytes.TrimPrefix(line, []byte("data:"))
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		if c, ok := decodeUsageBody(line); ok {
			out.merge(c)
			found = true
		}
	}
	return out, found
}

// usageEnvelope is every place a provider puts a usage object: at the top
// level, under "message" on the Claude wire, or under "response" on the
// Responses wire.
type usageEnvelope struct {
	Usage    *usageBody   `json:"usage"`
	Message  *nestedUsage `json:"message"`
	Response *nestedUsage `json:"response"`
}

type nestedUsage struct {
	Usage *usageBody `json:"usage"`
}

type usageBody struct {
	Prompt     *int     `json:"prompt_tokens"`
	Input      *int     `json:"input_tokens"`
	Completion *int     `json:"completion_tokens"`
	Output     *int     `json:"output_tokens"`
	Cost       *float64 `json:"cost"`

	// Anthropic splits its cache counters out of the prompt total.
	CacheReadInput      *int `json:"cache_read_input_tokens"`
	CacheCreationInput  *int `json:"cache_creation_input_tokens"`
	PromptTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputTokensDetails *struct {
		CachedTokens *int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	CachedTokens *int `json:"cached_tokens"`
}

func decodeUsageBody(body []byte) (counters, bool) {
	var env usageEnvelope
	if json.Unmarshal(body, &env) != nil {
		return counters{}, false
	}
	for _, u := range []*usageBody{env.Usage, nested(env.Message), nested(env.Response)} {
		if u == nil {
			continue
		}
		if c := u.counters(); !c.empty() {
			return c, true
		}
	}
	return counters{}, false
}

func nested(n *nestedUsage) *usageBody {
	if n == nil {
		return nil
	}
	return n.Usage
}

func (u *usageBody) counters() counters {
	c := counters{cost: u.Cost}
	switch {
	case u.Prompt != nil:
		c.prompt = u.Prompt
	case u.Input != nil:
		c.prompt = u.Input
	}
	switch {
	case u.Completion != nil:
		c.completion = u.Completion
	case u.Output != nil:
		c.completion = u.Output
	}
	switch {
	case u.CacheReadInput != nil:
		c.cacheRead = u.CacheReadInput
	case u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil:
		c.cacheRead = u.PromptTokensDetails.CachedTokens
	case u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens != nil:
		c.cacheRead = u.InputTokensDetails.CachedTokens
	case u.CachedTokens != nil:
		c.cacheRead = u.CachedTokens
	}
	if u.CacheCreationInput != nil {
		c.cacheWrite = u.CacheCreationInput
	}
	return c
}
