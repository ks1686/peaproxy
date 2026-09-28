package config

import "testing"

// TestRequestEngineDefaultsBoundAttempts catches an unset request-engine
// section allowing every account's retry loop to multiply upstream calls.
func TestRequestEngineDefaultsBoundAttempts(t *testing.T) {
	cfg := Config{}
	if got := cfg.RequestMaxAttempts(); got != 3 {
		t.Fatalf("default attempts = %d, want 3", got)
	}
	if got := cfg.RequestDeadline(); got.String() != "2m0s" {
		t.Fatalf("default deadline = %s, want 2m0s", got)
	}
}

// TestRequestEngineRejectsInvalidConfiguredValues catches malformed settings
// silently disabling the request-wide safety limits.
func TestRequestEngineRejectsInvalidConfiguredValues(t *testing.T) {
	cfg := Config{RequestEngine: RequestEnginePrefs{MaxAttempts: 0, Deadline: "not-a-duration"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid request engine configuration")
	}
}
