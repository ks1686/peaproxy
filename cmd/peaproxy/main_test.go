package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestCommandHelpStartsWithoutConfigOrNetwork(t *testing.T) {
	cmd := exec.Command("go", "run", ".", "--help")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("peaproxy --help: %v\n%s", err, output)
	}
	for _, want := range []string{"PeaProxy is a localhost gateway", "serve", "accounts", "completion"} {
		if !strings.Contains(string(output), want) {
			t.Errorf("help missing %q:\n%s", want, output)
		}
	}
}
