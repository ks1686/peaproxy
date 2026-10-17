package translate

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/jsonx"
)

// fetchAvailableModels advertises the "-pro-high" slots, but v1internal
// answers them with HTTP 400. The live id for that tier is gemini-pro-agent.
var upstreamModelIDs = map[string]string{
	"gemini-3.1-pro-high": "gemini-pro-agent",
	"gemini-3-pro-high":   "gemini-pro-agent",
}

// GeminiUpstreamModelID maps a consumer-facing model to its live Cloud Code ID.
func GeminiUpstreamModelID(model string) string {
	if id, ok := upstreamModelIDs[model]; ok {
		return id
	}
	return model
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FileData         *geminiFileData         `json:"fileData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
}

type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	ID   string          `json:"id,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string             `json:"name"`
	Response geminiToolResponse `json:"response"`
	ID       string             `json:"id,omitempty"`
}

type geminiToolResponse struct {
	Content json.RawMessage `json:"content"`
}

type chatToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type geminiToolCall struct {
	Index    *int             `json:"index,omitempty"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatMessage struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []geminiToolCall `json:"tool_calls,omitempty"`
}

type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiFileData struct {
	FileURI string `json:"fileUri"`
}

type geminiFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunction `json:"functionDeclarations"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature float64 `json:"temperature,omitempty"`
}

type geminiInnerRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
	Tools             []geminiTool            `json:"tools,omitempty"`
	SessionID         string                  `json:"sessionId,omitempty"`
}

type geminiEnvelope struct {
	Project     string             `json:"project,omitempty"`
	Model       string             `json:"model"`
	UserAgent   string             `json:"userAgent"`
	RequestType string             `json:"requestType"`
	RequestID   string             `json:"requestId"`
	Request     geminiInnerRequest `json:"request"`
}

