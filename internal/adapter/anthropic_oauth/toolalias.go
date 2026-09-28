package anthropic_oauth

import (
	"bytes"
	"encoding/json"
	"io"
)

// oauthToolAliases are client tool names Anthropic bills as extra usage on
// subscription OAuth, and the upstream names that stay on the subscription lane.
var oauthToolAliases = []struct {
	client   string
	upstream string
}{
	{client: "todowrite", upstream: "TodoWrite"},
	{client: "mcp_manage", upstream: "use_mcp"},
}

// aliasOAuthToolNames renames subscription-sensitive tool definitions to the
// upstream names, plus matching tool_choice and historical assistant tool_use
// names. The returned map is upstream name to client name for names this body
// actually rewrote. A collision with an existing upstream name leaves that
// client name unchanged.
func aliasOAuthToolNames(raw []byte) ([]byte, map[string]string) {
	names := toolDefNames(raw)
	forward := map[string]string{}
	reverse := map[string]string{}
	for _, pair := range oauthToolAliases {
		if !names[pair.client] || names[pair.upstream] {
			continue
		}
		forward[pair.client] = pair.upstream
		reverse[pair.upstream] = pair.client
	}
	if len(forward) == 0 {
		return raw, map[string]string{}
	}
	out, ok := rewriteAliasedNames(raw, forward)
	if !ok {
		return raw, map[string]string{}
	}
	return out, reverse
}

// restoreOAuthToolNames maps upstream tool_use names back to the client names
// recorded for this request. An empty reverse map leaves the body unchanged.
func restoreOAuthToolNames(raw []byte, reverse map[string]string) []byte {
	if len(reverse) == 0 || len(raw) == 0 {
		return raw
	}
	out, ok := rewriteAliasedNames(raw, reverse)
	if !ok {
		return raw
	}
	return out
}

func toolDefNames(raw []byte) map[string]bool {
	var body struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil
	}
	names := make(map[string]bool, len(body.Tools))
	for _, tool := range body.Tools {
		if tool.Name != "" {
			names[tool.Name] = true
		}
	}
	return names
}

type nameHit struct {
	start int
	end   int
	value string
}

func rewriteAliasedNames(raw []byte, forward map[string]string) ([]byte, bool) {
	p := aliasParser{body: raw, forward: forward}
	if !p.parseValue(aliasTop) {
		return nil, false
	}
	p.skipSpace()
	if p.pos != len(raw) {
		return nil, false
	}
	if len(p.hits) == 0 {
		return raw, true
	}
	out := raw
	for i := len(p.hits) - 1; i >= 0; i-- {
		hit := p.hits[i]
		next, ok := forward[hit.value]
		if !ok || next == hit.value {
			continue
		}
		quoted, err := json.Marshal(next)
		if err != nil {
			return nil, false
		}
		spliced := make([]byte, 0, len(out)-hit.end+hit.start+len(quoted))
		spliced = append(spliced, out[:hit.start]...)
		spliced = append(spliced, quoted...)
		spliced = append(spliced, out[hit.end:]...)
		out = spliced
	}
	return out, true
}

type aliasFrame int

const (
	aliasTop aliasFrame = iota
	aliasTools
	aliasTool
	aliasChoice
	aliasMessages
	aliasMessage
	aliasContent
	aliasBlock
	aliasNested
)

type aliasParser struct {
	body    []byte
	pos     int
	forward map[string]string
	hits    []nameHit
}

func (p *aliasParser) parseValue(frame aliasFrame) bool {
	p.skipSpace()
	if p.pos >= len(p.body) {
		return false
	}
	switch p.body[p.pos] {
	case '{':
		return p.parseObject(frame)
	case '[':
		return p.parseArray(frame)
	case '"':
		_, _, ok := p.parseString()
		return ok
	default:
		return p.parseLiteral()
	}
}

func (p *aliasParser) parseArray(frame aliasFrame) bool {
	p.pos++
	p.skipSpace()
	if p.consume(']') {
		return true
	}
	child := aliasNested
	switch frame {
	case aliasTools:
		child = aliasTool
	case aliasMessages:
		child = aliasMessage
	case aliasContent:
		child = aliasBlock
	default:
		child = aliasNested
	}
	for {
		if !p.parseValue(child) {
			return false
		}
		p.skipSpace()
		if p.consume(',') {
			continue
		}
		return p.consume(']')
	}
}

