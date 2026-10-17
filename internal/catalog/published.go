package catalog

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
)

// effortOrder is the stable order published effort names keep. Names the
// provider sends that are not in this list are kept, after these, sorted.
var effortOrder = []string{
	"none", "off", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "persistent",
}

// TokensFromJSON reads a published token count. A positive integer is that
// count. A quoted integer is the same number. Anything else, including a
// float or a missing field, is unknown.
func TokensFromJSON(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	s := string(raw)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var unquoted string
		if json.Unmarshal(raw, &unquoted) != nil {
			return 0
		}
		s = unquoted
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt32 {
		return 0
	}
	return int(n)
}

// AssignWindows keeps a published default window and, when the provider also
// published a strictly larger ceiling, that ceiling. A lone ceiling becomes
// the window. Equal values are not a choice.
func AssignWindows(contextWindow, maxWindow int) (window, ceiling int) {
	if contextWindow < 0 {
		contextWindow = 0
	}
	if maxWindow < 0 {
		maxWindow = 0
	}
	if contextWindow == 0 {
		return maxWindow, 0
	}
	if maxWindow > contextWindow {
		return contextWindow, maxWindow
	}
	return contextWindow, 0
}

// NormalizeEfforts lowercases, trims, and dedupes provider effort names into
// a stable order. Empty input stays nil.
func NormalizeEfforts(in []string) []string {
	seen := map[string]struct{}{}
	var known, extra []string
	rank := map[string]int{}
	for i, name := range effortOrder {
		rank[name] = i
	}
	for _, raw := range in {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		if _, ok := rank[name]; ok {
			known = append(known, name)
			continue
		}
		extra = append(extra, name)
	}
	slices.SortStableFunc(known, func(a, b string) int {
		return rank[a] - rank[b]
	})
	slices.Sort(extra)
	out := append(known, extra...)
	if len(out) == 0 {
		return nil
	}
	return out
}

// HasSelectableEffort reports whether efforts contains a level other than off
// or none. Those two name "thinking off"; they are not a thinking level.
func HasSelectableEffort(efforts []string) bool {
	for _, effort := range efforts {
		switch strings.ToLower(strings.TrimSpace(effort)) {
		case "", "off", "none":
		default:
			return true
		}
	}
	return false
}

type capSupport struct {
	Supported bool `json:"supported"`
}

