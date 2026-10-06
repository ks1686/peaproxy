package config

import (
	"fmt"
	"net"
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

	// ContextOptimization turns carried context on or off independently of the
	// rest of the optimization policy. Unset follows Automatic, so the v3
	// default is on and turning optimization off still turns this off.
	ContextOptimization *bool `yaml:"contextOptimization,omitempty" json:"contextOptimization,omitempty"`

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

	// LocalEndpoint configures the local helper used for PeaProxy's own small
	// decisions. It is only consulted when LocalAssistant is true.
	LocalEndpoint LocalAssistantPrefs `yaml:"localAssistantEndpoint,omitempty" json:"-"`
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

// LocalAssistantConfig returns the local helper configuration, or false when
// the user has not opted in or has not named an endpoint.
//
// Both conditions matter. The opt-in alone is not enough -- with no endpoint
// there is nothing to talk to, and guessing one would mean probing ports the
// user never mentioned.
func (c Config) LocalAssistantConfig() (LocalAssistantPrefs, bool) {
	if !c.LocalAssistantEnabled() {
		return LocalAssistantPrefs{}, false
	}
	if strings.TrimSpace(c.Optimization.LocalEndpoint.Endpoint) == "" {
		return LocalAssistantPrefs{}, false
	}
	return c.Optimization.LocalEndpoint, true
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
	if ep := strings.TrimSpace(c.Optimization.LocalEndpoint.Endpoint); ep != "" {
		if !c.LocalAssistantEnabled() {
			return fmt.Errorf("optimization.localAssistantEndpoint is set but optimization.localAssistant is false")
		}
		if !loopbackish(ep) {
			return fmt.Errorf("optimization.localAssistantEndpoint must be on the loopback interface")
		}
	}
	if t := c.Optimization.LocalEndpoint.TimeoutSeconds; t < 0 || t > 600 {
		return fmt.Errorf("optimization.localAssistantEndpoint.timeoutSeconds must be between 0 and 600")
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

// LocalAssistantPrefs configures PeaProxy's own small local inferences.
//
// Absent, or present with an empty endpoint, means the feature is off -- which
// is also the answer when localAssistant is false. The endpoint must be on the
// loopback interface; there is no setting that allows anything else, because a
// helper described as local that can be pointed at a remote host is not one.
type LocalAssistantPrefs struct {
	// Endpoint is an OpenAI-compatible base URL, such as MLX Serve on
	// http://127.0.0.1:11234/v1.
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	// Model is the model id to use. Empty means the endpoint's first model.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// TimeoutSeconds bounds one helper call. Helper work must never become the
	// slowest part of a request.
	TimeoutSeconds int `yaml:"timeoutSeconds,omitempty" json:"timeoutSeconds,omitempty"`
}

// loopbackish reports whether a base URL names this machine. It is duplicated
// from localruntime rather than imported, because config is loaded before any
// provider or runtime package and must not depend on them.
func loopbackish(raw string) bool {
	host := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		host = raw[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch strings.Trim(host, "[]") {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
