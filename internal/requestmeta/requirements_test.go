package requestmeta

import "testing"

func TestRequirementsFromBodyDetectsOpenAIToolsAndStrictSchema(t *testing.T) {
	got := RequirementsFromBody(WireChat, []byte(`{
  "tools": [{"type":"function","function":{"name":"lookup","strict":true,"parameters":{"type":"object"}}}],
  "parallel_tool_calls": true
}`))
	if !got.Tools || !got.ParallelTools || !got.StrictSchema {
		t.Fatalf("requirements = %#v", got)
	}
}

func TestRequirementsFromBodyDetectsVisionWithoutMatchingImageText(t *testing.T) {
	got := RequirementsFromBody(WireChat, []byte(`{
  "messages": [{"role":"user","content":[{"type":"text","text":"please describe an image"},{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]}]
}`))
	if !got.Vision {
		t.Fatalf("requirements = %#v, want vision", got)
	}

	got = RequirementsFromBody(WireChat, []byte(`{"messages":[{"role":"user","content":"please describe an image"}]}`))
	if got.Vision {
		t.Fatalf("requirements = %#v, did not want vision", got)
	}
}

func TestRequirementsFromBodyDetectsResponsesContinuation(t *testing.T) {
	got := RequirementsFromBody(WireResponses, []byte(`{"previous_response_id":"resp_123","input":"continue"}`))
	if !got.Continuation {
		t.Fatalf("requirements = %#v, want continuation", got)
	}
}