func (p *aliasParser) parseObject(frame aliasFrame) bool {
	p.pos++
	p.skipSpace()
	if p.consume('}') {
		return true
	}
	var role string
	var blockType string
	var name *nameHit
	var contentHits []nameHit
	for {
		p.skipSpace()
		_, key, ok := p.parseString()
		if !ok {
			return false
		}
		p.skipSpace()
		if !p.consume(':') {
			return false
		}
		p.skipSpace()
		switch frame {
		case aliasTool, aliasChoice:
			if key == "name" && p.pos < len(p.body) && p.body[p.pos] == '"' {
				start := p.pos
				end, value, parsed := p.parseString()
				if !parsed {
					return false
				}
				if _, alias := p.forward[value]; alias {
					p.hits = append(p.hits, nameHit{start: start, end: end, value: value})
				}
				break
			}
			if !p.parseValue(aliasNested) {
				return false
			}
		case aliasMessage:
			switch key {
			case "role":
				if p.pos < len(p.body) && p.body[p.pos] == '"' {
					_, value, parsed := p.parseString()
					if !parsed {
						return false
					}
					role = value
					break
				}
				if !p.parseValue(aliasNested) {
					return false
				}
			case "content":
				before := len(p.hits)
				if !p.parseValue(aliasContent) {
					return false
				}
				contentHits = append(contentHits, p.hits[before:]...)
			default:
				if !p.parseValue(aliasNested) {
					return false
				}
			}
		case aliasBlock:
			switch key {
			case "type":
				if p.pos < len(p.body) && p.body[p.pos] == '"' {
					_, value, parsed := p.parseString()
					if !parsed {
						return false
					}
					blockType = value
					break
				}
				if !p.parseValue(aliasNested) {
					return false
				}
			case "name":
				if p.pos < len(p.body) && p.body[p.pos] == '"' {
					start := p.pos
					end, value, parsed := p.parseString()
					if !parsed {
						return false
					}
					name = &nameHit{start: start, end: end, value: value}
					break
				}
				if !p.parseValue(aliasNested) {
					return false
				}
			default:
				if !p.parseValue(aliasNested) {
					return false
				}
			}
		default:
			child := aliasNested
			switch frame {
			case aliasTop:
				switch key {
				case "tools":
					child = aliasTools
				case "tool_choice":
					child = aliasChoice
				case "messages":
					child = aliasMessages
				case "content":
					child = aliasContent
				case "content_block":
					child = aliasBlock
				default:
					child = aliasNested
				}
			default:
				child = aliasNested
			}
			if !p.parseValue(child) {
				return false
			}
		}
		p.skipSpace()
		if p.consume(',') {
			continue
		}
		if !p.consume('}') {
			return false
		}
		break
	}
	switch frame {
	case aliasMessage:
		if role != "assistant" {
			p.hits = dropHits(p.hits, contentHits)
		}
	case aliasBlock:
		if blockType == "tool_use" && name != nil {
			if _, alias := p.forward[name.value]; alias {
				p.hits = append(p.hits, *name)
			}
		}
	default:
	}
	return true
}

func dropHits(all, unwanted []nameHit) []nameHit {
	if len(unwanted) == 0 {
		return all
	}
	out := all[:0]
	for _, hit := range all {
		drop := false
		for _, ban := range unwanted {
			if hit.start == ban.start && hit.end == ban.end {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, hit)
		}
	}
	return out
}

func (p *aliasParser) parseString() (int, string, bool) {
	if p.pos >= len(p.body) || p.body[p.pos] != '"' {
		return p.pos, "", false
	}
	start := p.pos
	i := p.pos + 1
	for i < len(p.body) {
		switch p.body[i] {
		case '\\':
			if i+1 >= len(p.body) {
				return start, "", false
			}
			if p.body[i+1] == 'u' {
				if i+5 >= len(p.body) {
					return start, "", false
				}
				i += 6
				continue
			}
			i += 2
		case '"':
			i++
			var value string
			if err := json.Unmarshal(p.body[start:i], &value); err != nil {
				return start, "", false
			}
			p.pos = i
			return i, value, true
		default:
			i++
		}
	}
	return start, "", false
}

func (p *aliasParser) parseLiteral() bool {
	start := p.pos
	for p.pos < len(p.body) && !bytes.ContainsRune([]byte(",}] \t\r\n"), rune(p.body[p.pos])) {
		p.pos++
	}
	return p.pos > start
}

func (p *aliasParser) skipSpace() {
	for p.pos < len(p.body) && (p.body[p.pos] == ' ' || p.body[p.pos] == '\t' || p.body[p.pos] == '\n' || p.body[p.pos] == '\r') {
		p.pos++
	}
}

// oAuthToolSSEFilter rewrites tool_use names in Claude SSE data lines.
type oAuthToolSSEFilter struct {
	dst     io.Writer
	reverse map[string]string
	pending []byte
}

func (f *oAuthToolSSEFilter) Write(p []byte) (int, error) {
	if len(f.reverse) == 0 {
		return f.dst.Write(p)
	}
	f.pending = append(f.pending, p...)
	for {
		i := bytes.IndexByte(f.pending, '\n')
		if i < 0 {
			break
		}
		line := f.pending[:i]
		f.pending = f.pending[i+1:]
		if _, err := f.dst.Write(append(restoreSSELine(line, f.reverse), '\n')); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (f *oAuthToolSSEFilter) Flush() error {
	if len(f.pending) == 0 {
		return nil
	}
	line := restoreSSELine(f.pending, f.reverse)
	f.pending = nil
	_, err := f.dst.Write(line)
	return err
}

func restoreSSELine(line []byte, reverse map[string]string) []byte {
	idx := bytes.Index(line, []byte("data:"))
	if idx < 0 {
		return line
	}
	payload := bytes.TrimSpace(line[idx+len("data:"):])
	restored := restoreOAuthToolNames(payload, reverse)
	if bytes.Equal(restored, payload) {
		return line
	}
	out := make([]byte, 0, idx+len("data:")+1+len(restored))
	out = append(out, line[:idx+len("data:")]...)
	if idx+len("data:") < len(line) && line[idx+len("data:")] == ' ' {
		out = append(out, ' ')
	}
	return append(out, restored...)
}

func (p *aliasParser) consume(char byte) bool {
	if p.pos >= len(p.body) || p.body[p.pos] != char {
		return false
	}
	p.pos++
	return true
}
