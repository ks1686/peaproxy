// Package catalog is the live model inventory PeaProxy exposes to UI and clients.
// Adapters are the source of truth; this package never ships a hand-maintained
// allowlist of model IDs.
package catalog

import "slices"

// Tier is a pricing/origin tag used for catalog filters.
type Tier string

const (
	TierFree     Tier = "free"
	TierFreemium Tier = "freemium"
	TierPaid     Tier = "paid"
	TierLocal    Tier = "local"
)

// Filter selects a catalog slice for UI or /v1/models.
type Filter string

const (
	FilterAll               Filter = "all"
	FilterFree              Filter = "free"
	FilterPaid              Filter = "paid"
	FilterLocal             Filter = "local"
	FilterSubscriptionOAuth Filter = "subscription_oauth"
)

// Model is one live-discovered entry. Tags are enriched locally; IDs come from the provider.
type Model struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"displayName,omitempty"`
	Provider          string   `json:"provider"`
	AccountID         string   `json:"accountId"`
	Tier              Tier     `json:"tier"`
	Modalities        []string `json:"modalities,omitempty"`
	Status            string   `json:"status,omitempty"`
	SubscriptionOAuth bool     `json:"subscriptionOAuth,omitempty"`
}

// Query is the hide/filter/expose pass applied before serving /v1/models or the UI picker.
type Query struct {
	Filter        Filter
	HideProviders []string
	HideModels    []string
	// ExposeModels, when non-empty and ForClients is true, is the subset
	// coding tools are allowed to see. Empty means "all non-hidden".
	ExposeModels []string
	ForClients   bool
}

// Apply filters a live catalog. Hidden providers/models never appear in client lists.
func Apply(models []Model, q Query) []Model {
	out := make([]Model, 0, len(models))
	for _, m := range models {
		if hiddenProvider(q.HideProviders, m.Provider) {
			continue
		}
		if slices.Contains(q.HideModels, m.ID) {
			continue
		}
		if q.ForClients && len(q.ExposeModels) > 0 && !slices.Contains(q.ExposeModels, m.ID) {
			continue
		}
		if !matchFilter(m, q.Filter) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func hiddenProvider(hidden []string, provider string) bool {
	return slices.Contains(hidden, provider)
}

func matchFilter(m Model, f Filter) bool {
	switch f {
	case FilterAll, "":
		return true
	case FilterFree:
		return m.Tier == TierFree || m.Tier == TierFreemium
	case FilterPaid:
		return m.Tier == TierPaid
	case FilterLocal:
		return m.Tier == TierLocal
	case FilterSubscriptionOAuth:
		return m.SubscriptionOAuth
	default:
		return false
	}
}

// OpenAIModelList is the OpenAI-compatible GET /v1/models body.
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// OpenAIModel is a single /v1/models entry. OwnedBy is always peaproxy so
// clients do not bind to an upstream vendor name.
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// ToOpenAIList converts filtered catalog models into the OpenAI list shape.
func ToOpenAIList(models []Model) OpenAIModelList {
	data := make([]OpenAIModel, 0, len(models))
	for _, m := range models {
		data = append(data, OpenAIModel{
			ID:      m.ID,
			Object:  "model",
			OwnedBy: "peaproxy",
		})
	}
	return OpenAIModelList{Object: "list", Data: data}
}
