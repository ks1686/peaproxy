package anthropic_oauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf16"

	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/translate"
)

const (
	claudeCodeVersion     = "2.1.280"
	fingerprintSalt       = "59cf53e54c78"
	claudeCodeCLIIdentity = "You are Claude Code, Anthropic's official CLI for Claude."
	billingHeaderPrefix   = "x-anthropic-billing-header:"
)

// claudeLegacySystemReminderModels are first-party IDs that reject a
// mid-conversation role=system turn. Unknown/future IDs use the modern path.
var claudeLegacySystemReminderModels = map[string]struct{}{
	"claude-3-5-haiku-20241022":  {},
	"claude-3-5-haiku-latest":    {},
	"claude-3-7-sonnet-20250219": {},
	"claude-3-7-sonnet-latest":   {},
	"claude-haiku-4-5":           {},
	"claude-haiku-4-5-20251001":  {},
	"claude-opus-4":              {},
	"claude-opus-4-20250514":     {},
	"claude-opus-4-1":            {},
	"claude-opus-4-1-20250805":   {},
	"claude-opus-4-5":            {},
	"claude-opus-4-5-20251101":   {},
	"claude-opus-4-6":            {},
	"claude-opus-4-7":            {},
	"claude-sonnet-4":            {},
	"claude-sonnet-4-20250514":   {},
	"claude-sonnet-4-5":          {},
	"claude-sonnet-4-5-20250929": {},
	"claude-sonnet-4-6":          {},
}

type cloakBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

type cloakMessage struct {
	Role    string       `json:"role"`
	Content []cloakBlock `json:"content"`
}

var ephemeralCache = cacheControl{Type: "ephemeral"}

// applyOAuthCloak prepends Claude Code's billing header + CLI identity system
// blocks (non-strict): caller system text is relocated, never deleted.
func applyOAuthCloak(raw []byte) []byte {
	if alreadyClaudeCodeCloak(raw) {
		return raw
	}
	forwarded := collectCallerSystemTexts(raw)
	billing := generateBillingHeader(firstUserText(raw))
	system := []cloakBlock{
		{Type: "text", Text: billing},
		{Type: "text", Text: claudeCodeCLIIdentity, CacheControl: &ephemeralCache},
	}
	raw = jsonx.SetTopLevelRaw(raw, "system", marshalJSON(system))
	if len(forwarded) == 0 {
		return raw
	}
	if claudeHistoryHasAdvisor(raw) {
		for _, text := range forwarded {
			system = append(system, cloakBlock{Type: "text", Text: text})
		}
		return jsonx.SetTopLevelRaw(raw, "system", marshalJSON(system))
	}
	model := translate.CanonicalClaudeModel(jsonx.PeekBody(raw).Model)
	if claudeUsesLegacySystemReminder(model) {
		return prependCallerSystemReminders(raw, forwarded)
	}
	return insertMidConversationSystem(raw, forwarded)
}

func alreadyClaudeCodeCloak(raw []byte) bool {
	for _, text := range collectSystemTexts(raw, false) {
		if isClaudeCodeAttribution(text) {
			return true
		}
	}
	return false
}

func collectCallerSystemTexts(raw []byte) []string {
	return collectSystemTexts(raw, true)
}

