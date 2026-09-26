package oauth

import (
	"net/url"
	"strings"
)

// LiabilityWarning is printed by `auth login`, the Accounts UI, and docs.
// Owner override 2026-09-26: ship subscription OAuth despite ToS/ban risk.
func LiabilityWarning() string {
	return `WARNING: Claude Pro/Max and ChatGPT/Codex subscription OAuth may violate the
provider's terms of service and can result in account suspension or ban.
PeaProxy authors are not liable for bans, suspensions, lost access, or other
damages. You proceed at your own risk.

The official, lower-risk path is an API key:
  Anthropic: https://console.anthropic.com/settings/keys  (adapter anthropic)
  OpenAI:    https://platform.openai.com/api-keys         (adapter openai)
Docs: https://docs.anthropic.com/en/api/getting-started`
}

// CallbackFromInput accepts a raw authorization code or a full redirect URL.
func CallbackFromInput(input string) (code, state string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", nil
	}
	if strings.Contains(input, "://") || strings.HasPrefix(input, "http") {
		u, perr := url.Parse(input)
		if perr != nil {
			return "", "", perr
		}
		q := u.Query()
		return q.Get("code"), q.Get("state"), nil
	}
	if i := strings.IndexByte(input, '#'); i >= 0 {
		return input[:i], input[i+1:], nil
	}
	return input, "", nil
}
