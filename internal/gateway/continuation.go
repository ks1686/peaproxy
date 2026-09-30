package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"time"
)

const continuationTTL = time.Hour

type continuationBind struct {
	Account string
	Model   string
	Until   time.Time
}

func responseID(raw []byte) string {
	var response struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return ""
	}
	return response.ID
}

func previousResponseID(raw []byte) string {
	var request struct {
		PreviousResponseID string `json:"previous_response_id"`
	}
	if json.Unmarshal(raw, &request) != nil {
		return ""
	}
	return request.PreviousResponseID
}

func (g *Gateway) bindContinuation(responseID, model, account string) {
	if responseID == "" || model == "" || account == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.continuations == nil {
		g.continuations = map[string]continuationBind{}
	}
	if len(g.continuations) >= 1024 {
		now := time.Now()
		for id, bind := range g.continuations {
			if !now.Before(bind.Until) {
				delete(g.continuations, id)
			}
		}
	}
	g.continuations[responseID] = continuationBind{Account: account, Model: model, Until: time.Now().Add(continuationTTL)}
}

func (g *Gateway) continuationByID(responseID string) (continuationBind, bool) {
	if responseID == "" {
		return continuationBind{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	bind, ok := g.continuations[responseID]
	if !ok {
		return continuationBind{}, false
	}
	if !time.Now().Before(bind.Until) {
		delete(g.continuations, responseID)
		return continuationBind{}, false
	}
	return bind, true
}

func (g *Gateway) continuationAccount(responseID, model string) string {
	if responseID == "" {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	bind, ok := g.continuations[responseID]
	if !ok {
		return ""
	}
	if !time.Now().Before(bind.Until) || bind.Model != model {
		delete(g.continuations, responseID)
		return ""
	}
	return bind.Account
}

func continuationFirst(candidates []instance, account string) []instance {
	if account == "" {
		return candidates
	}
	for _, candidate := range candidates {
		if candidate.Provider.ID == account {
			return []instance{candidate}
		}
	}
	return nil
}

// responseIDWatcher notes the response id of a streamed Responses call, so a
// follow-up carrying previous_response_id can be pinned to the account that
// produced it. The non-streaming path gets this from responseID(out); a stream
// carries the same id in its response.created and response.completed events,
// and nowhere else.
//
// It sits between the stream guard and the client, so it sees exactly the bytes
// the client will see. A failed attempt's id is discarded with reset().
type responseIDWatcher struct {
	w   io.Writer
	buf []byte
	id  string
}

func newResponseIDWatcher(w io.Writer) *responseIDWatcher {
	return &responseIDWatcher{w: w}
}

func (m *responseIDWatcher) Write(p []byte) (int, error) {
	n, err := m.w.Write(p)
	if m == nil {
		return n, err
	}
	m.buf = append(m.buf, p...)
	for {
		i := bytes.IndexByte(m.buf, '\n')
		if i < 0 {
			break
		}
		m.inspect(m.buf[:i])
		m.buf = m.buf[i+1:]
	}
	// A line that never ends would otherwise grow without bound.
	if len(m.buf) > 1<<20 {
		m.buf = m.buf[:0]
	}
	return n, err
}

// inspect records the id from one response.created or response.completed event.
// Anything else is ignored, and the cheap substring test keeps the JSON decode
// off the hot path for the delta events that make up most of a stream.
func (m *responseIDWatcher) inspect(line []byte) {
	data, ok := bytes.CutPrefix(line, []byte("data: "))
	if !ok || !bytes.Contains(data, []byte(`"response.`)) {
		return
	}
	var ev struct {
		Type     string `json:"type"`
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &ev) != nil {
		return
	}
	switch ev.Type {
	case "response.created", "response.completed":
		if ev.Response.ID != "" {
			m.id = ev.Response.ID
		}
	}
}

// reset forgets an attempt that failed, so its id is not bound to an account
// that never finished the response.
func (m *responseIDWatcher) reset() {
	m.buf = m.buf[:0]
	m.id = ""
}

// lastID is the id seen since the last reset, or "".
func (m *responseIDWatcher) lastID() string {
	if m == nil {
		return ""
	}
	return m.id
}
