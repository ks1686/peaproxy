package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// recordedExtraUsage400 is the body anthropic-oauth returned on 2026-10-20 for
// a request Anthropic counted as a third-party app on a workspace plan. The
// api_key field is not Anthropic's; it proves the rest of the body still stays
// out of the client error.
const recordedExtraUsage400 = `{"type":"error","error":{"type":"invalid_request_error","message":"Third-party apps now draw from extra usage, not plan limits. Ask your workspace admin to add more and keep going."},"request_id":"req_011CUa1b2C3d4E5f6G7h8i9J","api_key":"sk-live-DO-NOT-LEAK"}`

const extraUsageSentence = "Third-party apps now draw from extra usage, not plan limits. Ask your workspace admin to add more and keep going."

// The adapters keep 240 bytes of the upstream body, so the api_key field below
// is cut mid-value and the body is not valid JSON by the time it is read. The
// message and request_id come before the cut and must survive it.
//
// The client used to get only "upstream HTTP 400", and senpi read that as a
// request-format problem. It must get Anthropic's sentence, a type it can
// branch on, the request_id, and what to do next.
func TestExtraUsageRefusalReachesClientNamed(t *testing.T) {
	front := failingServer(t, http.StatusBadRequest, recordedExtraUsage400, nil)
	cases := []struct {
		name, path, body string
		anthropic        bool
	}{
		{"messages", "/v1/messages", `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"say ok"}]}`, true},
		{"messages stream", "/v1/messages", `{"model":"m","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"say ok"}]}`, true},
		{"chat", "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"say ok"}]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, raw := postJSON(t, front, tc.path, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status %d, want the upstream 400: %s", resp.StatusCode, raw)
			}
			if strings.Contains(string(raw), "sk-live-DO-NOT-LEAK") {
				t.Fatalf("rest of the upstream body leaked: %s", raw)
			}
			var env struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
					Code    string `json:"code"`
				} `json:"error"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("not a JSON error envelope: %v: %s", err, raw)
			}
			msg := env.Error.Message
			if msg == "upstream HTTP 400" || !strings.Contains(msg, extraUsageSentence) {
				t.Fatalf("upstream message not passed through: %q", msg)
			}
			if !strings.Contains(msg, "API-key account") || !strings.Contains(msg, "req_011CUa1b2C3d4E5f6G7h8i9J") {
				t.Fatalf("hint or request_id missing: %q", msg)
			}
			if env.Error.Type != "extra_usage_required" {
				t.Fatalf("error.type %q", env.Error.Type)
			}
			if tc.anthropic {
				if env.Type != "error" || env.RequestID != "req_011CUa1b2C3d4E5f6G7h8i9J" {
					t.Fatalf("anthropic envelope: %s", raw)
				}
			} else if env.Error.Code != "extra_usage_required" {
				t.Fatalf("openai code %q", env.Error.Code)
			}
		})
	}
}

// A request_id that is not Anthropic's shape is dropped rather than echoed.
func TestExtraUsageRefusalDropsOddRequestID(t *testing.T) {
	body := `{"type":"error","error":{"type":"invalid_request_error","message":"` + extraUsageSentence + `"},"request_id":"<script>alert(1)</script>"}`
	front := failingServer(t, http.StatusBadRequest, body, nil)
	_, raw := postJSON(t, front, "/v1/messages", `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if strings.Contains(string(raw), "alert") {
		t.Fatalf("request_id echoed unchecked: %s", raw)
	}
	if !strings.Contains(string(raw), "extra_usage_required") {
		t.Fatalf("refusal not named: %s", raw)
	}
}
