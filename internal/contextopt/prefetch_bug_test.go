package contextopt

import (
	"encoding/json"
	"strings"
	"testing"
)

// The reference block used to emit "content" three times in one JSON object:
// once for the opening label, once for the passages, once for the closing
// label. Duplicate keys are not a list. A decoder keeps the last one, so the
// model received the closing delimiter and every retrieved passage was
// silently discarded -- while the feature reported itself as working.
func TestPrefetchBlockCarriesItsPassages(t *testing.T) {
	passages := []string{"the staging cluster runs on port 8443"}
	block := buildBlock(passages)

	var msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(block), &msg); err != nil {
		t.Fatalf("the reference block is not a valid message: %v\n%s", err, block)
	}
	if !strings.Contains(msg.Content, "8443") {
		t.Fatalf("the retrieved passage did not survive into the message content:\n%s", msg.Content)
	}
}
