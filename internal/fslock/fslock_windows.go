package fslock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const osLocking = true

// osTryLock locks byte 0 of the (empty) lock file. Windows allows locking past
// EOF, and the lock belongs to this handle, so closing it also releases it.
func osTryLock(f *os.File) (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION):
		return false, nil
	}
	return false, err
}

func osUnlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}
