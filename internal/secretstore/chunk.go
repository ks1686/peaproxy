package secretstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// go-keyring on macOS shells out to `security`, which rejects a command line
// over 4096 bytes. The value is base64-encoded on that line, so the raw limit
// is roughly 3000 bytes, and OAuth token bundles (Codex, Antigravity) exceed
// it. Values above chunkSize are split across "<key>#<gen>.<i>" items; the
// base item, written last, holds {"peaproxyChunks":"<gen>:<n>"}. Older releases
// decode this as an empty OAuthToken rather than failing to load all accounts.
// Readers follow only the header, so a new generation becomes visible atomically and a
// failed or interleaved write never splices two values.
const (
	chunkSize       = 2000
	chunkHeader     = "peaproxy-chunks:"
	chunkJSONHeader = `{"peaproxyChunks":`
)

// chunkRef names one generation of chunks. gen "" is the pre-generation
// layout ("peaproxy-chunks:<n>" over "<key>#<i>"), still read and cleaned up.
type chunkRef struct {
	gen string
	n   int
}

func (r chunkRef) header() string {
	return chunkJSONHeader + strconv.Quote(r.gen+":"+strconv.Itoa(r.n)) + "}"
}

func (r chunkRef) key(key string, i int) string {
	if r.gen == "" {
		return key + "#" + strconv.Itoa(i)
	}
	return key + "#" + r.gen + "." + strconv.Itoa(i)
}

// parseHeader reports whether v is a chunk header and, if so, which chunks it names.
func parseHeader(key, v string) (chunkRef, bool, error) {
	rest, ok := strings.CutPrefix(v, chunkHeader)
	if strings.HasPrefix(v, chunkJSONHeader) {
		var header struct {
			Chunks string `json:"peaproxyChunks"`
		}
		if err := json.Unmarshal([]byte(v), &header); err != nil {
			return chunkRef{}, true, fmt.Errorf("%w: bad chunk header for %s", ErrUnreadable, key)
		}
		rest = header.Chunks
	} else if !ok {
		return chunkRef{}, false, nil
	}
	gen, count, hasGen := strings.Cut(rest, ":")
	if !hasGen {
		gen, count = "", rest
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 1 || (hasGen && gen == "") {
		return chunkRef{}, true, fmt.Errorf("%w: bad chunk header for %s", ErrUnreadable, key)
	}
	return chunkRef{gen: gen, n: n}, true, nil
}

func needsChunks(value string) bool {
	return len(value) > chunkSize || strings.HasPrefix(value, chunkHeader) || strings.HasPrefix(value, chunkJSONHeader)
}

// splitChunks cuts on rune starts so each item stays valid UTF-8 (Secret
// Service backends decode values as UTF-8 and would substitute U+FFFD).
func splitChunks(v string) []string {
	var out []string
	for len(v) > chunkSize {
		cut := chunkSize
		for cut > 0 && !utf8.RuneStart(v[cut]) {
			cut--
		}
		if cut == 0 {
			cut = chunkSize
		}
		out = append(out, v[:cut])
		v = v[cut:]
	}
	return append(out, v)
}

func newGen(avoid string) (string, error) {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		if g := hex.EncodeToString(b[:]); g != avoid {
			return g, nil
		}
	}
}