// ParseAnthropicList reads an Anthropic GET /v1/models body. Published
// max_input_tokens, max_tokens, and capabilities.effort become catalog facts.
// A model object without those fields is still listed.
func ParseAnthropicList(body []byte) ([]Model, error) {
	var list struct {
		Data []struct {
			ID             string          `json:"id"`
			DisplayName    string          `json:"display_name"`
			MaxInputTokens json.RawMessage `json:"max_input_tokens"`
			MaxTokens      json.RawMessage `json:"max_tokens"`
			Capabilities   *struct {
				Effort *struct {
					Supported bool       `json:"supported"`
					Low       capSupport `json:"low"`
					Medium    capSupport `json:"medium"`
					High      capSupport `json:"high"`
					XHigh     capSupport `json:"xhigh"`
					Max       capSupport `json:"max"`
				} `json:"effort"`
				Thinking *struct {
					Supported bool `json:"supported"`
					Types     *struct {
						Disabled *capSupport `json:"disabled"`
					} `json:"types"`
				} `json:"thinking"`
				ImageInput *capSupport `json:"image_input"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(list.Data))
	for _, row := range list.Data {
		if strings.TrimSpace(row.ID) == "" {
			continue
		}
		var input []string
		if row.Capabilities != nil && row.Capabilities.ImageInput != nil && row.Capabilities.ImageInput.Supported {
			input = []string{"image"}
		}
		m := Model{
			ID:          row.ID,
			DisplayName: row.DisplayName,
			Modalities:  ModalitiesFromLive(row.ID, input, nil),
			MaxTokens:   TokensFromJSON(row.MaxTokens),
		}
		m.ContextWindow, m.MaxContextWindow = AssignWindows(TokensFromJSON(row.MaxInputTokens), 0)
		if row.Capabilities != nil && row.Capabilities.Effort != nil && row.Capabilities.Effort.Supported {
			var levels []string
			add := func(name string, on capSupport) {
				if on.Supported {
					levels = append(levels, name)
				}
			}
			add("low", row.Capabilities.Effort.Low)
			add("medium", row.Capabilities.Effort.Medium)
			add("high", row.Capabilities.Effort.High)
			add("xhigh", row.Capabilities.Effort.XHigh)
			add("max", row.Capabilities.Effort.Max)
			m.Efforts = NormalizeEfforts(levels)
			m.Reasoning = HasSelectableEffort(m.Efforts)
		}
		if row.Capabilities != nil && row.Capabilities.Thinking != nil && row.Capabilities.Thinking.Supported &&
			row.Capabilities.Thinking.Types != nil && row.Capabilities.Thinking.Types.Disabled != nil &&
			!row.Capabilities.Thinking.Types.Disabled.Supported {
			m.HideEffortOff = true
		}
		out = append(out, m)
	}
	return out, nil
}

// ParseCodexCatalog reads a Codex GET /models body. models[] carries
// context_window, max_context_window, and supported_reasoning_levels. data[]
// is the OpenAI-shaped fallback. When both are present, data[] keeps its
// order and models[] fills facts data[] left out.
func ParseCodexCatalog(body []byte) ([]Model, error) {
	var list struct {
		Data   []codexModel `json:"data"`
		Models []codexModel `json:"models"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	rich := map[string]Model{}
	var richOrder []Model
	for _, row := range list.Models {
		m, ok := row.model()
		if !ok {
			continue
		}
		rich[m.ID] = m
		richOrder = append(richOrder, m)
	}
	if len(list.Data) == 0 {
		return richOrder, nil
	}
	out := make([]Model, 0, len(list.Data))
	seen := map[string]struct{}{}
	for _, row := range list.Data {
		m, ok := row.model()
		if !ok {
			continue
		}
		if extra, found := rich[m.ID]; found {
			m = mergeModelFacts(m, extra)
		}
		seen[m.ID] = struct{}{}
		out = append(out, m)
	}
	for _, m := range richOrder {
		if _, found := seen[m.ID]; found {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

type codexModel struct {
	ID                       string          `json:"id"`
	Slug                     string          `json:"slug"`
	DisplayName              string          `json:"display_name"`
	ContextWindow            json.RawMessage `json:"context_window"`
	MaxContextWindow         json.RawMessage `json:"max_context_window"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	InputModalities []string `json:"input_modalities"`
}

func (row codexModel) model() (Model, bool) {
	id := strings.TrimSpace(row.ID)
	if id == "" {
		id = strings.TrimSpace(row.Slug)
	}
	if id == "" {
		return Model{}, false
	}
	var efforts []string
	for _, level := range row.SupportedReasoningLevels {
		efforts = append(efforts, level.Effort)
	}
	efforts = NormalizeEfforts(efforts)
	window, ceiling := AssignWindows(TokensFromJSON(row.ContextWindow), TokensFromJSON(row.MaxContextWindow))
	return Model{
		ID:               id,
		DisplayName:      row.DisplayName,
		Modalities:       ModalitiesFromLive(id, row.InputModalities, nil),
		ContextWindow:    window,
		MaxContextWindow: ceiling,
		Reasoning:        HasSelectableEffort(efforts),
		Efforts:          efforts,
	}, true
}

func mergeModelFacts(dst, src Model) Model {
	if dst.DisplayName == "" {
		dst.DisplayName = src.DisplayName
	}
	if dst.ContextWindow == 0 {
		dst.ContextWindow = src.ContextWindow
	}
	if dst.MaxContextWindow == 0 {
		dst.MaxContextWindow = src.MaxContextWindow
	}
	if dst.MaxTokens == 0 {
		dst.MaxTokens = src.MaxTokens
	}
	if len(dst.Efforts) == 0 && len(src.Efforts) > 0 {
		dst.Efforts = src.Efforts
		dst.Reasoning = src.Reasoning
		dst.HideEffortOff = src.HideEffortOff
	}
	if len(dst.Modalities) == 0 {
		dst.Modalities = src.Modalities
	}
	if dst.MaxContextWindow <= dst.ContextWindow {
		dst.MaxContextWindow = 0
	}
	return dst
}
