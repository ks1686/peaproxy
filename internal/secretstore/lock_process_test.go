package secretstore

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Helper children re-run this test binary with one of these set; each test
// checks its own variable first and never returns in the child.
const (
	helperEnv      = "PEAPROXY_SECRETSTORE_HELPER"
	helperDirEnv   = "PEAPROXY_SECRETSTORE_DIR"
	helperPauseEnv = "PEAPROXY_HELPER_PAUSE"
)

func requireOSLock(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin", "linux", "freebsd", "openbsd", "netbsd", "dragonfly", "windows":
	default:
		t.Skip("no cross-process file lock on " + runtime.GOOS)
	}
}

// childLockTimeout applies PEAPROXY_TEST_LOCK_TIMEOUT in a helper child. A
// child never waits less than 5 s: shorter waits turn slow CI into ErrBusy.
func childLockTimeout() {
	d, err := time.ParseDuration(os.Getenv("PEAPROXY_TEST_LOCK_TIMEOUT"))
	if err != nil {
		return
	}
	lockTimeout = max(d, 5*time.Second)
}

func helperExit(err error) {
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// slack scales a generous-side deadline: GitHub's windows runners start
// processes and scan fresh files far more slowly than unix ones.
func slack(d time.Duration) time.Duration {
	if runtime.GOOS == "windows" {
		return 3 * d
	}
	return d
}

type child struct {
	cmd    *exec.Cmd
	out    bytes.Buffer
	exited chan struct{} // closed once cmd.Wait returns; err is set by then
	err    error
}

// startChild runs a helper child. Cleanup kills it and waits for it to exit,
// so it holds no handle in the TempDir when that is removed; Windows refuses
// to delete open files.
func startChild(t *testing.T, test, dir, arg string, extraEnv ...string) *child {
	t.Helper()
	c := &child{exited: make(chan struct{})}
	c.cmd = exec.Command(os.Args[0], "-test.run=^"+test+"$", "-test.count=1")
	c.cmd.Env = append(os.Environ(), append([]string{helperEnv + "=" + arg, helperDirEnv + "=" + dir}, extraEnv...)...)
	c.cmd.Stdout = &c.out
	c.cmd.Stderr = &c.out
	if err := c.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		c.err = c.cmd.Wait()
		close(c.exited)
	}()
	t.Cleanup(func() {
		_ = c.cmd.Process.Kill()
		<-c.exited
	})
	return c
}

// wait returns the child's exit error, killing it at the deadline.
func (c *child) wait(t *testing.T, deadline time.Time) error {
	t.Helper()
	select {
	case <-c.exited:
		return c.err
	case <-time.After(time.Until(deadline)):
		_ = c.cmd.Process.Kill()
		<-c.exited
		return fmt.Errorf("killed at deadline; output %q", c.out.String())
	}
}

func waitAll(t *testing.T, children []*child, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for i, c := range children {
		if err := c.wait(t, deadline); err != nil {
			t.Errorf("child %d: %v; output %q", i, err, c.out.String())
		}
	}
}

func TestIndexNotLostAcrossProcesses(t *testing.T) {
	if key := os.Getenv(helperEnv); key != "" {
		childLockTimeout()
		dir := os.Getenv(helperDirEnv)
		kr := &dirKeyring{root: filepath.Join(dir, "kr")}
		if os.Getenv(helperPauseEnv) == "1" {
			kr.pauseDir = dir
		}
		s := &Store{backend: BackendKeyring, dir: dir, kr: kr}
		helperExit(s.Set(key, KindOAuth, strings.Repeat(key, 5000)))
	}
	requireOSLock(t)

	// Given: child A is frozen after saving the index, before its chunks land.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "kr"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := startChild(t, "TestIndexNotLostAcrossProcesses", dir, "a", helperPauseEnv+"=1")
	if err := waitForFile(filepath.Join(dir, "paused"), slack(30*time.Second)); err != nil {
		t.Fatalf("child a never paused: %v; %v", err, a.wait(t, time.Now()))
	}

	// When: child B writes meanwhile, then A resumes.
	b := startChild(t, "TestIndexNotLostAcrossProcesses", dir, "b")
	select {
	case <-b.exited:
		t.Errorf("child b finished while child a held the store (err=%v)", b.err)
	case <-time.After(1500 * time.Millisecond):
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitAll(t, []*child{a, b}, slack(60*time.Second))

	// Then
	s := &Store{backend: BackendKeyring, dir: dir, kr: &dirKeyring{root: filepath.Join(dir, "kr")}}
	idx, err := s.loadIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a/oauth", "b/oauth"} {
		if _, ok := idx.keys[k]; !ok {
			t.Errorf("index lost %s: keys=%v", k, idx.keys)
		}
	}
	if len(idx.pending) != 0 {
		t.Errorf("pending entries left: %v", idx.pending)
	}
	for _, id := range []string{"a", "b"} {
		if got, err := s.Get(id, KindOAuth); err != nil || got != strings.Repeat(id, 5000) {
			t.Errorf("%s: len=%d err=%v", id, len(got), err)
		}
	}
}