// GeminiRequestJSON builds the Cloud Code request body. The caller resolves
// project under its own lock, so this stays a pure function of its inputs.
func GeminiRequestJSON(project string, req adapter.ChatRequest, stream bool) ([]byte, error) {
	_ = stream
	model := req.Model
	type inbound struct {
		Role       string           `json:"role"`
		Content    json.RawMessage  `json:"content"`
		ToolCalls  []geminiToolCall `json:"tool_calls"`
		ToolCallID string           `json:"tool_call_id"`
	}
	var msgs []inbound
	var tools []geminiFunction
	if len(req.Raw) > 0 {
		var parsed struct {
			Model    string          `json:"model"`
			Messages []inbound       `json:"messages"`
			Tools    json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(jsonx.SetStream(req.Raw, false), &parsed); err == nil && len(parsed.Messages) > 0 {
			if parsed.Model != "" {
				model = parsed.Model
			}
			msgs = parsed.Messages
			tools = geminiFunctions(parsed.Tools)
		}
	}
	if len(msgs) == 0 {
		for _, m := range req.Messages {
			raw, _ := json.Marshal(m.Content)
			msgs = append(msgs, inbound{Role: m.Role, Content: raw})
		}
	}
	type toolCall struct {
		name  string
		order int
		id    string
	}
	var sys strings.Builder
	var contents []geminiContent
	calls := make(map[string]toolCall)
	var groupOrder []int
	previousRole := ""
	for _, m := range msgs {
		role := strings.ToLower(m.Role)
		switch role {
		case "system", "developer":
			if sys.Len() > 0 {
				sys.WriteByte('\n')
			}
			sys.WriteString(messageText(m.Content))
		case "assistant":
			calls = make(map[string]toolCall)
			var parts []geminiPart
			if messageText(m.Content) != "" {
				parts = geminiPartsFromContent(m.Content)
			}
			for i, call := range m.ToolCalls {
				args := bytes.TrimSpace([]byte(call.Function.Arguments))
				if len(args) == 0 {
					// Some OpenAI clients send "" for a call to a zero-argument tool.
					args = []byte("{}")
				}
				if args[0] != '{' || !json.Valid(args) || call.Function.Name == "" {
					return nil, fmt.Errorf("antigravity: invalid function call %q", call.ID)
				}
				// Cloud Code's OpenAI models (gpt-oss) reject an assistant
				// tool_calls element whose id is empty. Keep the client's id,
				// and mint one when the client left it blank.
				id := call.ID
				if id == "" {
					id = "call_" + strconv.Itoa(i)
				}
				calls[id] = toolCall{name: call.Function.Name, order: i, id: id}
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{ID: id, Name: call.Function.Name, Args: args},
					// Cloud Code accepts this sentinel for unsigned OpenAI history;
					// OpenAI-compatible clients do not round-trip Gemini signatures.
					ThoughtSignature: "skip_thought_signature_validator",
				})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{Role: "model", Parts: parts})
			}
		case "tool":
			call, known := calls[m.ToolCallID]
			if !known && m.ToolCallID == "" && len(calls) == 1 {
				for _, only := range calls {
					call = only
					known = true
				}
			}
			if !known {
				contents = append(contents, geminiContent{Role: "user", Parts: geminiPartsFromContent(m.Content)})
				previousRole = "user"
				continue
			}
			content := m.Content
			if len(content) == 0 {
				content = json.RawMessage("null")
			}
			part := geminiPart{FunctionResponse: &geminiFunctionResponse{ID: call.id, Name: call.name, Response: geminiToolResponse{Content: content}}}
			if previousRole != "tool" {
				contents = append(contents, geminiContent{Role: "user"})
				groupOrder = nil
			}
			// A functionResponse carries only the function name, so Gemini pairs
			// it with its functionCall by position, not by tool_call_id.
			at := sort.Search(len(groupOrder), func(j int) bool { return groupOrder[j] > call.order })
			group := &contents[len(contents)-1]
			group.Parts = slices.Insert(group.Parts, at, part)
			groupOrder = slices.Insert(groupOrder, at, call.order)
		default:
			contents = append(contents, geminiContent{Role: "user", Parts: geminiPartsFromContent(m.Content)})
		}
		previousRole = role
	}
	reqType := "agent"
	if strings.Contains(strings.ToLower(model), "image") {
		reqType = "image_gen"
	}
	env := geminiEnvelope{
		Project:     project,
		Model:       GeminiUpstreamModelID(model),
		UserAgent:   "antigravity",
		RequestType: reqType,
		RequestID:   newAgentRequestID(reqType),
		Request:     geminiInnerRequest{Contents: contents},
	}
	if reqType != "image_gen" {
		env.Request.SessionID = newSessionID()
	}
	if len(tools) > 0 {
		env.Request.Tools = []geminiTool{{FunctionDeclarations: tools}}
	}
	if sys.Len() > 0 {
		env.Request.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: sys.String()}}}
	}
	return json.Marshal(env)
}

func newSessionID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	n := int64(binary.BigEndian.Uint64(b[:]) & 0x7fffffffffffffff)
	if n == 0 {
		n = 1
	}
	return "-" + strconv.FormatInt(n, 10)
}

func newAgentRequestID(reqType string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	if reqType == "image_gen" {
		return "image_gen/" + id
	}
	return "agent-" + id
}

func geminiFunctions(raw json.RawMessage) []geminiFunction {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Function    *struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []geminiFunction
	for _, item := range items {
		fn := geminiFunction{Name: item.Name, Description: item.Description, Parameters: item.Parameters}
		if item.Function != nil && item.Function.Name != "" {
			fn = geminiFunction{Name: item.Function.Name, Description: item.Function.Description, Parameters: item.Function.Parameters}
		}
		if fn.Name == "" {
			continue
		}
		fn.Parameters = SanitizeGeminiSchema(fn.Parameters)
		out = append(out, fn)
	}
	return out
}

// sanitizeGeminiSchema reduces a JSON Schema to the fields of the Gemini
// Schema proto. Cloud Code rejects the whole request on any unknown field
// ($schema, additionalProperties, exclusiveMinimum, const, oneOf, ...).
func SanitizeGeminiSchema(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return raw
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	out, err := json.Marshal(geminiSchema(v))
	if err != nil {
		return raw
	}
	return out
}

