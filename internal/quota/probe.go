package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const openRouterDefaultBase = "https://openrouter.ai/api/v1"

// ProbeURL is the documented remaining/usage GET for an adapter, if any.
func ProbeURL(adapter, baseURL string) (string, bool) {
	if adapter != "openrouter" {
		return "", false
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = openRouterDefaultBase
	}
	return base + "/key", true
}

type openRouterKeyResponse struct {
	Data struct {
		Limit          *float64 `json:"limit"`
		LimitRemaining *float64 `json:"limit_remaining"`
		Usage          *float64 `json:"usage"`
		IsFreeTier     *bool    `json:"is_free_tier"`
	} `json:"data"`
}

// ProbeOpenRouter calls documented GET /api/v1/key with the inference API key.
// null limit + null limit_remaining means unlimited per OpenRouter docs — that
// is the only path that sets CreditsUnlimited. Missing fields after a failed
// parse are unknown, not 0.
func ProbeOpenRouter(ctx context.Context, client *http.Client, accountID, apiKey, keyURL string) (Snapshot, error) {
	if strings.TrimSpace(apiKey) == "" {
		return Snapshot{}, fmt.Errorf("openrouter quota probe requires an API key")
	}
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, keyURL, nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Snapshot{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Snapshot{}, fmt.Errorf("openrouter GET /key HTTP %d", resp.StatusCode)
	}
	var parsed openRouterKeyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Snapshot{}, fmt.Errorf("openrouter GET /key: %w", err)
	}
	snap := Snapshot{
		AccountID:        accountID,
		Adapter:          "openrouter",
		Source:           SourceProbe,
		Note:             "from OpenRouter GET /key",
		CapturedAt:       nowUTC(),
		UsageCredits:     parsed.Data.Usage,
		LimitCredits:     parsed.Data.Limit,
		RemainingCredits: parsed.Data.LimitRemaining,
	}
	if parsed.Data.Limit == nil && parsed.Data.LimitRemaining == nil {
		snap.CreditsUnlimited = true
	}
	if !snap.Reported() {
		return Snapshot{}, fmt.Errorf("openrouter GET /key returned no remaining fields")
	}
	return snap, nil
}
