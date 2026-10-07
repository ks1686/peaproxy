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
//
// # Platform
//
// cursor-agent ships as a POSIX program -- a shell script on macOS -- so this
// adapter is only usable where that binary runs, and its tests are skipped on
// Windows for the same reason. New rejects the account when the binary is
// absent, rather than failing every call with an exec error later.
package cursoragent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	tier     catalog.Tier
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
	tier := opts.Tier
	if tier == "" {
		tier = catalog.TierPaid
	}
	// opts.WorkDir is the real config field and wins. The header stays for a
	// caller that drives the adapter directly, but the gateway populates
	// neither, so an unset provider here must not silently fall back to the
	// directory PeaProxy happened to start in.
	workDir := strings.TrimSpace(opts.WorkDir)
	if workDir == "" {
		workDir = strings.TrimSpace(opts.ExtraHeaders["X-Cursor-Workdir"])
	}
	return &Adapter{
		id:       id,
		tier:     tier,
		binary:   bin,
		apiKey:   strings.TrimSpace(opts.APIKey),
		endpoint: endpoint,
		workDir:  workDir,
	}, nil
}

func (a *Adapter) ID() string { return a.id }

// Validate is the "can this account ever work" check, kept separate from a
// real call: the CLI's presence is knowable without spending anything, and a
// usage-quota failure is the operator's to fix, not a reason to reject the
// account at startup.
//
// It checks the binary the adapter will actually run -- the absolute path
// resolved once at construction -- rather than looking the name up on PATH
// again. The two can disagree: PATH can gain or lose cursor-agent after the
// account is configured, and the call goes to a.binary either way. Searching
// PATH here would report an adapter sound while every call fails, or reject one
// that still runs.
func (a *Adapter) Validate(context.Context) error {
	// No empty-path guard: New returns ErrNoBinary when the lookup fails, so
	// a.binary is never empty, and os.Stat("") already errors anyway. An
	// unreachable branch here would be a claim nothing can test.
	if _, err := os.Stat(a.binary); err != nil {
		return fmt.Errorf("cursor_agent: %s is not runnable: %w", a.binary, err)
	}
	return nil
}

// Capabilities reports what this upstream actually is.
//
// Tools are false and not unknown: `--mode ask` is read-only Q&A, so a tool
// call sent here cannot be honoured, and saying "unknown" would let routing
// believe otherwise. Vision and the rest are unknown because the CLI decides
// per model, and this adapter does not guess on its behalf.
// UnsupportedRequirements states the limits Capabilities() cannot: both are
// deliberate refusals, not omissions, and PeaProxy must not route tool calls or
// images here on the strength of a bool it cannot tell apart from unset.
func (a *Adapter) UnsupportedRequirements() []catalog.Requirement {
	return []catalog.Requirement{
		catalog.RequirementTools,
		catalog.RequirementVision,
		catalog.RequirementParallelTools,
	}
}

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
		models = append(models, catalog.Model{ID: id, Provider: Name})
	}
	for i := range models {
		// Adapters label their own models. Without the account id the model
		// appears in the catalog with no owner, which costs it per-account
		// pricing, the usage rollup and any health report that groups by
		// account -- all of which key on this field.
		models[i].AccountID = a.id
		models[i].Tier = a.tier
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("cursor_agent: the CLI listed no models; output was:\n%s", out)
	}
	return models, nil
}

// Chat runs one agent turn and returns its text.
func (a *Adapter) Chat(ctx context.Context, req adapter.ChatRequest) (adapter.ChatResponse, error) {
	prompt := a.prompt(req)
	if prompt == "" {
		return adapter.ChatResponse{}, errNoPrompt
	}
	args := []string{"-p", "--mode", "ask", "--output-format", "json"}
	args = append(args, a.trustArgs()...)
	// The model has to be named. Without it every routed model runs on the
	// CLI's default while the response reports whatever was asked for, which
	// makes a routing decision invisible and unfalsifiable.
	if m := strings.TrimSpace(req.Model); m != "" && m != "auto" {
		args = append(args, "--model", m)
	}
	out, err := a.run(ctx, args, prompt, nil)
	if err != nil {
		return adapter.ChatResponse{}, err
	}
	text := extractText(out)
	if text == "" {
		return adapter.ChatResponse{}, fmt.Errorf("cursor_agent: the agent returned no text")
	}
	resp := adapter.ChatResponse{
		ID:      a.id,
		Model:   req.Model,
		Content: text,
	}
	// Raw is not optional. The response-cache path keeps only once.Raw and
	// rebuilds the response from it, so a ChatResponse without it serves an
	// empty cached turn to every later caller.
	if resp.Raw, err = chatCompletionBody(req.Model, text); err != nil {
		return adapter.ChatResponse{}, err
	}
	return resp, nil
}

