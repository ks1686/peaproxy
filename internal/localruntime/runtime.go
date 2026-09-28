// Package localruntime reports readiness for configured local model servers.
// It never scans a network or starts a process.
package localruntime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// State is an observed local model condition.
type State string

const (
	StateUnknown State = "unknown"
	StateLoading State = "loading"
	StateReady   State = "ready"
	StateBusy    State = "busy"
	StateOffline State = "offline"
)

// Snapshot is one observation.
type Snapshot struct {
	Endpoint string
	State    State
	At       time.Time
}

// ClassifyOllama maps a documented Ollama status into a readiness state.
// An unknown payload stays unknown rather than offline.
func ClassifyOllama(status int, body string) State {
	if status == 0 {
		return StateOffline
	}
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "loading"):
		return StateLoading
	case status == http.StatusTooManyRequests || strings.Contains(lower, "busy"):
		return StateBusy
	case status >= 200 && status < 300 && strings.TrimSpace(body) != "":
		return StateReady
	case status >= 500:
		return StateOffline
	default:
		return StateUnknown
	}
}

// ExactLocalStaysLocal reports that an exact local model must not gain cloud accounts.
func ExactLocalStaysLocal(local bool) bool { return local }

// ProbeLoopback asks one configured endpoint. Hosts that are not loopback are ignored.
func ProbeLoopback(ctx context.Context, endpoint string, client *http.Client) Snapshot {
	snap := Snapshot{Endpoint: endpoint, State: StateUnknown, At: time.Now()}
	if !loopback(endpoint) {
		return snap
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/api/tags", nil)
	if err != nil {
		snap.State = StateUnknown
		return snap
	}
	resp, err := client.Do(req)
	if err != nil {
		snap.State = StateOffline
		return snap
	}
	defer resp.Body.Close()
	var buf [512]byte
	n, _ := resp.Body.Read(buf[:])
	snap.State = ClassifyOllama(resp.StatusCode, string(buf[:n]))
	return snap
}

func loopback(raw string) bool {
	host := raw
	if i := strings.Index(raw, "://"); i >= 0 {
		host = raw[i+3:]
	}
	host = strings.Split(host, "/")[0]
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch strings.Trim(host, "[]") {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		ip := net.ParseIP(strings.Trim(host, "[]"))
		return ip != nil && ip.IsLoopback()
	}
}

// KnownLoopbackPorts are the only ports probed when discovering a local runtime.
var KnownLoopbackPorts = []int{11434}

// PortOpen reports whether a loopback port accepts a TCP connection.
func PortOpen(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ParseTagsReady is true when an Ollama tags document lists at least one model.
func ParseTagsReady(body []byte) bool {
	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return false
	}
	return len(doc.Models) > 0
}
