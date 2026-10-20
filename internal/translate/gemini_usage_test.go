package translate

import "testing"

// Cloud Code wraps every event in {"response":{...}}, and Gemini bills thinking
// tokens as output while reporting them apart from candidatesTokenCount.
func TestGeminiUsageReadsTheCloudCodeEnvelopeAndThoughts(t *testing.T) {
	cases := map[string]string{
		"wrapped":   `{"response":{"candidates":[],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":5}}}`,
		"top level": `{"candidates":[],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":5}}`,
	}
	for name, body := range cases {
		u := GeminiUsage([]byte(body))
		if u == nil || u.PromptTokens != 100 || u.CompletionTokens != 25 {
			t.Fatalf("%s: usage = %+v, want prompt 100 completion 25 (20 candidates + 5 thoughts)", name, u)
		}
	}
}