var geminiSchemaFields = map[string]bool{
	"type": true, "format": true, "title": true, "description": true, "nullable": true,
	"enum": true, "default": true, "example": true, "required": true, "propertyOrdering": true,
	"minItems": true, "maxItems": true, "minProperties": true, "maxProperties": true,
	"minLength": true, "maxLength": true, "pattern": true, "minimum": true, "maximum": true,
}

// geminiSchema walks schema positions only (property values, items, anyOf),
// so property names such as "type" or "title" are never mistaken for keywords.
func geminiSchema(v any) any {
	in, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{}
	var anyOf, types []any
	for k, val := range in {
		switch k {
		case "properties":
			if props, ok := val.(map[string]any); ok {
				clean := make(map[string]any, len(props))
				for name, child := range props {
					clean[name] = geminiSchema(child)
				}
				out[k] = clean
			}
		case "items":
			if tuple, ok := val.([]any); ok {
				if items, kept := tupleItems(tuple); kept {
					out[k] = items
				}
			} else {
				out[k] = geminiSchema(val)
			}
		case "anyOf", "oneOf":
			if list, ok := val.([]any); ok {
				for _, child := range list {
					anyOf = append(anyOf, geminiSchema(child))
				}
			}
		case "type":
			if list, ok := val.([]any); ok {
				for _, t := range list {
					if t == "null" {
						out["nullable"] = true
					} else {
						types = append(types, t)
					}
				}
			} else {
				out[k] = val
			}
		case "exclusiveMinimum", "exclusiveMaximum":
			bound := "minimum"
			if k == "exclusiveMaximum" {
				bound = "maximum"
			}
			if _, isNum := val.(float64); isNum {
				if _, set := in[bound]; !set {
					out[bound] = val
				}
			}
		case "enum":
			// The Schema proto's enum is a list of strings; Cloud Code rejects others.
			if list, isList := val.([]any); isList && allStrings(list) {
				out[k] = list
			}
		case "const":
			if _, set := in["enum"]; !set {
				if s, isStr := val.(string); isStr {
					out["enum"] = []any{s}
				}
			}
		default:
			if geminiSchemaFields[k] {
				out[k] = val
			}
		}
	}
	switch {
	case len(types) == 1:
		out["type"] = types[0]
	case len(types) > 1:
		if kept := listedBranches(out, types, anyOf); kept != nil {
			anyOf = kept
		} else {
			anyOf = append(typeUnion(out, types), anyOf...)
		}
	}
	return collapseNullable(out, anyOf)
}

// tupleItems reduces tuple-form items, which the Schema proto lacks, to one
// schema: the element schema when all agree, else an anyOf of the distinct
// elements, folded like any other anyOf.
func tupleItems(tuple []any) (any, bool) {
	schemas := make([]any, 0, len(tuple))
	for _, el := range tuple {
		schemas = append(schemas, geminiSchema(el))
	}
	return distinctUnion(schemas)
}

// distinctUnion reduces already-sanitized schemas to one: the schema when all
// agree, else an anyOf of the distinct ones in first-seen order.
func distinctUnion(schemas []any) (any, bool) {
	var distinct []any
	for _, schema := range schemas {
		if !slices.ContainsFunc(distinct, func(seen any) bool { return reflect.DeepEqual(seen, schema) }) {
			distinct = append(distinct, schema)
		}
	}
	switch len(distinct) {
	case 0:
		return nil, false
	case 1:
		return distinct[0], true
	default:
		return collapseNullable(map[string]any{}, distinct), true
	}
}

// typeUnion turns a multi-type list into anyOf branches. The array branch takes
// the items, because Cloud Code rejects an array schema without its own items.
func typeUnion(out map[string]any, types []any) []any {
	items, hasItems := out["items"]
	branches := make([]any, 0, len(types))
	for _, t := range types {
		branch := map[string]any{"type": t}
		if t == "array" && hasItems {
			branch["items"] = items
			delete(out, "items")
		}
		branches = append(branches, branch)
	}
	return branches
}

