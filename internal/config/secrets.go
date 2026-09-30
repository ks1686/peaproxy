package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

type secretReader interface {
	Get(id string, kind secretstore.Kind) (string, error)
}

type secretWriter interface {
	Set(id string, kind secretstore.Kind, value string) error
	Prune(ids []string) error
}

func hydrateSecrets(path string, cfg *Config) error {
	_, err := hydrateSecretsReporting(path, cfg)
	return err
}

// hydrateSecretsReporting is hydrateSecrets plus the set of secrets it could not
// read back. SaveMerged needs that set to tell a corrupt secret apart from one
// the user deliberately deleted (#54).
func hydrateSecretsReporting(path string, cfg *Config) (unreadableSecrets, error) {
	store, err := secretstore.Open(configDir(path))
	if err != nil {
		return nil, err
	}
	return hydrateFromReporting(store, cfg, os.Stderr)
}

// secretKind is the secretstore.Kind this package stores under, named locally so
// a secretRef reads without an import in signatures.
type secretKind = secretstore.Kind

const (
	secretAPIKey secretKind = secretstore.KindAPIKey
	secretOAuth  secretKind = secretstore.KindOAuth
)

// secretRef is one account's one stored secret.
type secretRef struct {
	id   string
	kind secretKind
}

// unreadableSecrets is the set of secrets a load could not decrypt. It is
// deliberately not "the secrets that were absent": a secret the user deleted is
// absent too, and must not be treated as something to rescue from memory.
type unreadableSecrets []secretRef

func (u unreadableSecrets) has(ref secretRef) bool {
	for _, r := range u {
		if r == ref {
			return true
		}
	}
	return false
}

// hydrateFrom treats one account's unreadable or corrupt secret as absent so
// only that account needs a re-login; backend failures still abort the load.
func hydrateFrom(store secretReader, cfg *Config, warn io.Writer) error {
	_, err := hydrateFromReporting(store, cfg, warn)
	return err
}

// hydrateFromReporting is hydrateFrom plus a record of which secrets were
// unreadable rather than simply absent.
func hydrateFromReporting(store secretReader, cfg *Config, warn io.Writer) (unreadableSecrets, error) {
	var unreadable unreadableSecrets
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.APIKey == "" {
			v, ok, err := readSecret(store, p.ID, secretstore.KindAPIKey, warn, &unreadable)
			if err != nil {
				return nil, err
			}
			if ok {
				p.APIKey = v
			}
		}
		if p.OAuth == nil || p.OAuth.AccessToken == "" {
			v, ok, err := readSecret(store, p.ID, secretstore.KindOAuth, warn, &unreadable)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			var tok OAuthToken
			if jerr := json.Unmarshal([]byte(v), &tok); jerr != nil {
				warnUnreadable(warn, p.ID, secretstore.KindOAuth)
				unreadable = append(unreadable, secretRef{p.ID, secretOAuth})
				continue
			}
			p.OAuth = mergeOAuth(p.OAuth, &tok)
		}
	}
	return unreadable, nil
}

func readSecret(store secretReader, id string, kind secretstore.Kind, warn io.Writer, unreadable *unreadableSecrets) (string, bool, error) {
	v, err := store.Get(id, kind)
	switch {
	case err == nil:
		return v, true, nil
	case errors.Is(err, secretstore.ErrUnreadable):
		warnUnreadable(warn, id, kind)
		*unreadable = append(*unreadable, secretRef{id, kind})
		return "", false, nil
	case errors.Is(err, secretstore.ErrNotFound):
		return "", false, nil
	default:
		return "", false, err
	}
}

func warnUnreadable(w io.Writer, id string, kind secretstore.Kind) {
	_, _ = fmt.Fprintf(w, "peaproxy: stored %s secret for account %q is unreadable; treating it as absent (log in again)\n", kind, id)
}

func persistSecrets(path string, cfg Config) (Config, error) {
	store, err := secretstore.Open(configDir(path))
	if err != nil {
		return Config{}, err
	}
	return persistTo(store, cfg)
}

// persistTo writes every provider's secrets before giving up, so one
// account's write failure (e.g. a locked keychain) does not stop every
// later account's secret from being saved. It always builds the stripped
// disk config across all providers, but only prunes the store — a
// destructive step — when every write succeeded; a failed write must never
// lead to deleting anything. On any write failure it returns the joined
// errors, which makes config.Save abort before the YAML (still holding an
// unpersisted secret) is ever written.
func persistTo(store secretWriter, cfg Config) (Config, error) {
	disk := cfg
	disk.Providers = make([]Provider, len(cfg.Providers))
	copy(disk.Providers, cfg.Providers)
	ids := make([]string, 0, len(disk.Providers))
	var errs []error
	for i := range disk.Providers {
		p := &disk.Providers[i]
		ids = append(ids, p.ID)
		if p.APIKey != "" {
			if err := store.Set(p.ID, secretstore.KindAPIKey, p.APIKey); err != nil {
				errs = append(errs, fmt.Errorf("account %q: %w", p.ID, err))
			}
			p.APIKey = ""
		}
		if p.OAuth != nil {
			raw, err := json.Marshal(p.OAuth)
			if err != nil {
				errs = append(errs, fmt.Errorf("account %q: %w", p.ID, err))
			} else if oauthHasSecret(p.OAuth) {
				if err := store.Set(p.ID, secretstore.KindOAuth, string(raw)); err != nil {
					errs = append(errs, fmt.Errorf("account %q: %w", p.ID, err))
				}
			}
			p.OAuth = publicOAuth(p.OAuth)
		}
	}
	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
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
	// github_token is the Copilot adapter's GitHub access token. It mints
	// Copilot sessions, so it belongs here with the other bearer values: it
	// must not be written to config.yaml in the clear.
	case "dca_token", "access_token", "refresh_token", "id_token", "api_key", "password", "secret", "github_token":
		return true
	default:
		return false
	}
}
