package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// PiImportModel is one live model to add to a Pi models.json provider. Only
// the fields Pi needs for an openai-completions custom provider are kept.
type PiImportModel struct {
	ID            string   `json:"id"`
	AccountID     string   `json:"accountId,omitempty"`
	Reasoning     bool     `json:"reasoning,omitempty"`
	Input         []string `json:"input,omitempty"`
	ContextWindow int      `json:"contextWindow,omitempty"`
	MaxTokens     int      `json:"maxTokens,omitempty"`
}

// piBuiltinPrefixes are the families Pi already serves with its own built-in
// providers. Importing them into a custom provider would route them through a
// different wire than Pi expects, so they are never imported.
var piBuiltinPrefixes = []string{"claude-", "gpt-"}

func piImportable(id string) bool {
	for _, p := range piBuiltinPrefixes {
		if strings.HasPrefix(id, p) {
			return false
		}
	}
	return id != ""
}

// ownedPiProviders lists top-level provider names that use the account prefix.
func ownedPiProviders(providers []byte) []string {
	var doc map[string]json.RawMessage
	if json.Unmarshal(providers, &doc) != nil {
		return nil
	}
	var names []string
	for name := range doc {
		if strings.HasPrefix(name, "peaproxy-") {
			names = append(names, name)
		}
	}
	return names
}

// piOwnedProvider names the PeaProxy-owned provider for one account.
func piOwnedProvider(account string) string { return "peaproxy-" + account }

// importPi adds or replaces the owned provider for account with the importable
// models. Re-running with the same models yields identical bytes. A provider
// with that name that PeaProxy does not own is never overwritten.
func importablePiModels(account string, models []PiImportModel) []PiImportModel {
	var keep []PiImportModel
	for _, m := range models {
		if m.AccountID != "" && m.AccountID != account {
			continue
		}
		if piImportable(m.ID) {
			m.AccountID = ""
			keep = append(keep, m)
		}
	}
	return keep
}

func importPi(raw []byte, account, baseURL string, models []PiImportModel) ([]byte, error) {
	if account == "" {
		return nil, errors.New("pi import: account is required")
	}
	body := bytes.TrimSpace(stripJSONC(raw))
	if len(body) == 0 {
		body = []byte("{}")
	}
	if !json.Valid(body) {
		return nil, errors.New("client config is not json")
	}
	providers, err := objectOrEmpty(body, "providers", "pi models: providers")
	if err != nil {
		return nil, err
	}
	name := piOwnedProvider(account)
	existing, present := getJSONKey(providers, name)
	if present {
		key, _ := getJSONKey(existing, "apiKey")
		if string(key) != `"`+ownedAPIKey+`"` {
			return nil, fmt.Errorf("pi models: providers.%s exists and is not owned by PeaProxy; refusing to overwrite", name)
		}
	}
	keep := importablePiModels(account, models)
	entry := map[string]any{
		"baseUrl": strings.TrimRight(baseURL, "/") + "/v1",
		"api":     "openai-completions",
		"apiKey":  ownedAPIKey,
		"models":  keep,
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	if providers, err = upsertJSONKey(providers, name, json.RawMessage(encoded)); err != nil {
		return nil, err
	}
	next, err := upsertJSONKey(body, "providers", json.RawMessage(providers))
	if err != nil {
		return nil, err
	}
	return formatJSON(raw, next), nil
}

// ImportPi reads the live model list from origin for account and writes the
// owned provider into Pi's models.json. It uses the same fingerprint and
// conflict check as Connect, so a concurrent edit is refused, not overwritten.
func (l Layout) ImportPi(account, origin string, models []PiImportModel) (int, error) {
	path := l.path("pi")
	if path == "" {
		return 0, ErrUnknownClient
	}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	fingerprint := hash(raw)
	updated, err := importPi(raw, account, NormalizeOrigin(origin), models)
	if err != nil {
		return 0, err
	}
	if err := beforeWrite(path); err != nil {
		return 0, err
	}
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if hash(current) != fingerprint {
		return 0, ErrConflict
	}
	if err := writeAtomic(path, updated); err != nil {
		return 0, err
	}
	return len(importablePiModels(account, models)), nil
}

// FetchPiModels reads GET /admin/catalog from origin, retaining account and
// modality metadata so it imports only models served by the selected account.
func FetchPiModels(ctx context.Context, origin, adminToken string) ([]PiImportModel, error) {
	origin = NormalizeOrigin(origin)
	if origin == "" {
		origin = DefaultOrigin
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/admin/catalog?filter=all", nil)
	if err != nil {
		return nil, err
	}
	if adminToken != "" {
		req.Header.Set("X-Admin-Token", adminToken)
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("import pi: is peaproxy serve running?\n  %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("import pi: GET /admin/catalog returned %d", resp.StatusCode)
	}
	var doc struct {
		Models []struct {
			ID            string   `json:"id"`
			AccountID     string   `json:"accountId"`
			Modalities    []string `json:"modalities"`
			ContextWindow int      `json:"contextWindow"`
			Routable      bool     `json:"routable"`
			Hidden        bool     `json:"hidden"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("import pi: decode /admin/catalog: %v", err)
	}
	out := make([]PiImportModel, 0, len(doc.Models))
	for _, m := range doc.Models {
		if !m.Routable || m.Hidden {
			continue
		}
		input := []string{"text"}
		for _, modality := range m.Modalities {
			if modality == "image" || modality == "image_in" {
				input = append(input, "image")
			}
		}
		out = append(out, PiImportModel{ID: m.ID, AccountID: m.AccountID, Input: input, ContextWindow: m.ContextWindow})
	}
	return out, nil
}
