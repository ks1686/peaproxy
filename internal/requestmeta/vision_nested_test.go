package requestmeta

import "testing"

func TestVisionIsDetectedInsideResponsesMessageItems(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"what is this"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`)
	if !RequirementsFromBody(WireResponses, body).Vision {
		t.Fatal("an input_image inside a Responses message item was not detected")
	}
}

func TestVisionIsDetectedInsideAToolResult(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}]}`)
	if !RequirementsFromBody(WireMessages, body).Vision {
		t.Fatal("an image inside a tool_result was not detected")
	}
}

func TestTextOnlyRequestIsNotVision(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"say image_url"}]}]}`)
	if RequirementsFromBody(WireResponses, body).Vision {
		t.Fatal("a text-only request was flagged as vision")
	}
}
