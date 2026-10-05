package config

import "testing"

// The v3 block is additive to schema 1. Absent means the opinionated default
// on, which is the whole point: a user who points their client at PeaProxy
// should get the savings without configuring anything. An explicit false is
// how they opt out.
func TestOptimizationDefaultsAreOpinionated(t *testing.T) {
	var c Config
	if !c.OptimizationEnabled() {
		t.Fatal("an absent optimization block must leave the opinionated default on")
	}
	if c.LocalAssistantEnabled() {
		t.Fatal("local assistance must stay off until it is asked for")
	}
	if c.PersistentContextEnabled() {
		t.Fatal("persisting artifacts to disk must stay off until it is asked for")
	}
	if c.FreeOnly() {
		t.Fatal("a free-only restriction must not be imposed on an existing config")
	}
	if c.SpendCeiling() != 0 {
		t.Fatalf("spend ceiling = %v, want unset", c.SpendCeiling())
	}
}

func TestOptimizationExplicitFalseWins(t *testing.T) {
	c := Config{}
	off := false
	c.Optimization.Automatic = &off
	if c.OptimizationEnabled() {
		t.Fatal("an explicit false must turn the automatic policy off")
	}
	c.Optimization.LocalAssistant = &off
	if c.LocalAssistantEnabled() {
		t.Fatal("local assistance cannot be on when explicitly false")
	}
}

func TestOptimizationOptIns(t *testing.T) {
	yes := true
	c := Config{Optimization: OptimizationPrefs{LocalAssistant: &yes, PersistentContext: &yes}}
	if !c.LocalAssistantEnabled() || !c.PersistentContextEnabled() {
		t.Fatal("explicit opt-ins must be honoured")
	}
}

// An existing requestEngine.promptCache setting is a decision the user already
// made. The optimization block must not quietly overturn it.
func TestExistingPromptCacheSettingWins(t *testing.T) {
	yes := true
	auto := "optimize"
	c := Config{Optimization: OptimizationPrefs{PromptCache: &auto, Automatic: &yes}}
	c.RequestEngine.PromptCache = "preserve"
	if got := c.EffectivePromptCache(); got != "preserve" {
		t.Fatalf("EffectivePromptCache = %q, want the user's existing requestEngine value", got)
	}

	c.RequestEngine.PromptCache = ""
	if got := c.EffectivePromptCache(); got != "optimize" {
		t.Fatalf("EffectivePromptCache = %q, want the optimization value when unset", got)
	}
}

// A negative ceiling is a configuration error, not "unlimited".
func TestOptimizationRejectsNegativeCeiling(t *testing.T) {
	c := Config{SchemaVersion: SchemaVersion, Bind: "127.0.0.1", Port: 8317, Optimization: OptimizationPrefs{SpendCeilingUSD: -1}}
	if err := c.Validate(); err == nil {
		t.Fatal("a negative spend ceiling must be rejected")
	}
	c.Optimization.SpendCeilingUSD = 25
	if err := c.Validate(); err != nil {
		t.Fatalf("a valid ceiling must pass validation: %v", err)
	}
}

// An unknown policy version is refused rather than guessed at, so a config
// written by a newer PeaProxy is not silently misread by an older one.
func TestOptimizationRejectsUnknownPolicyVersion(t *testing.T) {
	c := Config{SchemaVersion: SchemaVersion, Bind: "127.0.0.1", Port: 8317, Optimization: OptimizationPrefs{PolicyVersion: 99}}
	if err := c.Validate(); err == nil {
		t.Fatal("an unknown policy version must be rejected")
	}
	c.Optimization.PolicyVersion = OptimizationPolicyVersion
	if err := c.Validate(); err != nil {
		t.Fatalf("the current policy version must validate: %v", err)
	}
}

// Anonymous providers and paid automatic routes both need consent that an
// older config never gave.
func TestOptimizationDefaultsRefuseUnconsentedProviders(t *testing.T) {
	var c Config
	if c.AllowAnonymousProviders() {
		t.Fatal("an anonymous provider must not be assumed acceptable")
	}
	yes := true
	c.Optimization.AllowAnonymousProviders = &yes
	if !c.AllowAnonymousProviders() {
		t.Fatal("an explicit opt-in must allow anonymous providers")
	}
}