// chatCompletionBody builds the OpenAI-shaped body the rest of PeaProxy expects
// to replay from a cache entry.
func chatCompletionBody(model, text string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":     "cursor-" + model,
		"object": "chat.completion",
		"model":  model,
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": text},
		}},
	})
}

// ChatStream runs one agent turn and writes it as OpenAI-compatible SSE.
//
// The CLI emits newline-delimited stream-json records; the gateway's stream
// guard recognises event:/data: frames, so forwarding Cursor's bytes unchanged
// hands the client JSON that is not SSE at all. The records are translated here
// instead.
func (a *Adapter) ChatStream(ctx context.Context, req adapter.ChatRequest, w io.Writer) error {
	prompt := a.prompt(req)
	if prompt == "" {
		return errNoPrompt
	}
	args := []string{"-p", "--mode", "ask", "--output-format", "stream-json", "--stream-partial-output"}
	args = append(args, a.trustArgs()...)
	if m := strings.TrimSpace(req.Model); m != "" && m != "auto" {
		args = append(args, "--model", m)
	}
	sw := &sseWriter{w: w, model: req.Model}
	_, err := a.run(ctx, args, prompt, sw)
	if err != nil {
		return err
	}
	return sw.finish()
}

// trustArgs passes --trust only when the operator named the directory.
//
// The CLI blocks on an interactive workspace-trust prompt, which a service has
// no way to answer. Auto-trusting whatever directory PeaProxy happened to start
// in would grant an agent read and execute access to an unvetted path -- so it
// is refused, and the refusal names workDir as the way to choose one.
// Naming a directory is the operator stating which one they trust, so that one
// is passed through rather than re-asked for on every call.
func (a *Adapter) trustArgs() []string {
	if strings.TrimSpace(a.workDir) == "" {
		return nil
	}
	return []string{"--trust"}
}

// sseWriter translates the CLI's stream-json records into chat-completion SSE.
//
// A write that fails is not propagated. A client that hung up mid-stream is not
// the upstream's error, and the agent run already happened; reporting it would
// turn a disconnected reader into a failed provider call in the ledger.
type sseWriter struct {
	w        io.Writer
	model    string
	wroteAny bool
	err      error // marshalling failure only, which is ours
}