// listedBranches intersects a multi-type list with anyOf: it keeps untyped
// branches and those whose type is listed (integer fits number), drops null
// branches unless null is listed, and gives items-less array branches the
// parent's items. When only untyped branches fit, each becomes one branch per
// listed type, so the type list is not lost. It returns nil when no non-null
// branch fits, so the caller falls back to the plain type union.
func listedBranches(out map[string]any, types, anyOf []any) []any {
	listed := map[string]bool{}
	for _, t := range types {
		if s, ok := t.(string); ok {
			listed[s] = true
		}
	}
	items, hasItems := out["items"]
	var kept []any
	fits, typedFits := false, false
	for _, b := range anyOf {
		m, ok := b.(map[string]any)
		if !ok {
			kept, fits = append(kept, b), true
			continue
		}
		raw, typed := m["type"]
		t, _ := raw.(string)
		switch {
		case t == "null":
			if out["nullable"] == true {
				kept = append(kept, b)
			}
			continue
		case typed && !listed[t] && (t != "integer" || !listed["number"]):
			continue
		}
		if _, set := m["items"]; t == "array" && hasItems && !set {
			m["items"] = items
		}
		kept, fits, typedFits = append(kept, b), true, typedFits || typed
	}
	if !fits {
		return nil
	}
	if !typedFits {
		kept = typedCopies(kept, types, items, hasItems)
	}
	if listed["array"] {
		delete(out, "items")
	}
	return kept
}

