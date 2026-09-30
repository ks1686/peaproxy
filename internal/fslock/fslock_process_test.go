package fslock

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	helperPathEnv    = "FSLOCK_HELPER"
	helperTimeoutEnv = "FSLOCK_HELPER_TIMEOUT"
)

// runHelper is the child side: take the lock at FSLOCK_HELPER with the given
// timeout, print the outcome, exit 0 on success and 3 on ErrBusy.
func runHelper(path string) {
	timeout, err := time.ParseDuration(os.Getenv(helperTimeoutEnv))
	if err != nil {
		fmt.Println("bad timeout:", err)
		os.Exit(2)
	}
	unlock, err := Lock(path, timeout)
	switch {
	case errors.Is(err, ErrBusy):
		fmt.Println("busy")
		os.Exit(3)
	case err != nil:
		fmt.Println("error:", err)
		os.Exit(2)
	}
	unlock()
	fmt.Println("locked")
	os.Exit(0)
}

func helperCmd(t *testing.T, name, path string, timeout time.Duration) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$", "-test.count=1")
	cmd.Env = append(os.Environ(), helperPathEnv+"="+path, helperTimeoutEnv+"="+timeout.String())
	return cmd
}

func runHelperCmd(t *testing.T, cmd *exec.Cmd) (string, int) {
	t.Helper()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run helper: %v", err)
		}
		code = ee.ExitCode()
	}
	return strings.TrimSpace(out.String()), code
}

func TestLockExcludesOtherProcess(t *testing.T) {
	if path := os.Getenv(helperPathEnv); path != "" {
		runHelper(path)
	}
	if !osLocking {
		t.Skip("no OS file lock on this platform; only the in-process stage applies")
	}

	path := lockPath(t)
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	out, code := runHelperCmd(t, helperCmd(t, "TestLockExcludesOtherProcess", path, 300*time.Millisecond))
	if code == 0 || !strings.Contains(out, "busy") {
		unlock()
		t.Fatalf("child while parent holds lock: code=%d out=%q, want non-zero and busy", code, out)
	}

	unlock()
	out, code = runHelperCmd(t, helperCmd(t, "TestLockExcludesOtherProcess", path, 300*time.Millisecond))
	if code != 0 || !strings.Contains(out, "locked") {
		t.Fatalf("child after parent unlocked: code=%d out=%q, want 0 and locked", code, out)
	}
}

func TestLockWaitsForOtherProcess(t *testing.T) {
	if path := os.Getenv(helperPathEnv); path != "" {
		runHelper(path)
	}
	if !osLocking {
		t.Skip("no OS file lock on this platform; only the in-process stage applies")
	}

	path := lockPath(t)
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	cmd := helperCmd(t, "TestLockWaitsForOtherProcess", path, 10*time.Second)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		unlock()
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	unlock()

	if err := cmd.Wait(); err != nil {
		t.Fatalf("child did not acquire after parent unlocked: %v, out=%q", err, out.String())
	}
	if !strings.Contains(out.String(), "locked") {
		t.Fatalf("child out = %q, want locked", out.String())
	}
}
