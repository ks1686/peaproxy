package responsecache

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Eligible reports whether a request may be cached. Tools, images, and
// continuation ids are not reusable.
func Eligible(wire string, body []byte, explicit bool) bool {
	if !explicit {
		return false
	}
	switch wire {
	case "embeddings", "chat":
	default:
		return false
	}
	text := string(body)
	if strings.Contains(text, `"tools"`) || strings.Contains(text, `"previous_response_id"`) || strings.Contains(text, `"image"`) {
		return false
	}
	return true
}

// Key binds the response to the account, endpoint, model, and full body.
func Key(account, endpoint, model, wire string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.Join([]string{account, endpoint, model, wire, hex.EncodeToString(sum[:])}, "\x00")
}
