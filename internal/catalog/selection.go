package catalog

import (
	"sort"
	"strconv"
	"strings"
)

// effortSuffixes are the reasoning levels a provider may encode in a model id.
// Longer suffixes are matched first so a trailing "-low" is not peeled off a
// longer name before a longer suffix can match.
var effortSuffixes = []string{"minimal", "medium", "xhigh", "none", "high", "low", "max", "off"}

// effortFallback is the sibling a synthesized base calls when the request
// names no effort. Medium is the middle setting; the cheaper levels come
// before the larger ones.
var effortFallback = []string{"medium", "low", "minimal", "high", "xhigh", "max", "none", "off"}

type effortFamily struct {
	members  map[string]string
	efforts  []string
	fallback string
	template Model
}

// binding is the live deployment id for one client-facing id.
// direct is the id when effort does not select a sibling.
// byEffort is set only when the row's efforts are those siblings.
type binding struct {
	direct   string
	byEffort map[string]string
}

// Index is the one client-id selection for a catalog snapshot.
// Clients is what a picker shows. Target is the live id a request calls.
type Index struct {
	clients []Model
	added   []Model
	bind    map[string]binding
}

// Clients is one row per id, plus ceiling aliases and synthesized effort bases.
func (x Index) Clients() []Model { return x.clients }

// Added is the rows Clients has that were not in the input.
func (x Index) Added() []Model { return x.added }

// Target resolves a client id and effort to a live model id.
// An id the index did not advertise is returned unchanged.
func (x Index) Target(id, effort string) string {
	id = strings.TrimSpace(id)
	b, ok := x.bind[id]
	if !ok {
		return id
	}
	if live, ok := b.byEffort[strings.ToLower(strings.TrimSpace(effort))]; ok && live != "" {
		return live
	}
	if b.direct != "" {
		return b.direct
	}
	return id
}

// ContextAlias is the client id for a model's published larger context ceiling.
func ContextAlias(id string, ceiling int) string {
	return id + "@" + strconv.Itoa(ceiling)
}

// combineByID collapses duplicate ids. Context window, ceiling, and output
// cap are the maxima. Effort lists are the union. A ceiling that is not
// larger than the chosen window is dropped. Row order does not change which
// numbers win.
func combineByID(models []Model) []Model {
	if len(models) == 0 {
		return nil
	}
	index := map[string]int{}
	out := make([]Model, 0, len(models))
	for _, m := range models {
		i, ok := index[m.ID]
		if !ok {
			index[m.ID] = len(out)
			out = append(out, m)
			continue
		}
		out[i] = mergeCombined(out[i], m)
	}
	for i := range out {
		if out[i].MaxContextWindow <= out[i].ContextWindow {
			out[i].MaxContextWindow = 0
		}
	}
	return out
}

func mergeCombined(dst, src Model) Model {
	if dst.DisplayName == "" {
		dst.DisplayName = src.DisplayName
	}
	if src.ContextWindow > dst.ContextWindow {
		dst.ContextWindow = src.ContextWindow
	}
	if src.MaxContextWindow > dst.MaxContextWindow {
		dst.MaxContextWindow = src.MaxContextWindow
	}
	if src.MaxTokens > dst.MaxTokens {
		dst.MaxTokens = src.MaxTokens
	}
	if len(src.Efforts) > 0 {
		dst.Efforts = NormalizeEfforts(append(append([]string{}, dst.Efforts...), src.Efforts...))
	}
	if src.HideEffortOff {
		dst.HideEffortOff = true
	}
	if len(dst.Efforts) > 0 {
		dst.Reasoning = HasSelectableEffort(dst.Efforts)
	} else if src.Reasoning {
		dst.Reasoning = true
	}
	return dst
}

func splitEffortID(id string) (base, effort string, ok bool) {
	lower := strings.ToLower(id)
	for _, suffix := range effortSuffixes {
		mark := "-" + suffix
		if strings.HasSuffix(lower, mark) && len(lower) > len(mark) {
			return id[:len(id)-len(mark)], suffix, true
		}
	}
	return "", "", false
}

