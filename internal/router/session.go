package router

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"
)

type sessionCtxKey struct{}

// WithSession returns a context carrying a client session id.
func WithSession(ctx context.Context, id string) context.Context {
	id = strings.TrimSpace(id)
	if ctx == nil || id == "" {
		if ctx == nil {
			return context.Background()
		}
		return ctx
	}
	return context.WithValue(ctx, sessionCtxKey{}, id)
}

// SessionFrom reads a session id previously stored with WithSession.
func SessionFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(sessionCtxKey{}).(string)
	return id
}

// SessionID prefers an explicit client header, then a session field or a
// stable hash of the system prompt and the first user turn.
func SessionID(h http.Header, body []byte) string {
	if h != nil {
		for _, key := range []string{"X-Session-ID", "X-Client-Request-Id", "Session-Id"} {
			if v := clipID(h.Get(key)); v != "" {
				return v
			}
		}
	}
	return SessionFromBody(body)
}

// SessionFromBody reads session_id, conversation_id, a Claude metadata
// user id that already names a session, or a hash of the conversation prefix.
func SessionFromBody(body []byte) string {
	var peek struct {
		SessionID      string          `json:"session_id"`
		ConversationID string          `json:"conversation_id"`
		Instructions   string          `json:"instructions"`
		System         json.RawMessage `json:"system"`
		Metadata       struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &peek) != nil {
		return ""
	}
	if v := clipID(peek.SessionID); v != "" {
		return v
	}
	if v := clipID(peek.ConversationID); v != "" {
		return v
	}
	if id := strings.TrimSpace(peek.Metadata.UserID); strings.Contains(strings.ToLower(id), "session") {
		return clipID(id)
	}
	system := firstText(peek.System)
	if system == "" {
		system = peek.Instructions
	}
	user := firstUser(peek.Messages)
	if user == "" {
		user = inputUser(peek.Input)
	}
	system = clipRunes(system, 512)
	user = clipRunes(user, 512)
	if system == "" && user == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(system + "\n" + user))
	return "h:" + hex.EncodeToString(sum[:8])
}

func firstUser(msgs []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}) string {
	for _, m := range msgs {
		if strings.EqualFold(m.Role, "user") || strings.EqualFold(m.Role, "human") {
			if text := contentText(m.Content); text != "" {
				return text
			}
		}
	}
	return ""
}

func inputUser(raw json.RawMessage) string {
	raw = trimRaw(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		return contentText(raw)
	}
	var items []struct {
		Role    string          `json:"role"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
		Text    string          `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	for _, item := range items {
		if item.Text != "" && (item.Role == "" || strings.EqualFold(item.Role, "user")) && item.Type != "reasoning" {
			return item.Text
		}
		if strings.EqualFold(item.Role, "user") || item.Type == "message" {
			if text := contentText(item.Content); text != "" {
				return text
			}
		}
	}
	return ""
}

func firstText(raw json.RawMessage) string {
	return contentText(raw)
}

func contentText(raw json.RawMessage) string {
	raw = trimRaw(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Text == "" {
				continue
			}
			if bl.Type == "" || bl.Type == "text" || bl.Type == "input_text" {
				b.WriteString(bl.Text)
			}
		}
		return b.String()
	}
	return ""
}

func trimRaw(raw json.RawMessage) json.RawMessage {
	return json.RawMessage(strings.TrimSpace(string(raw)))
}

func clipID(s string) string {
	return clipRunes(strings.TrimSpace(s), 200)
}

func clipRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for count := 0; count < n && i < len(s); count++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}
