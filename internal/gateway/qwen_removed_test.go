package gateway

import (
	"strings"
	"testing"

	"github.com/ks1686/peaproxy/internal/config"
)

func TestQwenOnlyConfigNamesTheDiscontinuedLogin(t *testing.T) {
	_, err := New(config.Config{
		Providers: []config.Provider{{ID: "old-qwen", Adapter: "qwen_oauth"}},
	}, "", nil)
	if err == nil || !strings.Contains(err.Error(), "2026-04-15") {
		t.Fatalf("got %v", err)
	}
}

func TestQwenRowIsSkippedWhenAnotherAccountOpens(t *testing.T) {
	gw, err := New(config.Config{
		Providers: []config.Provider{
			{ID: "old-qwen", Adapter: "qwen_oauth"},
			{ID: "local", Adapter: "ollama", BaseURL: "http://127.0.0.1:9"},
		},
	}, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gw.inst) != 1 || gw.inst[0].Provider.ID != "local" {
		t.Fatalf("instances %#v", gw.inst)
	}
}
