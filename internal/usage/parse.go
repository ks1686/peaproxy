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
	// total is the provider's own all-in count for the call. It exists to tell
	// "this call produced no output tokens" apart from "this provider did not
	// say": OpenAI's embedding usage publishes prompt_tokens and total_tokens
	// and has no completion_tokens field at all.
	total *int
	// cacheReadNested marks a provider that counts cached tokens inside the
	// prompt total rather than beside it.
	cacheReadNested bool
}

func (c counters) empty() bool {
	return c.prompt == nil && c.completion == nil && c.cacheRead == nil && c.cacheWrite == nil &&
		c.cost == nil && c.total == nil
}

// merge keeps the highest value seen for each counter.
//
// Stream frames repeat and update counters rather than restate them: Anthropic
// reports input tokens in message_start and running output tokens in
// message_delta, and later frames often carry only what changed. Taking the
// maximum keeps a real number from being erased by a later frame that omits it
// or reports zero. Cost is different: a provider quotes the running cost of the
// turn, so the last value wins rather than the largest.
//
// The body handed to a single parse is one attempt. Summing separate attempts
// into one event is a later step and is not done here.
func (c *counters) merge(o counters) {
	c.prompt = maxInt(c.prompt, o.prompt)
	c.completion = maxInt(c.completion, o.completion)
	c.cacheRead = maxInt(c.cacheRead, o.cacheRead)
	c.cacheWrite = maxInt(c.cacheWrite, o.cacheWrite)
	c.total = maxInt(c.total, o.total)
	// Nesting is a property of the provider's shape, not a running total, so it
	// is only set once a counter that carries it has actually been seen. Two
	// providers in one stream are not a case that occurs, and guessing here
	// would misprice one of them.
	if o.cacheReadNested {
		c.cacheReadNested = true
	}
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
	e.CacheReadNested = c.cacheReadNested
	// Token counts are known only when the provider published them. Cache
	// counters alone do not say how many tokens a call consumed, and recording
	// that as a known zero-token call would invent a number.
	if c.prompt != nil || c.completion != nil {
		e.TokensKnown = true
	}
	// A cost can only be stated for a call whose usage is complete on both
	// sides. One half alone leaves real spend out of the total, and a total that
	// omits spend is the direction that spends the user's money.
	//
	// Embeddings are the exception that has to be named. OpenAI's embedding
	// usage publishes prompt_tokens and total_tokens and has no completion_tokens
	// field at all, because an embedding call produces no output tokens. Reading
	// that as an incomplete call priced nothing for money that was spent, and a
	// ceiling that fails closed on unmeasured calls then refused every request
	// after the first embedding. A missing completion counter is a zero when the
	// wire shape says the call cannot have one.
	if c.prompt != nil && c.completion == nil && c.total != nil && *c.total == *c.prompt {
		zero := 0
		e.Costable = true
		c.completion = &zero
	} else {
		e.Costable = c.prompt != nil && c.completion != nil
	}
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
	Total      *int     `json:"total_tokens"`
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
	c := counters{cost: u.Cost, total: u.Total}
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
		// Anthropic reports cache reads beside the prompt total.
		c.cacheRead = u.CacheReadInput
	case u.PromptTokensDetails != nil && u.PromptTokensDetails.CachedTokens != nil:
		// OpenAI reports them as a detail of the prompt total.
		c.cacheRead = u.PromptTokensDetails.CachedTokens
		c.cacheReadNested = true
	case u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens != nil:
		c.cacheRead = u.InputTokensDetails.CachedTokens
		c.cacheReadNested = true
	case u.CachedTokens != nil:
		c.cacheRead = u.CachedTokens
		c.cacheReadNested = true
	}
	if u.CacheCreationInput != nil {
		c.cacheWrite = u.CacheCreationInput
	}
	return c
}