func TestFileBlobNotLostAcrossProcesses(t *testing.T) {
	const children, writes = 4, 10
	if n := os.Getenv(helperEnv); n != "" {
		childLockTimeout()
		dir := os.Getenv(helperDirEnv)
		if err := waitForFile(filepath.Join(dir, "start"), 30*time.Second); err != nil {
			helperExit(err)
		}
		for i := 0; i < writes; i++ {
			s, err := OpenFile(dir)
			if err == nil {
				err = s.Set(fmt.Sprintf("child%s-%d", n, i), KindAPIKey, "v")
			}
			if err != nil {
				helperExit(err)
			}
		}
		helperExit(nil)
	}
	requireOSLock(t)

	// Given: the key exists, so only the blob read-modify-write races.
	dir := t.TempDir()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, KeyFileName), key, 0o600); err != nil {
		t.Fatal(err)
	}

	// When
	var kids []*child
	for n := 0; n < children; n++ {
		kids = append(kids, startChild(t, "TestFileBlobNotLostAcrossProcesses", dir, strconv.Itoa(n)))
	}
	if err := os.WriteFile(filepath.Join(dir, "start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitAll(t, kids, slack(60*time.Second))

	// Then
	s := &Store{backend: BackendFile, dir: dir}
	// shared: this parent takes no lock, exactly like Get's unlocked fallback.
	blob, err := s.loadBlob(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob.Items) != children*writes {
		t.Fatalf("secrets.enc holds %d items, want %d", len(blob.Items), children*writes)
	}
}

// Rounds give the children many fresh dirs to race on, so the unlocked
// create-then-write shows up reliably.
func TestKeyFileCreatedOnceAcrossProcesses(t *testing.T) {
	const children, rounds = 4, 25
	if os.Getenv(helperEnv) != "" {
		childLockTimeout()
		dir := os.Getenv(helperDirEnv)
		if err := waitForFile(filepath.Join(dir, "start"), 30*time.Second); err != nil {
			helperExit(err)
		}
		for r := 0; r < rounds; r++ {
			s := &Store{backend: BackendFile, dir: filepath.Join(dir, strconv.Itoa(r))}
			// Deliberately without the lock: these children race to create the
			// same key file, which is the one caller loadOrCreateKey cannot make
			// safe on its own (#64).
			k, err := s.loadOrCreateKeyShared()
			if err != nil {
				helperExit(err)
			}
			_, _ = fmt.Fprintf(os.Stdout, "key %d %s\n", r, hex.EncodeToString(k))
		}
		helperExit(nil)
	}
	requireOSLock(t)

	dir := t.TempDir()
	for r := 0; r < rounds; r++ {
		if err := os.Mkdir(filepath.Join(dir, strconv.Itoa(r)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var kids []*child
	for n := 0; n < children; n++ {
		kids = append(kids, startChild(t, "TestKeyFileCreatedOnceAcrossProcesses", dir, strconv.Itoa(n)))
	}
	if err := os.WriteFile(filepath.Join(dir, "start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitAll(t, kids, slack(60*time.Second))
	if t.Failed() {
		return
	}

	for r := 0; r < rounds; r++ {
		onDisk, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(r), KeyFileName))
		if err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("key %d %s", r, hex.EncodeToString(onDisk))
		for n, c := range kids {
			if !strings.Contains(c.out.String(), want+"\n") {
				t.Errorf("round %d: child %d used a key other than the one on disk", r, n)
			}
		}
	}
}