func collectSystemTexts(raw []byte, skipAttribution bool) []string {
	var probe struct {
		System json.RawMessage `json:"system"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.System) == 0 || string(probe.System) == "null" {
		return nil
	}
	var asString string
	if json.Unmarshal(probe.System, &asString) == nil {
		return filterSystemText(asString, skipAttribution)
	}
	var blocks []cloakBlock
	if json.Unmarshal(probe.System, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type != "" && b.Type != "text" {
			continue
		}
		out = append(out, filterSystemText(b.Text, skipAttribution)...)
	}
	return out
}

func filterSystemText(text string, skipAttribution bool) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if skipAttribution && isClaudeCodeAttribution(text) {
		return nil
	}
	return []string{text}
}

func isClaudeCodeAttribution(text string) bool {
	text = strings.TrimSpace(text)
	return text == claudeCodeCLIIdentity || strings.HasPrefix(text, billingHeaderPrefix)
}

func generateBillingHeader(messageText string) string {
	fp := computeFingerprint(messageText, claudeCodeVersion)
	var b strings.Builder
	b.WriteString(billingHeaderPrefix)
	b.WriteString(" cc_version=")
	b.WriteString(claudeCodeVersion)
	b.WriteByte('.')
	b.WriteString(fp)
	b.WriteString("; cc_entrypoint=cli; cch=00000;")
	return b.String()
}

func computeFingerprint(messageText, version string) string {
	units := utf16.Encode([]rune(messageText))
	var sampled [3]uint16
	for i, idx := range [...]int{4, 7, 20} {
		sampled[i] = '0'
		if idx < len(units) {
			sampled[i] = units[idx]
		}
	}
	sum := sha256.Sum256([]byte(fingerprintSalt + string(utf16.Decode(sampled[:])) + version))
	return hex.EncodeToString(sum[:])[:3]
}

func firstUserText(raw []byte) string {
	for _, msg := range peekClaudeMessages(raw) {
		if messageRole(msg) != "user" {
			continue
		}
		return messageContentText(msg)
	}
	return ""
}

func firstUserIndex(msgs []json.RawMessage) int {
	for i, msg := range msgs {
		if messageRole(msg) == "user" {
			return i
		}
	}
	return -1
}

func claudeUsesLegacySystemReminder(model string) bool {
	_, ok := claudeLegacySystemReminderModels[model]
	return ok
}

func insertMidConversationSystem(raw []byte, texts []string) []byte {
	msgs := peekClaudeMessages(raw)
	firstUser := firstUserIndex(msgs)
	if firstUser < 0 || len(texts) == 0 {
		return raw
	}
	insertAt := firstUser + 1
	for insertAt < len(msgs) && messageRole(msgs[insertAt]) == "user" {
		insertAt++
	}
	if len(msgs)-insertAt >= len(texts) {
		matches := true
		for i, text := range texts {
			if messageRole(msgs[insertAt+i]) != "system" || messageContentText(msgs[insertAt+i]) != text {
				matches = false
				break
			}
		}
		if matches {
			return raw
		}
	}
	inserted := make([]json.RawMessage, 0, len(texts))
	for _, text := range texts {
		inserted = append(inserted, marshalJSON(cloakMessage{
			Role:    "system",
			Content: []cloakBlock{{Type: "text", Text: text, CacheControl: &ephemeralCache}},
		}))
	}
	out := make([]json.RawMessage, 0, len(msgs)+len(inserted))
	out = append(out, msgs[:insertAt]...)
	out = append(out, inserted...)
	out = append(out, msgs[insertAt:]...)
	return setClaudeMessages(raw, out)
}

func prependCallerSystemReminders(raw []byte, texts []string) []byte {
	msgs := peekClaudeMessages(raw)
	idx := firstUserIndex(msgs)
	if idx < 0 || len(texts) == 0 {
		return raw
	}
	var turn struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msgs[idx], &turn) != nil {
		return raw
	}
	reminders := make([]cloakBlock, 0, len(texts))
	for _, text := range texts {
		reminders = append(reminders, cloakBlock{Type: "text", Text: callerSystemReminder(text)})
	}
	var asString string
	if json.Unmarshal(turn.Content, &asString) == nil {
		blocks := append(reminders, cloakBlock{Type: "text", Text: asString})
		msgs[idx] = jsonx.SetTopLevelRaw(msgs[idx], "content", marshalJSON(blocks))
		return setClaudeMessages(raw, msgs)
	}
	var existing []json.RawMessage
	if json.Unmarshal(turn.Content, &existing) != nil {
		return raw
	}
	insertAt := 0
	for insertAt < len(existing) && blockType(existing[insertAt]) == "tool_result" {
		insertAt++
	}
	reminderRaw := make([]json.RawMessage, 0, len(reminders))
	for _, b := range reminders {
		reminderRaw = append(reminderRaw, marshalJSON(b))
	}
	blocks := make([]json.RawMessage, 0, len(existing)+len(reminderRaw))
	blocks = append(blocks, existing[:insertAt]...)
	blocks = append(blocks, reminderRaw...)
	blocks = append(blocks, existing[insertAt:]...)
	msgs[idx] = jsonx.SetTopLevelRaw(msgs[idx], "content", joinRawArray(blocks))
	return setClaudeMessages(raw, msgs)
}

func callerSystemReminder(text string) string {
	var b strings.Builder
	b.WriteString(" \n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString(" ")
	return b.String()
}

func claudeHistoryHasAdvisor(raw []byte) bool {
	for _, msg := range peekClaudeMessages(raw) {
		var turn struct {
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(msg, &turn) != nil {
			continue
		}
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(turn.Content, &blocks) != nil {
			var one struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(turn.Content, &one) == nil {
				if one.Type == "advisor_tool_result" || one.Type == "advisor_redacted_result" {
					return true
				}
			}
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "advisor_tool_result", "advisor_redacted_result":
				return true
			case "server_tool_use":
				if b.Name == "advisor" {
					return true
				}
			}
		}
	}
	return false
}

func peekClaudeMessages(raw []byte) []json.RawMessage {
	var probe struct {
		Messages []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Messages
}

func setClaudeMessages(raw []byte, msgs []json.RawMessage) []byte {
	return jsonx.SetTopLevelRaw(raw, "messages", joinRawArray(msgs))
}

func joinRawArray(items []json.RawMessage) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(item)
	}
	b.WriteByte(']')
	return b.Bytes()
}

func messageRole(msg json.RawMessage) string {
	var probe struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(msg, &probe)
	return probe.Role
}

func messageContentText(msg json.RawMessage) string {
	var probe struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &probe) != nil {
		return ""
	}
	var asString string
	if json.Unmarshal(probe.Content, &asString) == nil {
		return asString
	}
	var blocks []cloakBlock
	if json.Unmarshal(probe.Content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" || b.Type == "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func blockType(raw json.RawMessage) string {
	var probe struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &probe)
	return probe.Type
}

func marshalJSON(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null")
	}
	return bytes.TrimSpace(b.Bytes())
}
