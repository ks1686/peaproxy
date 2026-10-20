package translate

import (
	"encoding/json"
	"testing"
)

// A Messages request served by a chat upstream lost tool_choice, temperature,
// top_p and stop_sequences: a forced tool call became optional, and sampling
// and stop settings were silently ignored.
func TestToOpenAICarriesToolChoiceAndSamplingSettings(t *testing.T) {
	raw := []byte(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"ping","description":"d","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"tool","name":"ping","disable_parallel_tool_use":true},
		"temperature":0.2,"top_p":0.9,"stop_sequences":["END"]}`)
	body, _, err := ToOpenAI(raw)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ToolChoice struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_choice"`
		ParallelToolCalls *bool    `json:"parallel_tool_calls"`
		Temperature       *float64 `json:"temperature"`
		TopP              *float64 `json:"top_p"`
		Stop              []string `json:"stop"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.ToolChoice.Type != "function" || out.ToolChoice.Function.Name != "ping" {
		t.Fatalf("tool_choice lost: %s", body)
	}
	if out.ParallelToolCalls == nil || *out.ParallelToolCalls {
		t.Fatalf("disable_parallel_tool_use lost: %s", body)
	}
	if out.Temperature == nil || *out.Temperature != 0.2 || out.TopP == nil || *out.TopP != 0.9 {
		t.Fatalf("sampling settings lost: %s", body)
	}
	if len(out.Stop) != 1 || out.Stop[0] != "END" {
		t.Fatalf("stop_sequences lost: %s", body)
	}
}

func TestToOpenAIMapsSimpleToolChoices(t *testing.T) {
	for in, want := range map[string]string{`"auto"`: `"auto"`, `"any"`: `"required"`, `"none"`: `"none"`} {
		raw := []byte(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"ping","input_schema":{"type":"object"}}],"tool_choice":{"type":` + in + `}}`)
		body, _, err := ToOpenAI(raw)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			ToolChoice json.RawMessage `json:"tool_choice"`
		}
		_ = json.Unmarshal(body, &out)
		if string(out.ToolChoice) != want {
			t.Fatalf("tool_choice %s -> %s, want %s", in, out.ToolChoice, want)
		}
	}
}
