package config

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/ks1686/peaproxy/internal/secretstore"
)

func configDir(path string) string {
	dir := filepath.Dir(path)
	if dir == "" {
		return "."
	}
	return dir
}

// OpenStore opens the secret backend for a config file path.
func OpenStore(configPath string) (*secretstore.Store, error) {
	return secretstore.Open(configDir(configPath))
}

func hydrateSecrets(path string, cfg *Config) error {
	store, err := secretstore.Open(configDir(path))
	if err != nil {
		return err
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.APIKey == "" {
			v, gerr := store.Get(p.ID, secretstore.KindAPIKey)
			if gerr == nil {
				p.APIKey = v
			} else if !errors.Is(gerr, secretstore.ErrNotFound) {
				return gerr
			}
		}
		if p.OAuth == nil || p.OAuth.AccessToken == "" {
			v, gerr := store.Get(p.ID, secretstore.KindOAuth)
			if gerr == nil {
				var tok OAuthToken
				if jerr := json.Unmarshal([]byte(v), &tok); jerr != nil {
					return jerr
				}
				p.OAuth = mergeOAuth(p.OAuth, &tok)
			} else if !errors.Is(gerr, secretstore.ErrNotFound) {
				return gerr
			}
		}
	}
	return nil
}

func persistSecrets(path string, cfg Config) (Config, error) {
	store, err := secretstore.Open(configDir(path))
	if err != nil {
		return Config{}, err
	}
	disk := cfg
	disk.Providers = make([]Provider, len(cfg.Providers))
	copy(disk.Providers, cfg.Providers)
	ids := make([]string, 0, len(disk.Providers))
	for i := range disk.Providers {
		p := &disk.Providers[i]
		ids = append(ids, p.ID)
		if p.APIKey != "" {
			if err := store.Set(p.ID, secretstore.KindAPIKey, p.APIKey); err != nil {
				return Config{}, err
			}
			p.APIKey = ""
		}
		if p.OAuth != nil {
			raw, err := json.Marshal(p.OAuth)
			if err != nil {
				return Config{}, err
			}
			if oauthHasSecret(p.OAuth) {
				if err := store.Set(p.ID, secretstore.KindOAuth, string(raw)); err != nil {
					return Config{}, err
				}
			}
			p.OAuth = publicOAuth(p.OAuth)
		}
	}
	if err := store.Prune(ids); err != nil {
		return Config{}, err
	}
	return disk, nil
}

func mergeOAuth(yamlTok, stored *OAuthToken) *OAuthToken {
	if stored == nil {
		return yamlTok
	}
	out := *stored
	if yamlTok == nil {
		return &out
	}
	if out.Email == "" {
		out.Email = yamlTok.Email
	}
	if out.AccountID == "" {
		out.AccountID = yamlTok.AccountID
	}
	if out.PlanType == "" {
		out.PlanType = yamlTok.PlanType
	}
	if out.ExpiresAt == "" {
		out.ExpiresAt = yamlTok.ExpiresAt
	}
	if out.AccessToken == "" {
		out.AccessToken = yamlTok.AccessToken
	}
	if out.RefreshToken == "" {
		out.RefreshToken = yamlTok.RefreshToken
	}
	if out.IDToken == "" {
		out.IDToken = yamlTok.IDToken
	}
	if len(yamlTok.Extra) > 0 {
		extra := make(map[string]string, len(yamlTok.Extra)+len(out.Extra))
		for k, v := range yamlTok.Extra {
			extra[k] = v
		}
		for k, v := range out.Extra {
			extra[k] = v
		}
		out.Extra = extra
	}
	return &out
}

func oauthHasSecret(t *OAuthToken) bool {
	if t == nil {
		return false
	}
	if t.AccessToken != "" || t.RefreshToken != "" || t.IDToken != "" {
		return true
	}
	for k, v := range t.Extra {
		if v != "" && extraKeyIsSecret(k) {
			return true
		}
	}
	return false
}

func publicOAuth(t *OAuthToken) *OAuthToken {
	if t == nil {
		return nil
	}
	out := &OAuthToken{
		ExpiresAt: t.ExpiresAt,
		AccountID: t.AccountID,
		Email:     t.Email,
		PlanType:  t.PlanType,
	}
	if len(t.Extra) > 0 {
		extra := make(map[string]string)
		for k, v := range t.Extra {
			if extraKeyIsSecret(k) {
				continue
			}
			extra[k] = v
		}
		if len(extra) > 0 {
			out.Extra = extra
		}
	}
	if out.ExpiresAt == "" && out.AccountID == "" && out.Email == "" && out.PlanType == "" && len(out.Extra) == 0 {
		return &OAuthToken{}
	}
	return out
}

func extraKeyIsSecret(k string) bool {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case "dca_token", "access_token", "refresh_token", "id_token", "api_key", "password", "secret":
		return true
	default:
		return false
	}
}
