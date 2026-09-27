package translate

import (
	"bytes"
	"encoding/json"

	"github.com/ks1686/peaproxy/internal/jsonx"
)

const (
	kindAnthropicThinking  = "anthropic_thinking"
	kindAnthropicRedacted  = "anthropic_redacted_thinking"
	kindResponsesReasoning = "responses_reasoning"
)

// opaqueEntry is one reasoning_opaque element. Kinds are not rewritten into
// each other: an Anthropic signature is not a Responses encrypted_content.
type opaqueEntry struct {
	Kind             string          `json:"kind"`
	Thinking         string          `json:"thinking,omitempty"`
	Signature        string          `json:"signature,omitempty"`
	Data             string          `json:"data,omitempty"`
	ID               string          `json:"id,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	Status           string          `json:"status,omitempty"`
}

type responsesReasoningWire struct {
	Type             string          `json:"type"`
	ID               string          `json:"id,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	Status           string          `json:"status,omitempty"`
}

func marshalOpaque(entries []opaqueEntry) (json.RawMessage, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	parts := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		parts = append(parts, b)
	}
	return json.Marshal(parts)
}

func parseOpaque(raw json.RawMessage) ([]opaqueEntry, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []opaqueEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func opaqueFromReasoningItem(item json.RawMessage) (opaqueEntry, error) {
	var parsed struct {
		ID               string          `json:"id"`
		EncryptedContent string          `json:"encrypted_content"`
		Summary          json.RawMessage `json:"summary"`
		Status           string          `json:"status"`
	}
	if err := json.Unmarshal(item, &parsed); err != nil {
		return opaqueEntry{}, err
	}
	summary := bytes.TrimSpace(parsed.Summary)
	if string(summary) == "null" {
		summary = nil
	}
	return opaqueEntry{
		Kind:             kindResponsesReasoning,
		ID:               parsed.ID,
		EncryptedContent: parsed.EncryptedContent,
		Summary:          summary,
		Status:           parsed.Status,
	}, nil
}

// CarryResponsesReasoning copies Responses reasoning items into a
// reasoning_opaque array. Summary JSON is kept byte-for-byte.
func CarryResponsesReasoning(items []json.RawMessage) (json.RawMessage, error) {
	if len(items) == 0 {
		return nil, nil
	}
	entries := make([]opaqueEntry, 0, len(items))
	for _, item := range items {
		entry, err := opaqueFromReasoningItem(item)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return marshalOpaque(entries)
}

// ResponsesReasoningItems returns reasoning input/output items for
// responses_reasoning entries. Anthropic kinds are omitted.
func ResponsesReasoningItems(raw json.RawMessage) ([]json.RawMessage, error) {
	entries, err := parseOpaque(raw)
	if err != nil {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		if e.Kind != kindResponsesReasoning {
			continue
		}
		b, err := json.Marshal(responsesReasoningWire{
			Type:             "reasoning",
			ID:               e.ID,
			EncryptedContent: e.EncryptedContent,
			Summary:          e.Summary,
			Status:           e.Status,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// StripReasoningOpaque removes reasoning_opaque from each chat message before
// an OpenAI-compat upstream POST. Other keys stay in their original order.
func StripReasoningOpaque(raw []byte) []byte {
	return jsonx.DropKeyInArray(raw, "messages", "reasoning_opaque")
}

func withOpaque(msgs []openAIMessage, opaque json.RawMessage) []openAIMessage {
	if len(opaque) == 0 || len(msgs) == 0 {
		return msgs
	}
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			msgs[i].ReasoningOpaque = opaque
			return msgs
		}
	}
	msgs[len(msgs)-1].ReasoningOpaque = opaque
	return msgs
}
