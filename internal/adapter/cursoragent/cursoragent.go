// Package cursoragent drives Cursor's own CLI as an upstream.
//
// # Why a subprocess and not an RPC client
//
// Cursor publishes no documented request format for api2.cursor.sh: the
// `cursor-agent` binary is the client. Reverse-engineering the wire would mean
// PeaProxy depends on a private protocol that can change under it, which is the
// shape of breakage that cannot be caught by a test written against yesterday's
// capture. Shelling out means Cursor's client speaks to Cursor, and PeaProxy
// only has to understand its output.
//
// # Auth
//
// CURSOR_API_KEY is the supported path and is what the adapter uses when set.
// Without one the CLI falls back to the session the Cursor IDE already has on
// this machine, which is the same thing that happens when a person runs
// `cursor-agent` themselves — but it is session state rather than a credential,
// so it is not something a config file can carry or a deployment can rely on.
//
// # Scope, honestly
//
// This is an agent, not a chat completion endpoint. It takes one prompt string,
// not a message array, so a conversation is flattened into a transcript before
// it is sent, and tool calls are not representable: the CLI runs in `--mode
// ask`, which is read-only Q&A. Capabilities below say so rather than implying
// parity with the hosted providers.
package cursoragent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/catalog"
)

const (
	Name = "cursor_agent"
	// Binary is Cursor's CLI. It is not vendored: the adapter shells out to
	// whatever `cursor-agent` is on PATH.
	Binary = "cursor-agent"
	// callTimeout bounds one agent run. An agent turn is tens of seconds even
	// when nothing is wrong, and the gateway's own deadline is separate.
	callTimeout = 5 * time.Minute
)

// ErrNoBinary means the Cursor CLI is not installed. It is reported at
// validation time, not at first use, so a misconfigured account says so up
// front instead of failing every call with an exec error.
var ErrNoBinary = errors.New("cursor_agent: " + Binary + " is not on PATH")

type Adapter struct {
	id       string
	binary   string
	apiKey   string
	endpoint string
	workDir  string
}

// New validates the CLI is present. An account that cannot run is rejected
// here rather than at request time.
func New(opts adapter.Options) (adapter.Adapter, error) {
	bin, err := exec.LookPath(Binary)
	if err != nil {
		return nil, ErrNoBinary
	}
	id := opts.ID
	if id == "" {
		id = Name
	}
	endpoint := strings.TrimSpace(opts.BaseURL)
	if endpoint == "" {
		endpoint = opts.ExtraHeaders["X-Cursor-Endpoint"]
	}
	return &Adapter{
		id:       id,
		binary:   bin,
		apiKey:   strings.TrimSpace(opts.APIKey),
		endpoint: endpoint,
		workDir:  opts.ExtraHeaders["X-Cursor-Workdir"],
	}, nil
}

func (a *Adapter) ID() string { return a.id }

// Validate is the "can this account ever work" check, kept separate from a
// real call: the CLI's presence is knowable without spending anything, and a
// usage-quota failure is the operator's to fix, not a reason to reject the
// account at startup.
func (a *Adapter) Validate(context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return ErrNoBinary
	}
	return nil
}

// Capabilities reports what this upstream actually is.
//
// Tools are false and not unknown: `--mode ask` is read-only Q&A, so a tool
// call sent here cannot be honoured, and saying "unknown" would let routing
// believe otherwise. Vision and the rest are unknown because the CLI decides
// per model, and this adapter does not guess on its behalf.
func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		Chat:       true,
		Stream:     true,
		ListModels: true,
		Tools:      false,
		VisionIn:   false,
		ImageOut:   false,
		Embeddings: false,
		// The CLI authenticates itself, so there is no API key to manage in
		// PeaProxy's secret store unless CURSOR_API_KEY is set deliberately.
		APIKey: a.apiKey != "",
		Local:  true,
	}
}

// ListModels shells out for the model list. Cursor's CLI prints it as text, so
// this parses text; there is no JSON form to ask for.
func (a *Adapter) ListModels(ctx context.Context) ([]catalog.Model, error) {
	out, err := a.run(ctx, []string{"--list-models"}, "", nil)
	if err != nil {
		return nil, err
	}
	var models []catalog.Model
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "available models") {
			continue
		}
		// Each row is "<id> - Label". The separator is " - " and not "-":
		// ids are full of hyphens (gpt-5.3-codex), and cutting on a bare
		// hyphen turns that into "gpt" -- a model that does not exist,
		// offered to routing as though it did.
		id, _, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		models = append(models, catalog.Model{ID: id, Provider: a.id})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("cursor_agent: the CLI listed no models; output was:\n%s", out)
	}
	return models, nil
}