func (s *sseWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || s.err != nil {
			continue
		}
		var rec struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Done bool   `json:"done"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			// Cursor may interleave non-JSON banners. Dropping a line is
			// better than emitting it to a client that parses SSE.
			continue
		}
		if rec.Text == "" {
			continue
		}
		s.emit(map[string]any{
			"object": "chat.completion.chunk",
			"model":  s.model,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]any{"content": rec.Text},
			}},
		})
		s.wroteAny = true
	}
	return len(p), nil
}

// finish emits the terminal frame. A stream that produced no text still gets a
// stop, so a client waiting on finish_reason is not left hanging.
func (s *sseWriter) finish() error {
	if s.err != nil {
		return s.err
	}
	if !s.wroteAny {
		s.emit(map[string]any{
			"object":  "chat.completion.chunk",
			"model":   s.model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		})
		return nil
	}
	s.emit(map[string]any{
		"object":  "chat.completion.chunk",
		"model":   s.model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	})
	_, _ = io.WriteString(s.w, "data: [DONE]\n\n")
	return s.err
}

func (s *sseWriter) emit(v any) {
	if s.err != nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		s.err = err
		return
	}
	// A short write or a dead reader is deliberately not recorded as an error.
	_, _ = io.WriteString(s.w, "data: "+string(b)+"\n\n")
}

// errNoPrompt refuses a turn with no question rather than spending an agent run
// to answer nothing.
var errNoPrompt = errors.New("cursor_agent: the request carries no conversation, so there is nothing to ask")

// prompt flattens the conversation into the single prompt the CLI accepts.
//
// Raw first, because that is what the gateway actually sends: chatReq fills
// Model, Raw and Stream, and never Messages. Reading Messages alone looked
// correct -- the field is on the struct and every unit test populated it -- and
// would have sent the literal text "Assistant:" with no question in it.
//
// Raw is the OpenAI chat shape, and content may be a string or an array of
// parts, so both are read. Falling back to Messages keeps the adapter usable
// from a caller that has them.
func (a *Adapter) prompt(req adapter.ChatRequest) string {
	msgs := a.messages(req)
	if len(msgs) == 0 {
		// Nothing to ask about. Sending an empty prompt would burn an agent
		// run to produce an answer to nothing, so this is refused instead.
		return ""
	}
	var b strings.Builder
	for _, m := range msgs {
		switch strings.ToLower(m.Role) {
		case "system", "developer":
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

// messages reads the conversation out of the request, preferring Raw.
func (a *Adapter) messages(req adapter.ChatRequest) []adapter.Message {
	if len(req.Raw) > 0 {
		if msgs, err := messagesFromRaw(req.Raw); err == nil && len(msgs) > 0 {
			return msgs
		}
	}
	return req.Messages
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// messagesFromRaw reads an OpenAI-shaped chat body into adapter messages.
func messagesFromRaw(raw []byte) ([]adapter.Message, error) {
	var body struct {
		Messages []rawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]adapter.Message, 0, len(body.Messages))
	for _, m := range body.Messages {
		out = append(out, adapter.Message{Role: m.Role, Content: contentText(m.Content)})
	}
	return out, nil
}

// contentText reads content that is either a plain string or the array of parts
// the vision and tool shapes use. Image parts contribute their type rather than
// a URL: this upstream declares no vision, so there is nothing to look at.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		switch {
		case p.Text != "":
			b.WriteString(p.Text)
		case p.Type != "" && p.Type != "text":
			b.WriteString("[" + p.Type + " omitted: this upstream declares no support for it]")
		}
	}
	return b.String()
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
		// Checked here rather than in New so a directory deleted between
		// startup and the call is named at the point it matters, instead of
		// running the agent somewhere the operator did not choose.
		if st, err := os.Stat(a.workDir); err != nil || !st.IsDir() {
			return "", fmt.Errorf("cursor_agent: workDir %q cannot be used, so the agent was not run: %w", a.workDir, err)
		}
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
		// That is the operator's actionable text, so it is carried into the
		// error rather than replaced with "exit status 1".
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", refusalError(msg)
		}
		return "", fmt.Errorf("cursor_agent: %w", err)
	}
	return stdout.String(), nil
}

// trustRequired recognises the workspace-trust prompt.
//
// This is checked before any other reading of stderr because it is the one
// refusal whose text is a directory path on its own line. Treating it as a
// generic error reported the path to the client and said nothing about the
// choice that has to be made.
func trustRequired(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "workspace trust")
}

// refusalError classifies the CLI's prose refusal so the gateway can act on it.
//
// A plain error is not retryable, and the router aborts the whole request on
// one. "You're out of usage" is an exhausted account, not a broken request: the
// next account can answer it, so it is reported as a rate limit and the account
// is cooled. The operator's own words stay in the message.
func refusalError(msg string) error {
	// A named refusal outranks a banner. The CLI prints the trust banner even
	// when it goes on to fail for an unrelated reason, and reporting "not
	// trusted" for an exhausted account would send the operator to fix the
	// wrong thing.
	if line, ok := namedRefusal(msg); ok {
		return classifyRefusal(line)
	}
	// Classification reads the whole message. Picking a line first is what
	// let a banner whose only prose line was a path be reported as that path.
	if trustRequired(msg) {
		return fmt.Errorf("cursor_agent: the workspace is not trusted, so the agent refused to run. "+
			"Run `cursor-agent` once in the directory you want it to use and accept the trust prompt, "+
			"or set workDir on this provider. Full refusal: %s", msg)
	}
	return classifyRefusal(firstMeaningfulLine(msg))
}

// namedRefusal finds an explicitly named failure line, if the CLI printed one.
// It returns false when the only remaining line is a bare path or a stray
// fragment, because guessing is what turns a banner into a reported error.
func namedRefusal(msg string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(msg))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || isBanner(line) {
			continue
		}
		if strings.Contains(line, "Error:") || strings.Contains(line, "error:") {
			return line, true
		}
	}
	return "", false
}

func classifyRefusal(msg string) error {
	if isQuotaRefusal(msg) {
		return adapter.HTTPError{
			Status: http.StatusTooManyRequests,
			Body:   "cursor_agent: " + msg,
		}
	}
	// Only an unrecognised refusal gets trimmed. Classification above runs on
	// the whole text, because trimming first is what turned the trust banner
	// into a bare directory path.
	return fmt.Errorf("cursor_agent: %s", firstMeaningfulLine(msg))
}

func isQuotaRefusal(msg string) bool {
	l := strings.ToLower(msg)
	return strings.Contains(l, "out of usage") ||
		strings.Contains(l, "usage limit") ||
		strings.Contains(l, "rate limit") ||
		strings.Contains(l, "increase limits")
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