// keywordsByType are the JSON Schema keywords that constrain one type and only
// make sense on it. A shared branch carrying several of these -- as an
// untyped anyOf of a string and a number will -- cannot be copied to every
// per-type branch as it stands, or the number branch ends up with minLength and
// the string branch with minimum (#55).
//
// Anything not listed here is shared: enum, const, description, nullable,
// title, default, and any keyword we have not enumerated. Dropping a constraint
// we did not think of would silently widen a schema, which is worse than
// carrying a redundant one.
var keywordsByType = map[string]map[string]bool{
	"string":  keywordSet("minLength", "maxLength", "pattern", "format"),
	"number":  keywordSet("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "format"),
	"integer": keywordSet("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "format"),
	"array":   keywordSet("items", "minItems", "maxItems", "uniqueItems"),
	"object":  keywordSet("properties", "required", "additionalProperties", "minProperties", "maxProperties"),
}

// keywordFitsType reports whether key belongs on a branch of the given type.
// Gemini accepts `format` on a number as well as a string -- int32, double --
// which is why format is listed under both.
func keywordFitsType(key, typ string) bool {
	typed, ok := keywordsByType[typ]
	if !ok {
		return true
	}
	// A keyword another type owns is not this type's to carry, but it is also
	// not this type's to delete if it is shared with something that applies:
	// the caller copies into an independent branch, so keeping it here would
	// be wrong, and dropping it here would lose it. It stays only if no other
	// listed type claims it.
	if typed[key] {
		return true
	}
	for _, other := range schemaTypes {
		if other != typ && keywordsByType[other][key] {
			return false
		}
	}
	return true
}

func keywordSet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// schemaTypes is the set of JSON Schema type names the filter knows about.
// Not called "types": typedCopies has a parameter by that name.
var schemaTypes = []string{"string", "number", "integer", "array", "object"}

// typedCopies replaces each untyped branch with one copy per listed type,
// the array copy taking the parent's items when it has none. Null branches
// pass through.
func typedCopies(branches, types []any, items any, hasItems bool) []any {
	var out []any
	for _, b := range branches {
		m, ok := b.(map[string]any)
		if ok && m["type"] == "null" {
			out = append(out, b)
			continue
		}
		for _, t := range types {
			branch := map[string]any{}
			for k, v := range m {
				// The listed types come from the branch itself, so they are
				// strings; anything else is carried as it was.
				if name, ok := t.(string); ok && !keywordFitsType(k, name) {
					continue
				}
				branch[k] = v
			}
			branch["type"] = t
			if _, set := branch["items"]; t == "array" && hasItems && !set {
				branch["items"] = items
			}
			out = append(out, branch)
		}
	}
	return out
}

// collapseNullable folds a {"type":"null"} branch into nullable. When one
// branch is left and none of its keys conflict with the parent, it is merged
// into the parent, because Gemini requires an array schema to carry its own
// items (Optional[list] is anyOf[array, null]). A conflicting branch stays
// whole. An items-less array parent takes the items of every branch that has
// them, as one schema or their union, since every value must match one of
// those branches.
func collapseNullable(out map[string]any, anyOf []any) map[string]any {
	branches := anyOf[:0]
	for _, b := range anyOf {
		if m, ok := b.(map[string]any); ok && m["type"] == "null" && len(m) == 1 {
			out["nullable"] = true
			continue
		}
		branches = append(branches, b)
	}
	if len(branches) == 1 {
		if only, ok := branches[0].(map[string]any); ok && mergesLosslessly(out, only) {
			for k, v := range only {
				out[k] = v
			}
			return out
		}
	}
	if _, set := out["items"]; !set && out["type"] == "array" {
		var items []any
		for _, b := range branches {
			if m, ok := b.(map[string]any); ok {
				if it, has := m["items"]; has {
					items = append(items, it)
				}
			}
		}
		if union, ok := distinctUnion(items); ok {
			out["items"] = union
		}
	}
	if len(branches) > 0 {
		out["anyOf"] = branches
	}
	return out
}

func mergesLosslessly(parent, branch map[string]any) bool {
	for k, v := range branch {
		if pv, set := parent[k]; set && !reflect.DeepEqual(pv, v) {
			return false
		}
	}
	return true
}

func allStrings(list []any) bool {
	for _, v := range list {
		if _, isStr := v.(string); !isStr {
			return false
		}
	}
	return true
}

func geminiPartsFromContent(raw json.RawMessage) []geminiPart {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return []geminiPart{{Text: ""}}
	}
	if raw[0] == '"' {
		return []geminiPart{{Text: messageText(raw)}}
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return []geminiPart{{Text: messageText(raw)}}
	}
	var out []geminiPart
	for _, p := range parts {
		switch p.Type {
		case "image_url", "input_image":
			if part, ok := geminiImagePart(imageURLString(p.ImageURL)); ok {
				out = append(out, part)
			}
		default:
			if p.Text != "" {
				out = append(out, geminiPart{Text: p.Text})
			}
		}
	}
	if len(out) == 0 {
		return []geminiPart{{Text: messageText(raw)}}
	}
	return out
}

func geminiImagePart(url string) (geminiPart, bool) {
	url = strings.TrimSpace(url)
	if url == "" {
		return geminiPart{}, false
	}
	const prefix = "data:"
	if strings.HasPrefix(url, prefix) {
		rest := strings.TrimPrefix(url, prefix)
		mime, data, ok := strings.Cut(rest, ";base64,")
		if ok && mime != "" && data != "" {
			return geminiPart{InlineData: &geminiInlineData{MimeType: mime, Data: data}}, true
		}
	}
	return geminiPart{FileData: &geminiFileData{FileURI: url}}, true
}

func imageURLString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
		return ""
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.URL
	}
	return ""
}

func messageText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return string(raw)
}

