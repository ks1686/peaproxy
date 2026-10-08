package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/router"
)

// The openai_compat adapter declares Tools for every endpoint it talks to,
// because the wire format has a tools field. Whether the server behind it acts
// on that field is not observable from here. A user who knows theirs ignores it
// has to be able to say so, and routing has to believe them.
func TestAUserCanDeclareAnEndpointToolLess(t *testing.T) {
	withTools, withoutTools := 0, 0
	gw := twoAccountGateway(t, countOK(&withTools, "liar"), countOK(&withoutTools, "honest"))
	gw.cfg.Providers[0].Capabilities.Tools = boolp(false)
	gw.cfg.AutomaticRoutes.Enabled = true
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	body := []byte(`{"model":"` + router.RouteAuto + `","tools":[{"type":"function","function":{"name":"f"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	// The request still succeeds -- the other account can run tools. What must
	// not happen is it being served by the endpoint the user declared tool-less.
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatalf("a tool call failed although a capable endpoint was configured: %v", err)
	}
	if withTools != 0 {
		t.Fatalf("the declared tool-less endpoint was called %d times", withTools)
	}
	if withoutTools != 1 {
		t.Fatalf("the capable endpoint was called %d times, want 1", withoutTools)
	}
}

// With every endpoint declared tool-less there is nowhere to send the request,
// and the refusal has to say so. Silently dropping the tools and calling a
// tool-less endpoint is the failure this setting exists to prevent.
func TestAToolCallIsRefusedWhenNoEndpointSupportsTools(t *testing.T) {
	first, second := 0, 0
	gw := twoAccountGateway(t, countOK(&first, "a"), countOK(&second, "b"))
	gw.cfg.Providers[0].Capabilities.Tools = boolp(false)
	gw.cfg.Providers[1].Capabilities.Tools = boolp(false)
	gw.cfg.AutomaticRoutes.Enabled = true
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	body := []byte(`{"model":"` + router.RouteAuto + `","tools":[{"type":"function","function":{"name":"f"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	_, _, err := gw.Chat(context.Background(), body)
	if err == nil {
		t.Fatal("a tool call was served although every endpoint was declared tool-less")
	}
	if !strings.Contains(err.Error(), "tool") {
		t.Fatalf("the refusal does not mention tools: %v", err)
	}
	if first != 0 || second != 0 {
		t.Fatalf("a tool-less endpoint was called anyway: %d, %d", first, second)
	}
}

// The same request without the declaration still routes, so the override is a
// correction rather than a new default.
func TestToolsAreRoutedNormallyWithoutAnOverride(t *testing.T) {
	withTools := 0
	gw := twoAccountGateway(t, countOK(&withTools, "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	body := []byte(`{"model":"` + router.RouteAuto + `","tools":[{"type":"function","function":{"name":"f"}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	if _, _, err := gw.Chat(context.Background(), body); err != nil {
		t.Fatalf("a tool call was refused without any override: %v", err)
	}
	if withTools != 1 {
		t.Fatalf("the tool-capable endpoint was called %d times, want 1", withTools)
	}
}

// What the user said is what the health report shows, because that is where a
// user goes to find out what PeaProxy believes about their own endpoint. A
// report that still said "tools: yes" for an endpoint just declared tool-less
// would be a lie the user has to notice themselves.
func TestTheHealthReportShowsWhatTheUserDeclared(t *testing.T) {
	gw := twoAccountGateway(t, countOK(new(int), "a"), countOK(new(int), "b"))
	gw.cfg.AutomaticRoutes.Enabled = true
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	tools := func(id string) bool {
		for _, h := range gw.AdapterHealth() {
			if h.AccountID == id {
				return h.Capabilities.Tools
			}
		}
		t.Fatalf("no health row for account %q", id)
		return false
	}
	if !tools("acct-a") {
		t.Fatal("the default endpoint was reported as tool-less")
	}

	gw.cfg.Providers[0].Capabilities.Tools = boolp(false)
	gw.cfg.AutomaticRoutes.Enabled = true
	_ = gw.rebuild()
	gw.Refresh(context.Background())

	if tools("acct-a") {
		t.Fatal("a declared tool-less endpoint is still reported as supporting tools")
	}
	if !tools("acct-b") {
		t.Fatal("the override leaked to another account")
	}
}
