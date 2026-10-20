package gateway

import (
	"context"
	"strings"
	"testing"
)

// A client that declares its own pea_search tool owns its calls: Inject leaves
// such a request alone, so PeaProxy must not intercept and answer them either.
func TestAClientDeclaredPeaSearchIsReturnedToTheClient(t *testing.T) {
	gw, _, calls := searchForeverGateway(t, `{"prompt_tokens":10,"completion_tokens":5}`)
	resp, _, err := gw.Chat(context.Background(), []byte(
		`{"model":"m","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"pea_search","parameters":{"type":"object"}}}]}`))
	if err != nil {
		t.Fatalf("the client's own tool call was intercepted: %v", err)
	}
	if n := calls(); n != 1 {
		t.Fatalf("upstream called %d times; the client's pea_search call must go back to the client after one", n)
	}
	if !strings.Contains(string(resp.Raw), `"pea_search"`) {
		t.Fatalf("the response lost the client's tool call: %s", resp.Raw)
	}
}
