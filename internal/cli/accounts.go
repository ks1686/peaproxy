package cli

import (
	"fmt"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter/hosted"
	"github.com/ks1686/peaproxy/internal/adapters"
	"github.com/ks1686/peaproxy/internal/config"
)

func presetWarn(id string) string {
	p, ok := adapters.LookupPreset(id)
	if !ok {
		return ""
	}
	return p.Warn
}

func providerFromPreset(presetID, id, baseURL, accountID, apiKey, apiKeyEnv, tier string) (config.Provider, error) {
	preset, ok := adapters.LookupPreset(presetID)
	if !ok {
		return config.Provider{}, fmt.Errorf("unknown preset %q\n  peaproxy accounts add jan-local\n  peaproxy accounts add gpt4all-local\n  peaproxy accounts add sambanova-key\n  peaproxy accounts add workers-ai --account-id <id>", presetID)
	}
	if id == "" {
		id = preset.ID
	}
	if baseURL == "" {
		baseURL = preset.BaseURL
	}
	if tier == "" {
		tier = preset.Tier
	}
	if spec, ok := hosted.Lookup(preset.Adapter); ok {
		if accountID != "" && spec.URLPlaceholder != "" {
			if !strings.Contains(baseURL, spec.URLPlaceholder) && preset.BaseURL != "" {
				baseURL = preset.BaseURL
			}
			baseURL = strings.ReplaceAll(baseURL, spec.URLPlaceholder, strings.TrimSpace(accountID))
		}
		baseURL = spec.FillBaseURL(baseURL)
		if spec.URLPlaceholder != "" && strings.Contains(baseURL, spec.URLPlaceholder) {
			env := spec.AccountIDEnv
			if env == "" {
				env = "the documented env var"
			}
			return config.Provider{}, fmt.Errorf("preset %s needs an account id (--account-id or export %s)", preset.ID, env)
		}
	}
	if apiKeyEnv == "" && apiKey == "" {
		apiKeyEnv = preset.EnvKey
	}
	return config.Provider{
		ID:        id,
		Adapter:   preset.Adapter,
		Tier:      tier,
		BaseURL:   baseURL,
		APIKey:    apiKey,
		APIKeyEnv: apiKeyEnv,
	}, nil
}
