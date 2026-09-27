package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/jsonx"
	"github.com/ks1686/peaproxy/internal/translate"
)

// prepare strips an opt-in -thinking-N suffix, resolves route names, and
// returns the upstream model plus the id the client should see in the response.
func (g *Gateway) prepare(raw []byte) (upstream, client string, body []byte, budget int, err error) {
	client = strings.TrimSpace(jsonx.PeekBody(raw).Model)
	base, budget := translate.SplitThinkingSuffix(client)
	if base != client {
		quoted, mErr := json.Marshal(base)
		if mErr != nil {
			return "", "", nil, 0, mErr
		}
		raw = jsonx.SetTopLevelRaw(raw, "model", quoted)
	}
	upstream, body, err = g.resolveBody(raw)
	return upstream, client, body, budget, err
}

// ImageEditModel reads the model field from a JSON or multipart edits body.
func ImageEditModel(raw []byte, contentType string) string {
	media, params, err := mime.ParseMediaType(contentType)
	if err == nil && strings.HasPrefix(media, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return ""
		}
		r := multipart.NewReader(bytes.NewReader(raw), boundary)
		for {
			part, err := r.NextPart()
			if err != nil {
				return ""
			}
			if part.FormName() == "model" {
				b, _ := io.ReadAll(io.LimitReader(part, 512))
				return strings.TrimSpace(string(b))
			}
		}
	}
	return strings.TrimSpace(jsonx.PeekBody(raw).Model)
}

func rewriteMultipartModel(raw []byte, contentType, from, to string) ([]byte, string, bool) {
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || !strings.HasPrefix(media, "multipart/") || params["boundary"] == "" {
		return raw, contentType, false
	}
	r := multipart.NewReader(bytes.NewReader(raw), params["boundary"])
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.SetBoundary(params["boundary"]); err != nil {
		return raw, contentType, false
	}
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return raw, contentType, false
		}
		payload, err := io.ReadAll(io.LimitReader(part, 32<<20))
		if err != nil {
			return raw, contentType, false
		}
		var dst io.Writer
		if part.FileName() != "" {
			dst, err = w.CreatePart(part.Header)
		} else if part.FormName() == "model" && strings.TrimSpace(string(payload)) == from {
			dst, err = w.CreateFormField("model")
			payload = []byte(to)
		} else {
			dst, err = w.CreateFormField(part.FormName())
		}
		if err != nil {
			return raw, contentType, false
		}
		if _, err := dst.Write(payload); err != nil {
			return raw, contentType, false
		}
	}
	if err := w.Close(); err != nil {
		return raw, contentType, false
	}
	return buf.Bytes(), w.FormDataContentType(), true
}

func echoClientModel(payload []byte, client, upstream string) []byte {
	if client == "" || client == upstream || len(bytes.TrimSpace(payload)) == 0 {
		return payload
	}
	quoted, err := json.Marshal(client)
	if err != nil {
		return payload
	}
	return jsonx.SetTopLevelRaw(payload, "model", quoted)
}

func claudeAdapter(inst instance) bool {
	switch inst.Provider.Adapter {
	case "anthropic", "anthropic_oauth":
		return true
	default:
		_, ok := inst.Adapter.(adapter.NativeMessages)
		return ok
	}
}

func chatReq(inst instance, model string, raw []byte, stream bool, budget int) adapter.ChatRequest {
	req := adapter.ChatRequest{Model: model, Raw: raw, Stream: stream}
	if budget > 0 && claudeAdapter(inst) {
		req.ThinkingBudget = budget
	}
	return req
}

func claudeRaw(raw []byte, budget int) []byte {
	if budget <= 0 {
		return raw
	}
	return translate.ApplyThinkingBudget(raw, budget)
}

// routeRewriter rewrites streamed `"model":"<upstream>"` back to the client id.
type routeRewriter struct {
	w    io.Writer
	from string
	to   string
	hold []byte
}

func newRouteRewriter(w io.Writer, from, to string) io.Writer {
	if from == "" || from == to {
		return w
	}
	return &routeRewriter{w: w, from: from, to: to}
}

func (m *routeRewriter) Write(p []byte) (int, error) {
	buf := append(append([]byte(nil), m.hold...), p...)
	from := `"` + m.from + `"`
	to := `"` + m.to + `"`
	// Only rewrite the value when it is the model field, by replacing the
	// JSON string after a "model" key. A full scan keeps other copies of the
	// id (in content) unchanged.
	emit, hold := rewriteQuotedModel(buf, from, to)
	m.hold = hold
	if len(emit) > 0 {
		if _, err := m.w.Write(emit); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func rewriteQuotedModel(buf []byte, fromQuoted, toQuoted string) (emit, hold []byte) {
	from := []byte(fromQuoted)
	to := []byte(toQuoted)
	var out []byte
	for {
		key := bytes.Index(buf, []byte(`"model"`))
		if key < 0 {
			break
		}
		rest := buf[key+len(`"model"`):]
		i := skipSpace(rest)
		if i >= len(rest) {
			return append(out, buf[:key]...), append([]byte(nil), buf[key:]...)
		}
		if rest[i] != ':' {
			out = append(out, buf[:key+len(`"model"`)]...)
			buf = rest
			continue
		}
		i++
		i += skipSpace(rest[i:])
		if i >= len(rest) || (len(rest)-i < len(from) && bytes.HasPrefix(from, rest[i:])) {
			return append(out, buf[:key]...), append([]byte(nil), buf[key:]...)
		}
		if bytes.HasPrefix(rest[i:], from) {
			out = append(out, buf[:key]...)
			out = append(out, []byte(`"model":`)...)
			out = append(out, to...)
			buf = rest[i+len(from):]
			continue
		}
		out = append(out, buf[:key+len(`"model"`)]...)
		buf = rest
	}
	n := prefixHold(buf, []byte(`"model"`))
	if n > 0 {
		return append(out, buf[:len(buf)-n]...), append([]byte(nil), buf[len(buf)-n:]...)
	}
	return append(out, buf...), nil
}

func skipSpace(b []byte) int {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

func prefixHold(buf, needle []byte) int {
	max := len(needle) - 1
	if max > len(buf) {
		max = len(buf)
	}
	for n := max; n > 0; n-- {
		if bytes.HasPrefix(needle, buf[len(buf)-n:]) {
			return n
		}
	}
	return 0
}
