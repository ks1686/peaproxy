// Package fslock is a cross-process advisory lock on a file.
//
// Lock serialises callers in two stages under one deadline: an in-process
// semaphore per absolute path (so goroutines never share an OS lock), then the
// OS lock (flock on unix, LockFileEx on Windows) polled until the deadline.
// Platforms without an OS lock get the in-process stage only.
//
// Locks are not reentrant: a second Lock on the same path from the same
// process waits like any other caller. The lock file is never removed.
package fslock

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBusy is returned when the lock is still held at the deadline. Callers
// wrap it with context.
var ErrBusy = errors.New("fslock: busy")

const pollInterval = 25 * time.Millisecond

// sems maps an absolute lock path to a capacity-1 channel. A sync.Mutex cannot
// be waited on with a deadline, hence the channel.
var sems sync.Map

// tryLockCalls counts OS lock attempts; tests use it to tell the stages apart.
var tryLockCalls atomic.Int64

func tryLock(f *os.File) (bool, error) {
	tryLockCalls.Add(1)
	return osTryLock(f)
}

// Lock takes the exclusive lock on path, creating the file (mode 0600) if
// needed; its directory must exist. It returns ErrBusy if the lock is not
// acquired within timeout. The returned unlock is safe to call more than once.
func Lock(path string, timeout time.Duration) (unlock func(), err error) {
	deadline := time.Now().Add(timeout)
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	v, _ := sems.LoadOrStore(abs, make(chan struct{}, 1))
	sem := v.(chan struct{})

	select {
	case sem <- struct{}{}:
	default:
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil, ErrBusy
		}
		timer := time.NewTimer(wait)
		select {
		case sem <- struct{}{}:
			timer.Stop()
		case <-timer.C:
			return nil, ErrBusy
		}
	}

	f, err := os.OpenFile(abs, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		<-sem
		return nil, err
	}
	for {
		ok, err := tryLock(f)
		if err != nil {
			f.Close()
			<-sem
			return nil, err
		}
		if ok {
			break
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			f.Close()
			<-sem
			return nil, ErrBusy
		}
		time.Sleep(min(pollInterval, wait))
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			_ = osUnlock(f)
			f.Close()
			<-sem
		})
	}, nil
}