func geminiChatMessage(body []byte) (chatMessage, string, error) {
	type candidate struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	}
	type promptFeedback struct {
		BlockReason string `json:"blockReason"`
	}
	var parsed struct {
		Response struct {
			Candidates     []candidate    `json:"candidates"`
			PromptFeedback promptFeedback `json:"promptFeedback"`
		} `json:"response"`
		Candidates     []candidate    `json:"candidates"`
		PromptFeedback promptFeedback `json:"promptFeedback"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return chatMessage{}, "", err
	}
	cands := parsed.Response.Candidates
	if len(cands) == 0 {
		cands = parsed.Candidates
	}
	var b strings.Builder
	message := chatMessage{Role: "assistant"}
	finish := ""
	if len(cands) == 0 && (parsed.Response.PromptFeedback.BlockReason != "" || parsed.PromptFeedback.BlockReason != "") {
		finish = promptBlocked
	}
	if len(cands) > 0 {
		finish = cands[0].FinishReason
		for _, p := range cands[0].Content.Parts {
			if p.Thought {
				continue
			}
			b.WriteString(p.Text)
			if p.FunctionCall != nil {
				args := p.FunctionCall.Args
				if len(args) == 0 || bytes.Equal(bytes.TrimSpace(args), []byte("null")) {
					args = json.RawMessage("{}")
				}
				message.ToolCalls = append(message.ToolCalls, geminiToolCall{
					ID: "call_" + strings.TrimPrefix(newAgentRequestID("agent"), "agent-"), Type: "function",
					Function: chatToolFunction{Name: p.FunctionCall.Name, Arguments: string(args)},
				})
			}
		}
	}
	message.Content = b.String()
	return message, finish, nil
}

// toOpenAIChatJSON writes the OpenAI chat.completion a Gemini body becomes.
// usage is nil when Gemini published none: an omitted object means the call was
// never measured, which is not the same as a call that measured zero tokens.
func toOpenAIChatJSON(model string, message chatMessage, finish string, usage *OpenAIChatUsage) ([]byte, error) {
	out := map[string]any{
		"id":      "peaproxy-antigravity",
		"object":  "chat.completion",
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": message, "finish_reason": geminiFinishReason(len(message.ToolCalls), finish)}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	return json.Marshal(out)
}

// promptBlocked stands in for a finish reason when Gemini rejects the prompt
// itself: it then sends promptFeedback.blockReason and no candidates.
const promptBlocked = "PROMPT_BLOCKED"

func geminiFinishReason(calls int, upstream string) string {
	switch upstream {
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", promptBlocked:
		// Like MAX_TOKENS, a blocked turn must not read as a finished tool turn.
		return "content_filter"
	}
	if calls > 0 {
		return "tool_calls"
	}
	return "stop"
}

// GeminiSSEToOpenAI translates a Cloud Code stream with the codec's own error
// text for a failed event.
func GeminiSSEToOpenAI(r io.Reader, w io.Writer, model string) error {
	return GeminiSSEToOpenAIWith(r, w, model, nil)
}

// GeminiSSEToOpenAIWith is GeminiSSEToOpenAI with the caller choosing the error
// for a failed event. The adapter passes its own policy, which maps the status
// to an HTTP error with scope and reset hint. A nil onEventError keeps the
// codec's plain error.
func GeminiSSEToOpenAIWith(r io.Reader, w io.Writer, model string, onEventError func(event []byte) error) error {
	// The role chunk commits the gateway's prelude guard, so it waits for the
	// first real upstream event; a stalled account can then still fail over.
	eventError := onEventError
	if eventError == nil {
		eventError = func(event []byte) error {
			var parsed struct {
				Error    json.RawMessage `json:"error"`
				Response struct {
					Error json.RawMessage `json:"error"`
				} `json:"response"`
			}
			if json.Unmarshal(event, &parsed) != nil || (len(parsed.Error) == 0 && len(parsed.Response.Error) == 0) {
				return nil
			}
			return fmt.Errorf("antigravity: stream error: %s", truncateEvent(event))
		}
	}
	wroteRole := false
	writeRole := func() error {
		if wroteRole {
			return nil
		}
		wroteRole = true
		return WriteOpenAIChatSSERole(w, "peaproxy-antigravity", model)
	}
	calls := 0
	upstreamFinish := ""
	// Token counts from Gemini's usageMetadata. Zero means the upstream never
	// published them, which is not the same as a call that cost nothing.
	promptTokens, completionTokens := 0, 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		payload := strings.TrimSpace(line)
		if !strings.HasPrefix(payload, "data:") {
			continue
		}
		payload = strings.TrimSpace(strings.TrimPrefix(payload, "data:"))
		if payload == "[DONE]" {
			break
		}
		if payload == "" {
			continue
		}
		event := []byte(payload)
		// A failed event ends the stream without a finish chunk, so a partial
		// answer is never reported as complete.
		if err := eventError(event); err != nil {
			return err
		}
		if u, ok := geminiEventUsage(event); ok {
			promptTokens, completionTokens = u.prompt, u.completion
		}
		delta, finish, err := geminiChatMessage(event)
		if err != nil {
			continue
		}
		if finish != "" {
			upstreamFinish = finish
		}
		if delta.Content == "" && len(delta.ToolCalls) == 0 {
			continue
		}
		delta.Role = ""
		for i := range delta.ToolCalls {
			index := calls
			delta.ToolCalls[i].Index = &index
			calls++
		}
		chunk, err := json.Marshal(map[string]any{
			"id":      "peaproxy-antigravity",
			"object":  "chat.completion.chunk",
			"model":   model,
			"choices": []map[string]any{{"index": 0, "delta": delta}},
		})
		if err != nil {
			return err
		}
		if err := writeRole(); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", chunk); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		// Same rule as every other translated wire: a cut stream is announced,
		// never completed. Returning quietly here leaves the client holding a
		// response that stops without a terminal event, which it can only
		// report as a missing finish_reason.
		if wErr := WriteOpenAIChatSSEError(w, "upstream stream ended before the response was complete: "+err.Error()); wErr != nil {
			return wErr
		}
		return err
	}
	if err := writeRole(); err != nil {
		return err
	}
	var usage *OpenAIChatUsage
	// Both halves, or nothing: one-sided counts understate the call, and the
	// ledger refuses an unmeasured call rather than pricing half of it.
	if promptTokens > 0 && completionTokens > 0 {
		usage = &OpenAIChatUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		}
	}
	return WriteOpenAIChatSSEFinishUsage(w, "peaproxy-antigravity", model, geminiFinishReason(calls, upstreamFinish), usage)
}

// geminiEventUsage reads usageMetadata from a Gemini streamed chunk. Gemini
// names the fields promptTokenCount and candidatesTokenCount; an OpenAI chat
// client expects prompt_tokens and completion_tokens.
func geminiEventUsage(event []byte) (struct{ prompt, completion int }, bool) {
	var out struct{ prompt, completion int }
	var ev struct {
		UsageMetadata *struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(event, &ev) != nil || ev.UsageMetadata == nil {
		return out, false
	}
	return struct{ prompt, completion int }{ev.UsageMetadata.PromptTokenCount, ev.UsageMetadata.CandidatesTokenCount}, true
}

// truncateEvent bounds a failed event quoted in an error, the same 240-byte
// limit the adapter uses for an error body.
func truncateEvent(b []byte) string {
	const n = 240
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

// GeminiChatToOpenAI turns a non-stream Cloud Code body into the OpenAI chat
// completion JSON and returns the assistant text the caller reports alongside
// it. The usage is passed in, so the caller decides how its counts are read.
func GeminiChatToOpenAI(model string, body []byte, usage *OpenAIChatUsage) (oa []byte, content string, err error) {
	message, finish, err := geminiChatMessage(body)
	if err != nil {
		return nil, "", err
	}
	oa, err = toOpenAIChatJSON(model, message, finish, usage)
	if err != nil {
		return nil, "", err
	}
	return oa, message.Content, nil
}

// GeminiUsage reads usageMetadata from a Gemini body or streamed event. Both
// halves or nothing: a one-sided count understates the call, and the ledger
// refuses an unmeasured call rather than pricing half of it.
func GeminiUsage(body []byte) *OpenAIChatUsage {
	u, ok := geminiEventUsage(body)
	if !ok || u.prompt <= 0 || u.completion <= 0 {
		return nil
	}
	return &OpenAIChatUsage{
		PromptTokens:     u.prompt,
		CompletionTokens: u.completion,
		TotalTokens:      u.prompt + u.completion,
	}
}