// Chat runs one agent turn and returns its text.
func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	out, err := a.run(ctx, []string{"-p", "--mode", "ask", "--output-format", "json"}, a.prompt(req), nil)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	text := extractText(out)
	if text == "" {
		return adapter.ChatResponse{}, fmt.Errorf("cursor_agent: the agent returned no text")
	}
	return adapter.ChatResponse{
		ID:      a.id,
		Model:   req.Model,
		Content: text,
	}, nil
}

// ChatStream runs one agent turn and forwards its partial output.
func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	_, err := a.run(ctx, []string{"-p", "--mode", "ask", "--output-format", "stream-json",
		"--stream-partial-output"}, a.prompt(req), w)
	return err
}

// prompt flattens a message array into the single prompt the CLI accepts.
//
// This is the lossy part and it is lossy on purpose: the CLI has no message
// array, so a transcript is the most faithful thing that can be sent. Roles are
// kept because the agent reads them; everything else about the original wire
// shape does not survive the trip.
func (a *Adapter) prompt(req adapter.ChatRequest) string {
	var b strings.Builder
	for _, m := range req.Messages {
		switch strings.ToLower(m.Role) {
		case "system":
			b.WriteString("System instructions:\n")
		case "assistant":
			b.WriteString("Assistant (previous turn):\n")
		default:
			b.WriteString("User:\n")
		}
		b.WriteString(m.Content)
		b.WriteString("\n\n")
	}
	b.WriteString("Assistant:")
	return strings.TrimSpace(b.String())
}

// run executes the CLI and returns stdout. In stream mode stdout is forwarded
// to dest as it arrives, because an agent turn can run for a minute and a
// client that sees nothing until the end cannot tell a slow answer from a hang.
func (a *Adapter) run(ctx context.Context, args []string, prompt string, stream io.Writer) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	full := append([]string(nil), args...)
	if prompt != "" {
		full = append(full, prompt)
	}
	cmd := exec.CommandContext(runCtx, a.binary, full...)
	cmd.Env = os.Environ()
	if a.apiKey != "" {
		cmd.Env = append(cmd.Env, "CURSOR_API_KEY="+a.apiKey)
	}
	if a.endpoint != "" {
		cmd.Env = append(cmd.Env, "CURSOR_API_ENDPOINT="+a.endpoint)
	}
	if a.workDir != "" {
		cmd.Dir = a.workDir
	}

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if stream != nil {
		cmd.Stdout = io.MultiWriter(&stdout, destWriter{stream})
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// The CLI reports refusals on stderr as prose ("You're out of usage").
		// That is the operator's actionable text, so it is surfaced rather than
		// replaced with "exit status 1".
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("cursor_agent: %s", firstMeaningfulLine(msg))
		}
		return "", fmt.Errorf("cursor_agent: %w", err)
	}
	return stdout.String(), nil
}

// destWriter adapts the streaming destination to io.Writer with a Write that
// cannot fail the agent run: a client that hung up mid-stream is not the
// upstream's error.
type destWriter struct{ w io.Writer }

func (d destWriter) Write(p []byte) (int, error) {
	if d.w == nil {
		return len(p), nil
	}
	n, err := d.w.Write(p)
	if err != nil {
		return len(p), nil
	}
	return n, nil
}

// extractText pulls the answer out of the CLI's output.
//
// Two different failures are kept apart here. Output that is not JSON at all is
// returned as-is: that is a text-mode run, and its content is the answer. JSON
// that parses but carries no recognised text field returns empty, because
// handing a client the raw payload as if it were the assistant's sentence is
// worse than reporting that this build does not understand the reply.
func extractText(out string) string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return trimmed
	}
	for _, k := range []string{"result", "text", "answer", "response", "message", "content"} {
		if s, ok := doc[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstMeaningfulLine(s string) string {
	var first string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if isBanner(line) {
			continue
		}
		if first == "" {
			first = line
		}
		if strings.Contains(line, "Error:") || strings.Contains(line, "error:") {
			return line
		}
	}
	if first != "" {
		return first
	}
	return s
}

func isBanner(line string) bool {
	return strings.Contains(line, "Workspace Trust") ||
		strings.HasPrefix(line, "Do you trust") ||
		strings.Contains(line, "execute code and access files") ||
		strings.Contains(line, "Pass --trust")
}