func effortFamilies(models []Model) map[string]*effortFamily {
	groups := map[string]*effortFamily{}
	for _, m := range models {
		base, effort, ok := splitEffortID(m.ID)
		if !ok || base == "" {
			continue
		}
		fam := groups[base]
		if fam == nil {
			fam = &effortFamily{members: map[string]string{}}
			groups[base] = fam
		}
		if _, exists := fam.members[effort]; exists {
			continue
		}
		fam.members[effort] = m.ID
		if fam.template.ID == "" {
			fam.template = m
		}
	}
	out := map[string]*effortFamily{}
	for base, fam := range groups {
		if len(fam.members) < 2 {
			continue
		}
		var efforts []string
		for effort := range fam.members {
			efforts = append(efforts, effort)
		}
		fam.efforts = NormalizeEfforts(efforts)
		for _, effort := range effortFallback {
			if id, ok := fam.members[effort]; ok {
				fam.fallback = id
				for _, m := range models {
					if m.ID == id {
						fam.template = m
						break
					}
				}
				break
			}
		}
		if fam.fallback == "" {
			continue
		}
		out[base] = fam
	}
	return out
}

func siblingRow(models []Model, fam *effortFamily) Model {
	ids := map[string]struct{}{}
	for _, id := range fam.members {
		ids[id] = struct{}{}
	}
	var rows []Model
	for _, m := range models {
		if _, ok := ids[m.ID]; ok {
			rows = append(rows, m)
		}
	}
	combined := combineByID(rows)
	if len(combined) == 0 {
		return fam.template
	}
	row := combined[0]
	for _, extra := range combined[1:] {
		row = mergeCombined(row, extra)
	}
	if row.MaxContextWindow <= row.ContextWindow {
		row.MaxContextWindow = 0
	}
	return row
}

// Build is the client-facing catalog and the resolve table for it.
// A published ceiling larger than the combined window becomes `{id}@{ceiling}`.
// Live effort suffixes that share a base become one base id when that base
// is not already listed. A base that already published its own efforts keeps
// them and is not redirected onto a sibling.
func Build(models []Model) Index {
	if len(models) == 0 {
		return Index{}
	}
	families := effortFamilies(models)
	combined := combineByID(models)
	bind := map[string]binding{}
	routed := map[string]*effortFamily{}
	for i := range combined {
		fam, ok := families[combined[i].ID]
		if !ok || len(combined[i].Efforts) > 0 {
			continue
		}
		combined[i].Efforts = fam.efforts
		combined[i].Reasoning = HasSelectableEffort(fam.efforts)
		bind[combined[i].ID] = binding{direct: combined[i].ID, byEffort: fam.members}
		routed[combined[i].ID] = fam
	}
	seen := make(map[string]struct{}, len(combined))
	for _, m := range combined {
		seen[m.ID] = struct{}{}
	}
	var added []Model
	var bases []string
	for base := range families {
		if _, ok := seen[base]; ok {
			continue
		}
		bases = append(bases, base)
	}
	sort.Strings(bases)
	for _, base := range bases {
		fam := families[base]
		row := siblingRow(models, fam)
		row.ID = base
		row.DisplayName = base
		row.Efforts = fam.efforts
		row.Reasoning = HasSelectableEffort(fam.efforts)
		row.AliasOf = fam.fallback
		row.Pinned = false
		row.Hidden = false
		row.Exposed = true
		row.Routable = true
		combined = append(combined, row)
		added = append(added, row)
		seen[base] = struct{}{}
		bind[base] = binding{direct: fam.fallback, byEffort: fam.members}
		routed[base] = fam
	}
	type alias struct {
		id  string
		row Model
	}
	var aliases []alias
	for _, m := range combined {
		if m.MaxContextWindow <= m.ContextWindow || m.MaxContextWindow == 0 {
			continue
		}
		id := ContextAlias(m.ID, m.MaxContextWindow)
		if _, ok := seen[id]; ok {
			continue
		}
		direct := m.ID
		if b, ok := bind[m.ID]; ok && b.direct != "" {
			direct = b.direct
		}
		row := m
		row.ID = id
		row.ContextWindow = m.MaxContextWindow
		row.MaxContextWindow = 0
		row.AliasOf = direct
		row.Pinned = false
		if m.DisplayName != "" {
			row.DisplayName = m.DisplayName + " @" + strconv.Itoa(m.MaxContextWindow)
		}
		b := binding{direct: direct}
		if fam, ok := routed[m.ID]; ok {
			b.byEffort = fam.members
		}
		bind[id] = b
		seen[id] = struct{}{}
		aliases = append(aliases, alias{id: id, row: row})
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].id < aliases[j].id })
	for _, a := range aliases {
		combined = append(combined, a.row)
		added = append(added, a.row)
	}
	return Index{clients: combined, added: added, bind: bind}
}
