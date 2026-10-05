// Package localassistant lets PeaProxy use a local model for its own small
// decisions, such as classifying a request or ranking retrieved context.
//
// It is opt-in and deliberately narrow. Nothing here downloads a model, starts
// a server, or falls back to a remote provider. If the local endpoint is not
// running, the feature is simply unavailable and the caller carries on with
// what it was doing before.
package localassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/localruntime"
)

// Config is one configured local endpoint.
type Config struct {
	// Endpoint is an OpenAI-compatible base URL on the loopback interface.
	Endpoint string
	// Enabled is the user's explicit opt-in. Absent or false means PeaProxy
	// makes no request to this endpoint at all.
	Enabled bool
	Model   string
}

// Assistant is a ready-to-use local helper, or a nil-safe value that is simply
// unavailable.
type Assistant struct {
	endpoint string
	model    string
	client   *http.Client
}

// ErrNotLoopback is returned when the endpoint is not on this machine. There is
// no way to turn this into a warning: a local assistant that could be pointed
// remotely would not be one.
var ErrNotLoopback = fmt.Errorf("local assistant endpoint must be on the loopback interface")

// ErrDisabled is returned when the user has not opted in.
var ErrDisabled = fmt.Errorf("local assistant is not enabled")

// New validates a configuration and returns an assistant.
//
// It performs no I/O. Nothing is contacted, started or downloaded here, so a
// disabled or misconfigured assistant costs the user nothing.
func New(cfg Config) (*Assistant, error) {
	if !cfg.Enabled {
		return nil, ErrDisabled
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("local assistant endpoint is empty")
	}
	if !localruntime.LoopbackURL(endpoint) {
		return nil, ErrNotLoopback
	}
	return &Assistant{
		endpoint: strings.TrimRight(endpoint, "/"),
		model:    strings.TrimSpace(cfg.Model),
		client:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Ready reports whether a local model server is actually listening.
//
// It is a probe, not a promise: a server can answer /v1/models and still be
// unable to serve a completion.
func (a *Assistant) Ready() (bool, error) {
	if a == nil {
		return false, ErrDisabled
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+"/models", nil)
	if err != nil {
		return false, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		// Not running is an ordinary state, not a failure worth alarming about.
		return false, nil
	}
	defer resp.Body.Close()

	// Only a JSON model list counts. Something else on the port is not a model
	// server, and prompts must not be sent to it on the strength of a 200.
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || len(body.Data) == 0 {
		return false, nil
	}
	return true, nil
}

// Model returns the configured model id, or the first the endpoint offers.
func (a *Assistant) Model() string {
	if a == nil {
		return ""
	}
	return a.model
}
