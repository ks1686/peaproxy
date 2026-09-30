package fslock

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	renameAttempts = 10
	renameBackoff  = 20 * time.Millisecond
)

// Rename is os.Rename, retried while newpath is open elsewhere. Go's os.Open
// does not request FILE_SHARE_DELETE, so an unlocked reader (an older
// peaproxy, an AV scanner, the indexer) makes MoveFileEx fail with
// ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION until it closes the file.
func Rename(oldpath, newpath string) error {
	var err error
	for i := 0; i < renameAttempts; i++ {
		if i > 0 {
			time.Sleep(renameBackoff)
		}
		if err = os.Rename(oldpath, newpath); err == nil || !retryableRename(err) {
			return err
		}
	}
	return err
}

func retryableRename(err error) bool {
	var le *os.LinkError
	if !errors.As(err, &le) {
		return false
	}
	return errors.Is(le.Err, windows.ERROR_ACCESS_DENIED) || errors.Is(le.Err, windows.ERROR_SHARING_VIOLATION)
}
