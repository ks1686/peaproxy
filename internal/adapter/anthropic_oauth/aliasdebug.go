package anthropic_oauth

import (
	"log"
	"os"
	"sort"
	"strings"
)

// Client tool names are sent upstream under Claude Code's names, and restored
// on the way back. That is deliberate: it is the name upstream validates, and a
// 400 naming a tool is a real answer. But it means the 4xx a user sees names
// tools they never wrote, which makes a third-party-app rejection hard to trace.
//
// PEAPROXY_DEBUG logs the mapping on a 4xx: account id, status, and the
// client -> upstream pairs, sorted. Nothing else. No headers, no request or
// response body, no tokens -- a debug line is still a log line, and logs are
// the thing people paste into issues.
func aliasDebugLine(account string, status int, reverse map[string]string) string {
	if len(reverse) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(reverse))
	for upstream, client := range reverse {
		pairs = append(pairs, client+" -> "+upstream)
	}
	sort.Strings(pairs)
	return "claude oauth tool aliases (upstream saw these names): " +
		"account=" + account +
		" status=" + itoa(status) +
		" aliases=[" + strings.Join(pairs, ", ") + "]"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// aliasDebug reports whether PEAPROXY_DEBUG is set. Read on each call rather
// than cached: this only runs on a 4xx, which is rare by construction, and a
// cached value would mean the flag silently does nothing if it were ever set
// after start.
func aliasDebug() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("PEAPROXY_DEBUG")))
	return v != "" && v != "0" && v != "false"
}

// noteAliasedToolNames logs the alias mapping for a client error. It is a no-op
// for a success, for a server error the alias did not cause, and whenever
// PEAPROXY_DEBUG is unset.
func noteAliasedToolNames(account string, status int, reverse map[string]string) {
	if status < 400 || status >= 500 || len(reverse) == 0 {
		return
	}
	line := aliasDebugLine(account, status, reverse)
	if line == "" || !aliasDebug() {
		return
	}
	log.Print("peaproxy: ", line)
}
