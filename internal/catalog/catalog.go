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
	FilterFreemium          Filter = "freemium"
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
	PrivacyNote       string   `json:"privacyNote,omitempty"`
	Hidden            bool     `json:"hidden,omitempty"`
	Exposed           bool     `json:"exposed"`
	Routable          bool     `json:"routable"`
}

// Query is the hide/filter/expose pass applied before serving /v1/models.
// Hide/expose affect listing only unless BlockRouting is set (CPA #5995).
type Query struct {
	Filter        Filter
	HideProviders []string
	HideModels    []string
	// ExposeModels, when non-empty and ForClients is true, is the subset
	// coding tools are allowed to see. Empty means "all non-hidden".
	ExposeModels []string
	ForClients   bool
	// BlockRouting makes hide/expose also refuse POST routing. Default false.
	BlockRouting bool
}

// Apply is an alias of List for older call sites.
func Apply(models []Model, q Query) []Model { return List(models, q) }

// List returns models that should appear in /v1/models or client pickers.
func List(models []Model, q Query) []Model {
	out := make([]Model, 0, len(models))
	for _, m := range models {
		ann := annotate(m, q)
		if !ann.Exposed {
			continue
		}
		if !matchFilter(m, q.Filter) {
			continue
		}
		out = append(out, ann)
	}
	return out
}

// AllAnnotated returns every model with hidden/exposed/routable flags for the UI.
func AllAnnotated(models []Model, q Query) []Model {
	out := make([]Model, 0, len(models))
	for _, m := range models {
		ann := annotate(m, q)
		if q.Filter != "" && q.Filter != FilterAll && !matchFilter(m, q.Filter) {
			continue
		}
		out = append(out, ann)
	}
	return out
}

// FindRoutable locates a model by id for POST routing. Hidden/unexposed models
// still match unless BlockRouting is set.
func FindRoutable(models []Model, q Query, id string) (Model, bool) {
	for _, m := range models {
		if m.ID != id {
			continue
		}
		ann := annotate(m, q)
		if !ann.Routable {
			continue
		}
		return ann, true
	}
	return Model{}, false
}

func annotate(m Model, q Query) Model {
	hidden := slices.Contains(q.HideProviders, m.Provider) || slices.Contains(q.HideModels, m.ID)
	exposed := !hidden
	if q.ForClients && len(q.ExposeModels) > 0 && !slices.Contains(q.ExposeModels, m.ID) {
		exposed = false
		hidden = true
	}
	m.Hidden = hidden
	m.Exposed = exposed
	m.Routable = true
	if q.BlockRouting && hidden {
		m.Routable = false
	}
	return m
}

func matchFilter(m Model, f Filter) bool {
	switch f {
	case FilterAll, "":
		return true
	case FilterFree:
		return m.Tier == TierFree || m.Tier == TierFreemium
	case FilterFreemium:
		return m.Tier == TierFreemium
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
