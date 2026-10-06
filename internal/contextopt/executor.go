package contextopt

import (
	"strings"

	"github.com/ks1686/peaproxy/internal/contextstore"
)

// Search runs one retrieval against the session store and returns passages.
//
// It returns an empty string when nothing matches. Inventing a plausible
// answer here would be the worst possible failure: the model would reason over
// material that does not exist, and nothing downstream could tell.
func Search(s *contextstore.Store, session, query string) string {
	terms := map[string]bool{}
	collectTerms(query, terms)
	for t := range terms {
		if len([]rune(t)) < 3 {
			delete(terms, t)
		}
	}
	if len(terms) == 0 {
		return ""
	}
	passages := Prefetch{Store: s, Session: session}.rank(terms, DefaultMaxPassages, DefaultMaxBytes)
	return strings.Join(passages, "\n\n")
}

// RunSearches answers every proxy tool call in a response and returns the
// results keyed by call id.
func RunSearches(s *contextstore.Store, session string, calls []ToolCall) map[string]string {
	out := make(map[string]string, len(calls))
	for _, c := range calls {
		got := Search(s, session, c.Query)
		if got == "" {
			// An honest miss, said plainly.
			got = "No stored context matches that query."
		}
		out[c.ID] = got
	}
	return out
}
