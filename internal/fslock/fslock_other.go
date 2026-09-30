//go:build !(darwin || linux || freebsd || openbsd || netbsd || dragonfly) && !windows

package fslock

import "os"

// No OS file lock here (e.g. plan9, solaris, js/wasm): Lock serialises
// goroutines in this process only, not other processes.
const osLocking = false

func osTryLock(*os.File) (bool, error) { return true, nil }

func osUnlock(*os.File) error { return nil }
