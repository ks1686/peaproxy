package config

import (
	"fmt"
	"strings"
	"time"
)

const (
	defaultRequestMaxAttempts = 3
	defaultRequestDeadline    = 2 * time.Minute
	defaultPreludeTimeout     = 30 * time.Second
)

// RequestEnginePrefs bounds work across retries and accounts. It is additive
// to schema 1 so existing configurations retain their current behavior.
type RequestEnginePrefs struct {
	MaxAttempts     int    `yaml:"maxAttempts,omitempty" json:"maxAttempts,omitempty"`
	Deadline        string `yaml:"deadline,omitempty" json:"deadline,omitempty"`
	PreludeTimeout  string `yaml:"preludeTimeout,omitempty" json:"preludeTimeout,omitempty"`
	PromptCache     string `yaml:"promptCache,omitempty" json:"promptCache,omitempty"`
	CacheResponses  bool   `yaml:"cacheResponses,omitempty" json:"cacheResponses,omitempty"`
	CacheEmbeddings bool   `yaml:"cacheEmbeddings,omitempty" json:"cacheEmbeddings,omitempty"`
	MaxInFlight     int    `yaml:"maxInFlight,omitempty" json:"maxInFlight,omitempty"`
}

// PriceQuote is an optional verified price. Nil amounts stay unknown.
type PriceQuote struct {
	Input    *float64 `yaml:"input,omitempty" json:"input,omitempty"`
	Output   *float64 `yaml:"output,omitempty" json:"output,omitempty"`
	Verified bool     `yaml:"verified,omitempty" json:"verified,omitempty"`
}

// AutomaticRoutePrefs enables the explicit pea/* routes. Disabled is the default.
type AutomaticRoutePrefs struct {
	Enabled       bool                  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Auto          []string              `yaml:"auto,omitempty" json:"auto,omitempty"`
	Economy       []string              `yaml:"economy,omitempty" json:"economy,omitempty"`
	Local         []string              `yaml:"local,omitempty" json:"local,omitempty"`
	Free          []string              `yaml:"free,omitempty" json:"free,omitempty"`
	CloudFallback bool                  `yaml:"cloudFallback,omitempty" json:"cloudFallback,omitempty"`
	Prices        map[string]PriceQuote `yaml:"prices,omitempty" json:"prices,omitempty"`
}

// Models returns the configured preference list for an automatic route.
func (p AutomaticRoutePrefs) Models(route string) []string {
	switch route {
	case "pea/economy":
		return p.Economy
	case "pea/local":
		return p.Local
	case "pea/free":
		return p.Free
	default:
		return p.Auto
	}
}

// RequestMaxAttempts returns the request-wide attempt cap.
func (c Config) RequestMaxAttempts() int {
	if c.RequestEngine.MaxAttempts > 0 {
		return c.RequestEngine.MaxAttempts
	}
	return defaultRequestMaxAttempts
}

// RequestDeadline returns the request-wide deadline.
func (c Config) RequestDeadline() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(c.RequestEngine.Deadline)); err == nil && d > 0 {
		return d
	}
	return defaultRequestDeadline
}

// StreamPreludeTimeout is how long a stream may wait for its first valid event.
func (c Config) StreamPreludeTimeout() time.Duration {
	if d, err := time.ParseDuration(strings.TrimSpace(c.RequestEngine.PreludeTimeout)); err == nil && d > 0 {
		return d
	}
	return defaultPreludeTimeout
}

func (c Config) validateRequestEngine() error {
	if c.RequestEngine.MaxAttempts < 0 || (c.RequestEngine.MaxAttempts == 0 && strings.TrimSpace(c.RequestEngine.Deadline) != "") {
		return fmt.Errorf("requestEngine.maxAttempts must be at least 1")
	}
	if s := strings.TrimSpace(c.RequestEngine.Deadline); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid requestEngine.deadline %q", c.RequestEngine.Deadline)
		}
	}
	if s := strings.TrimSpace(c.RequestEngine.PreludeTimeout); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return fmt.Errorf("invalid requestEngine.preludeTimeout %q", c.RequestEngine.PreludeTimeout)
		}
	}
	return nil
}
