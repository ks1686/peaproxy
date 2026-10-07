package cursoragent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ks1686/peaproxy/internal/adapter"
	"github.com/ks1686/peaproxy/internal/router"
)

// The gateway's chatReq fills Model, Raw and Stream -- and never Messages.
// Every earlier test in this package built a request by hand with Messages set,
// which is a shape no caller produces, so they passed while the adapter would
// have sent the literal text "Assistant:" with no question in it.
func TestTheQuestionIsReadFromTheRawBodyTheGatewayActuallySends(t *testing.T) {
	dir, out := captureCLI(t, `echo '{"result":"ok"}'`)
	_ = dir
	a, err := New(adapter.Options{ID: "cursor-sub"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"gpt-5.3-codex","stream":false,"messages":[
		{"role":"system","content":"Be terse."},
		{"role":"user","content":"what is the capital of France?"},
		{"role":"assistant","content":"Paris"},
		{"role":"user","content":"and of Spain?"}]}`)
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gpt-5.3-codex", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	prompt := readFile(t, out)
	for _, want := range []string{"Be terse.", "what is the capital of France?", "Paris", "and of Spain?"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the question never reached the agent; %q is missing from:\n%s", want, prompt)
		}
	}
}

// content can be an array of parts, not only a string. The vision and tool
// shapes use it, and dropping it silently would lose the user's question.
func TestArrayContentPartsAreReadNotDropped(t *testing.T) {
	_, out := captureCLI(t, `echo '{"result":"ok"}'`)
	a, err := New(adapter.Options{ID: "cursor-sub"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":[
		{"type":"text","text":"describe this"},
		{"type":"image_url","image_url":{"url":"http://x/y.png"}}]}]}`)
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	prompt := readFile(t, out)
	if !strings.Contains(prompt, "describe this") {
		t.Fatalf("the text part was dropped from:\n%s", prompt)
	}
	// The image is not silently swallowed either: this upstream declares no
	// vision, and pretending the part was never sent would misrepresent it.
	if !strings.Contains(prompt, "image_url") {
		t.Fatalf("the image part vanished without comment:\n%s", prompt)
	}
}

// Without --model every routed model runs on the CLI's default while the
// response reports whatever was asked for, which makes a routing decision
// invisible and unfalsifiable.
func TestTheRequestedModelIsPassedToTheCLI(t *testing.T) {
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	fakeCLI(t, `for a in "$@"; do echo "$a"; done > `+argvPath+`; echo '{"result":"ok"}'`)
	a, err := New(adapter.Options{ID: "cursor-sub"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"gpt-5.3-codex-high","messages":[{"role":"user","content":"hi"}]}`)
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "gpt-5.3-codex-high", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	argv := readFile(t, argvPath)
	if !strings.Contains(argv, "--model") || !strings.Contains(argv, "gpt-5.3-codex-high") {
		t.Fatalf("the model was not passed to the CLI; argv was:\n%s", argv)
	}
}

// The response-cache path keeps only once.Raw and rebuilds the response from it.
// A ChatResponse without Raw serves an empty cached turn to every later caller.
func TestTheResponseCarriesTheRawBodyTheCacheReplays(t *testing.T) {
	fakeCLI(t, `echo '{"result":"the answer"}'`)
	a, err := New(adapter.Options{ID: "cursor-sub"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Raw) == 0 {
		t.Fatal("the response carries no Raw, so a cache hit replays an empty turn")
	}
	var body struct {
		Object  string `json:"object"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp.Raw, &body); err != nil {
		t.Fatalf("Raw is not a chat completion body: %v\n%s", err, resp.Raw)
	}
	if body.Object != "chat.completion" || len(body.Choices) != 1 {
		t.Fatalf("Raw is not the shape the cache rebuilds from: %s", resp.Raw)
	}
	if body.Choices[0].Message.Content != "the answer" {
		t.Fatalf("Raw lost the answer: %s", resp.Raw)
	}
	if body.Choices[0].FinishReason != "stop" {
		t.Fatalf("Raw has no finish_reason, so a replayed turn looks unfinished: %s", resp.Raw)
	}
}

// The CLI emits newline-delimited stream-json; the gateway's stream guard reads
// SSE. Forwarding Cursor's bytes unchanged hands the client JSON that is not SSE.
func TestStreamedOutputIsSSETheStreamGuardCanRead(t *testing.T) {
	fakeCLI(t, `printf '{"type":"text","text":"Hel"}\n{"type":"text","text":"lo"}\n'`)
	a, err := New(adapter.Options{ID: "cursor-sub"})
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	raw := []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if err := a.ChatStream(context.Background(), adapter.ChatRequest{Model: "m", Stream: true, Raw: raw}, &buf); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasPrefix(strings.TrimSpace(got), "data: ") {
		t.Fatalf("the stream does not start with an SSE frame:\n%s", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n\n") {
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("a frame is not SSE:\n%s", line)
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("a frame is not JSON: %v\n%s", err, payload)
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Fatalf("a frame is not a chat completion chunk: %s", payload)
		}
	}
	if !strings.Contains(got, "[DONE]") {
		t.Fatalf("the stream never terminates:\n%s", got)
	}
	if !strings.Contains(got, `"finish_reason":"stop"`) {
		t.Fatalf("the stream has no finish_reason, which is the failure this whole path exists to prevent:\n%s", got)
	}
	// The text arrives as separate deltas, which is what SSE is for; the
	// client reassembles them. Asserting the contiguous string would fail on
	// correct output.
	var text strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(got), "\n\n") {
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) == nil && len(chunk.Choices) > 0 {
			text.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("reassembled stream is %q, want %q; frames were:\n%s", text.String(), "Hello", got)
	}
}

