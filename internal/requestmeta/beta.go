package requestmeta

import "strings"

const (
	maxAnthropicBetas   = 16
	maxAnthropicBetaLen = 64
)

// NormalizeAnthropicBeta keeps the well-formed, de-duplicated names from a
// client's anthropic-beta header, capped in count and length, so a forwarded
// value can never carry header syntax or grow without bound.
func NormalizeAnthropicBeta(header string) string {
	var kept []string
	seen := map[string]bool{}
	for _, name := range strings.Split(header, ",") {
		name = strings.TrimSpace(name)
		if !validBetaName(name) || seen[name] {
			continue
		}
		seen[name] = true
		kept = append(kept, name)
		if len(kept) == maxAnthropicBetas {
			break
		}
	}
	return strings.Join(kept, ",")
}

// MergeAnthropicBeta appends the client's betas that own does not already list.
func MergeAnthropicBeta(own, client string) string {
	if client == "" {
		return own
	}
	parts := []string{}
	seen := map[string]bool{}
	for _, name := range strings.Split(own+","+client, ",") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		parts = append(parts, name)
	}
	return strings.Join(parts, ",")
}

// DropAnthropicBeta removes the betas whose names start with any of prefixes.
func DropAnthropicBeta(betas string, prefixes ...string) string {
	var kept []string
	for _, name := range strings.Split(betas, ",") {
		drop := name == ""
		for _, p := range prefixes {
			drop = drop || strings.HasPrefix(name, p)
		}
		if !drop {
			kept = append(kept, name)
		}
	}
	return strings.Join(kept, ",")
}

func validBetaName(name string) bool {
	if name == "" || len(name) > maxAnthropicBetaLen {
		return false
	}
	for _, r := range name {
		ok := r == '-' || r == '_' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}
