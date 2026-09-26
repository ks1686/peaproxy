package quota

import (
	"sort"
)

// Family describes how an adapter can honestly populate remaining quota.
type Family struct {
	Adapter string `json:"adapter"`
	Headers string `json:"headers"`
	Probe   string `json:"probe,omitempty"`
	Note    string `json:"note"`
}

func localNote() string {
	return "not reported by provider; local runtimes do not expose remaining quota"
}

func oauthNote() string {
	return "not reported by provider; no stable documented remaining endpoint for this OAuth adapter"
}

func headersOnly(names string) string {
	return "remaining is captured from " + names + " on upstream responses when present; no documented remaining-quota GET for API keys"
}

var families = map[string]Family{
	"openai":          {Headers: "x-ratelimit-*", Note: headersOnly("x-ratelimit-* headers")},
	"anthropic":       {Headers: "anthropic-ratelimit-*", Note: headersOnly("anthropic-ratelimit-* headers")},
	"google":          {Headers: "", Note: "not reported by provider; Google AI Studio OpenAI-compat does not document remaining-quota headers or a key usage endpoint"},
	"gemini":          {Headers: "", Note: "not reported by provider; Google AI Studio OpenAI-compat does not document remaining-quota headers or a key usage endpoint"},
	"xai":             {Headers: "x-ratelimit-* if sent", Note: headersOnly("x-ratelimit-* headers if the upstream sends them")},
	"groq":            {Headers: "x-ratelimit-*", Note: headersOnly("x-ratelimit-* headers")},
	"cerebras":        {Headers: "x-ratelimit-*-day / x-ratelimit-*-minute if sent", Note: headersOnly("windowed x-ratelimit-* headers if the upstream sends them")},
	"huggingface":     {Headers: "x-ratelimit-* if sent", Note: headersOnly("x-ratelimit-* headers if the router sends them")},
	"nim":             {Headers: "x-ratelimit-* if sent", Note: "not reported by provider unless NVIDIA sends rate-limit headers; no documented remaining GET"},
	"sambanova":       {Headers: "x-ratelimit-*", Note: headersOnly("x-ratelimit-* headers")},
	"workers_ai":      {Headers: "x-ratelimit-* if sent", Note: "not reported by provider unless Cloudflare sends rate-limit headers; no remaining GET"},
	"ollama_cloud":    {Headers: "", Note: "not reported by provider; Ollama Cloud has no documented remaining-quota API"},
	"openrouter":      {Headers: "X-RateLimit-* on 429", Probe: "GET /api/v1/key", Note: "credit remaining from documented GET /api/v1/key (Health refresh). Rate-limit headers only on 429s"},
	"opencode_zen":    {Headers: "x-ratelimit-* if sent", Note: "not reported by provider unless Zen sends rate-limit headers; no documented remaining GET"},
	"opencode_go":     {Headers: "x-ratelimit-* if sent", Note: "not reported by provider unless OpenCode Go sends rate-limit headers; no documented remaining GET"},
	"openai_compat":   {Headers: "x-ratelimit-* / anthropic-ratelimit-* if sent", Note: headersOnly("rate-limit headers if the upstream sends them")},
	"ollama":          {Headers: "", Note: localNote()},
	"lmstudio":        {Headers: "", Note: localNote()},
	"llamacpp":        {Headers: "", Note: localNote()},
	"vllm":            {Headers: "", Note: localNote()},
	"jan":             {Headers: "", Note: localNote()},
	"gpt4all":         {Headers: "", Note: localNote()},
	"anthropic_oauth": {Headers: "anthropic-ratelimit-* if sent", Note: oauthNote()},
	"openai_oauth":    {Headers: "x-ratelimit-* if sent", Note: oauthNote()},
	"antigravity":     {Headers: "", Note: oauthNote()},
	"gemini_oauth":    {Headers: "", Note: oauthNote()},
	"xai_oauth":       {Headers: "x-ratelimit-* if sent", Note: oauthNote()},
	"kimi_oauth":      {Headers: "x-ratelimit-* if sent", Note: oauthNote()},
	"kimi_ai_oauth":   {Headers: "x-ratelimit-* if sent", Note: oauthNote()},
	"meta_oauth":      {Headers: "x-ratelimit-* if sent", Note: oauthNote()},
	"copilot_oauth":   {Headers: "", Note: "not reported by provider; Copilot has no stable documented remaining endpoint (internal quota APIs are skipped)"},
	"qwen_oauth":      {Headers: "", Note: "not reported by provider; Qwen consumer OAuth is not yet"},
	"factory_oauth":   {Headers: "", Note: "not reported by provider; Factory/Droid upstream is not yet"},
}

var defaultFamily = Family{
	Headers: "rate-limit remaining headers if sent",
	Note:    "not reported by provider unless the upstream sends documented remaining headers",
}

// Lookup returns the honesty policy for an adapter name.
func Lookup(adapter string) Family {
	if f, ok := families[adapter]; ok {
		f.Adapter = adapter
		return f
	}
	f := defaultFamily
	f.Adapter = adapter
	return f
}

// Families is the documented adapter matrix for GET /admin/quota.
func Families() []Family {
	out := make([]Family, 0, len(families))
	for name, f := range families {
		f.Adapter = name
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Adapter < out[j].Adapter })
	return out
}

// CanProbe reports whether this adapter has a documented remaining/usage GET.
func CanProbe(adapter string) bool {
	return Lookup(adapter).Probe != ""
}
