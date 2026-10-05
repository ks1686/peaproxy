package config

import (
	"fmt"
	"strings"
)

// OptimizationPolicyVersion is the policy this build implements. A config
// carrying a different version is refused rather than guessed at, so a file
// written by a newer PeaProxy is never silently misread by an older one.
const OptimizationPolicyVersion = 1

// OptimizationPrefs are the v3 cost behaviours. The block is additive to schema
// 1: a config without it keeps working, and a field left unset takes the
// opinionated default rather than a zero value.
//
// Nullable booleans are used throughout so "the user did not say" stays
// distinguishable from "the user said no". That distinction is the difference
// between a default PeaProxy can improve on and one it must respect.
type OptimizationPrefs struct {
	PolicyVersion int `yaml:"policyVersion,omitempty" json:"policyVersion,omitempty"`

	// Automatic applies the safe optimizations without being asked. Unset
	// means on, because the point of the proxy is that a user does not have to
	// configure it. Set false to keep the pre-v3 behaviour.
	Automatic *bool `yaml:"automatic,omitempty" json:"automatic,omitempty"`

	// PromptCache is the cache policy PeaProxy applies on its own. Unset means
	// optimize where a provider is known to support it. requestEngine.promptCache,
	// when set, wins over this.
	PromptCache *string `yaml:"promptCache,omitempty" json:"promptCache,omitempty"`

	// LocalAssistant lets PeaProxy use a configured local model for its own
	// small decisions. Unset means off: nothing local is contacted until asked.
	LocalAssistant *bool `yaml:"localAssistant,omitempty" json:"localAssistant,omitempty"`

	// PersistentContext keeps stored request artifacts on disk between runs.
	// Unset means off, and ephemeral storage is still per-session.
	PersistentContext *bool `yaml:"persistentContext,omitempty" json:"persistentContext,omitempty"`

	// FreeOnly refuses any deployment that cannot prove a call will not be
	// billed. Unset means off, so an existing paid config is not broken.
	FreeOnly *bool `yaml:"freeOnly,omitempty" json:"freeOnly,omitempty"`

	// AllowAnonymousProviders permits routes that send prompts to a provider
	// without an account attached. Unset means no.
	AllowAnonymousProviders *bool `yaml:"allowAnonymousProviders,omitempty" json:"allowAnonymousProviders,omitempty"`

	// SpendCeilingUSD caps what PeaProxy may spend on a user's behalf in a
	// rolling window. Zero means no ceiling is configured; it never means
	// unlimited spending is permitted by default.
	SpendCeilingUSD float64 `yaml:"spendCeilingUSD,omitempty" json:"spendCeilingUSD,omitempty"`
}

// OptimizationEnabled reports whether PeaProxy may apply its own safe
// optimizations. An absent block means on: that is the opinionated default.
func (c Config) OptimizationEnabled() bool {
	if c.Optimization.Automatic == nil {
		return true
	}
	return *c.Optimization.Automatic
}

// LocalAssistantEnabled reports whether a local model may be used for
// PeaProxy's own small decisions. Always off unless asked for.
func (c Config) LocalAssistantEnabled() bool {
	return boolOrFalse(c.Optimization.LocalAssistant)
}

// PersistentContextEnabled reports whether stored artifacts survive a restart.
func (c Config) PersistentContextEnabled() bool {
	return boolOrFalse(c.Optimization.PersistentContext)
}

// FreeOnly reports whether routing must refuse any deployment that cannot
// prove a call stays free.
func (c Config) FreeOnly() bool {
	return boolOrFalse(c.Optimization.FreeOnly)
}

// AllowAnonymousProviders reports whether routes may reach a provider with no
// account attached. Never assumed.
func (c Config) AllowAnonymousProviders() bool {
	return boolOrFalse(c.Optimization.AllowAnonymousProviders)
}

// SpendCeiling returns the configured ceiling, or zero when none is set.
func (c Config) SpendCeiling() float64 {
	return c.Optimization.SpendCeilingUSD
}

// EffectivePromptCache resolves the cache policy. A requestEngine.promptCache
// the user already set wins over the optimization block, because that is a
// decision they made before this block existed.
func (c Config) EffectivePromptCache() string {
	if existing := strings.TrimSpace(c.RequestEngine.PromptCache); existing != "" {
		return existing
	}
	if c.Optimization.PromptCache != nil {
		if p := strings.TrimSpace(*c.Optimization.PromptCache); p != "" {
			return p
		}
	}
	if c.OptimizationEnabled() {
		return "optimize"
	}
	return "preserve"
}

func boolOrFalse(v *bool) bool {
	return v != nil && *v
}

func (c Config) validateOptimization() error {
	if v := c.Optimization.PolicyVersion; v != 0 && v != OptimizationPolicyVersion {
		return fmt.Errorf("unsupported optimization.policyVersion %d (want %d)", v, OptimizationPolicyVersion)
	}
	if c.Optimization.SpendCeilingUSD < 0 {
		return fmt.Errorf("optimization.spendCeilingUSD must not be negative")
	}
	if p := c.Optimization.PromptCache; p != nil {
		switch strings.TrimSpace(*p) {
		case "", "preserve", "optimize", "off":
		default:
			return fmt.Errorf("invalid optimization.promptCache %q (want preserve, optimize or off)", strings.TrimSpace(*p))
		}
	}
	return nil
}