// An exhausted account is not a broken request. A plain error is non-retryable
// and the router aborts the whole turn on one; this has to fail over.
func TestAnExhaustedAccountIsClassifiedSoTheGatewayCanFailOver(t *testing.T) {
	fakeCLI(t, `echo "ActionRequiredError: You're out of usage." >&2; exit 1`)
	a, err := New(adapter.Options{ID: "cursor-sub"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_, err = a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw})
	if err == nil {
		t.Fatal("a refused run was reported as a successful turn")
	}
	if !router.Retryable(err) {
		t.Fatalf("an exhausted account is not retryable, so the gateway aborts instead of "+
			"failing over: %v", err)
	}
	if class := router.Classify(err); class != router.FailoverRateLimit {
		t.Fatalf("classified as %q, want %q", class, router.FailoverRateLimit)
	}
	if !strings.Contains(err.Error(), "out of usage") {
		t.Fatalf("the operator's own words were lost: %v", err)
	}
	var he adapter.HTTPError
	if !asHTTPError(err, &he) || he.Status != http.StatusTooManyRequests {
		t.Fatalf("refusal is not carried as a 429: %v", err)
	}
	_ = time.Second
}

func asHTTPError(err error, out *adapter.HTTPError) bool {
	he, ok := err.(adapter.HTTPError)
	if ok {
		*out = he
	}
	return ok
}

// captureCLI installs a fake that records the prompt it was given, and returns
// the directory plus the path of that recording.
func captureCLI(t *testing.T, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "prompt.txt")
	body := "last=\"\"; for a in \"$@\"; do last=\"$a\"; done; printf '%s' \"$last\" > " + out + "; " + script
	fakeCLI(t, body)
	return dir, out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the fake CLI recorded nothing at %s: %v", path, err)
	}
	return string(b)
}

// The trust prompt's text is a directory path on its own line, so any
// line-picking rule reports the path to the client and says nothing about the
// choice that has to be made.
func TestAnUntrustedWorkspaceIsNamedAsSuchRatherThanReportedAsAPath(t *testing.T) {
	// The real CLI writes this banner to stderr, which is where a refusal is
	// read from; a fake writing it to stdout would test nothing.
	fakeCLI(t, `{ echo "Workspace Trust Required"; echo; echo "  Do you trust the contents of this directory?"; echo; echo "    /Users/someone/secret/project"; } >&2; exit 1`)
	a, err := New(adapter.Options{ID: "cursor-sub"})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_, err = a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw})
	if err == nil {
		t.Fatal("a blocked run was reported as a turn")
	}
	msg := err.Error()
	if strings.Contains(msg, "cursor_agent: /Users") {
		t.Fatalf("the client was handed a bare path instead of an explanation: %v", err)
	}
	if !strings.Contains(msg, "not trusted") {
		t.Fatalf("the refusal does not say what is wrong: %v", err)
	}
	if !strings.Contains(msg, "workDir") {
		t.Fatalf("the refusal names no way out: %v", err)
	}
}

// workDir must actually reach the subprocess, or the trust prompt lands on
// whatever directory the service happened to start in.
func TestTheConfiguredWorkDirReachesTheSubprocess(t *testing.T) {
	dir := t.TempDir()
	seen := filepath.Join(dir, "cwd.txt")
	fakeCLI(t, `pwd > `+seen+`; echo '{"result":"ok"}'`)
	work := t.TempDir()
	a, err := New(adapter.Options{ID: "cursor-sub", WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	if _, err := a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(readFile(t, seen))
	// macOS reports /var for /private/var, so compare the resolved paths.
	resolved, _ := filepath.EvalSymlinks(work)
	if got != work && got != resolved {
		t.Fatalf("the agent ran in %q, not the configured %q", got, work)
	}
}

// A workDir that does not exist is a config error, and it must say so at the
// call rather than falling back to an unpredictable directory.
func TestAWorkDirThatDoesNotExistIsRefusedWithThePath(t *testing.T) {
	fakeCLI(t, `echo '{"result":"ok"}'`)
	missing := filepath.Join(t.TempDir(), "not-here")
	a, err := New(adapter.Options{ID: "cursor-sub", WorkDir: missing})
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	_, err = a.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw})
	if err == nil {
		t.Fatal("a missing workDir was silently ignored")
	}
	if !strings.Contains(err.Error(), "not-here") {
		t.Fatalf("the refusal does not name the path: %v", err)
	}
}

// The CLI blocks on an interactive trust prompt that a service cannot answer.
// Passing --trust unconditionally would grant an agent access to whatever
// directory PeaProxy happened to start in, so it goes only with a named one.
func TestTrustIsPassedOnlyForADirectoryTheOperatorNamed(t *testing.T) {
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	fakeCLI(t, `for a in "$@"; do echo "$a"; done > `+argvPath+`; echo '{"result":"ok"}'`)
	raw := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	unpinned, err := New(adapter.Options{ID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unpinned.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	if argv := readFile(t, argvPath); strings.Contains(argv, "--trust") || strings.Contains(argv, "--yolo") {
		t.Fatalf("an inherited directory was auto-trusted; argv was:\n%s", argv)
	}

	work := t.TempDir()
	pinned, err := New(adapter.Options{ID: "c", WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pinned.Chat(context.Background(), adapter.ChatRequest{Model: "m", Raw: raw}); err != nil {
		t.Fatal(err)
	}
	if argv := readFile(t, argvPath); !strings.Contains(argv, "--trust") {
		t.Fatalf("a named workDir still blocks on the trust prompt; argv was:\n%s", argv)
	}
}
