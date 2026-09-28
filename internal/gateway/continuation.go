package gateway

import (
	"encoding/json"
	"time"
)

const continuationTTL = time.Hour

type continuationBind struct {
	Account string
	Model   string
	Until   time.Time
}

func responseID(raw []byte) string {
	var response struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return ""
	}
	return response.ID
}

func previousResponseID(raw []byte) string {
	var request struct {
		PreviousResponseID string `json:"previous_response_id"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return ""
	}
	return request.PreviousResponseID
}

func (g *Gateway) bindContinuation(responseID, model, account string) {
	if responseID == "" || model == "" || account == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.continuations == nil {
		g.continuations = map[string]continuationBind{}
	}
	if len(g.continuations) >= 1024 {
		now := time.Now()
		for id, bind := range g.continuations {
			if !now.Before(bind.Until) {
				delete(g.continuations, id)
			}
		}
	}
	g.continuations[responseID] = continuationBind{Account: account, Model: model, Until: time.Now().Add(continuationTTL)}
}

func (g *Gateway) continuationByID(responseID string) (continuationBind, bool) {
	if responseID == "" {
		return continuationBind{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	bind, ok := g.continuations[responseID]
	if !ok {
		return continuationBind{}, false
	}
	if !time.Now().Before(bind.Until) {
		delete(g.continuations, responseID)
		return continuationBind{}, false
	}
	return bind, true
}

func (g *Gateway) continuationAccount(responseID, model string) string {
	if responseID == "" {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	bind, ok := g.continuations[responseID]
	if !ok {
		return ""
	}
	if !time.Now().Before(bind.Until) || bind.Model != model {
		delete(g.continuations, responseID)
		return ""
	}
	return bind.Account
}

func continuationFirst(candidates []instance, account string) []instance {
	if account == "" {
		return candidates
	}
	for _, candidate := range candidates {
		if candidate.Provider.ID == account {
			return []instance{candidate}
		}
	}
	return nil
}
