package fslock

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const (
	renameBudget     = 2 * time.Second
	renameFirstDelay = 10 * time.Millisecond
	renameMaxDelay   = 200 * time.Millisecond
)

// Rename is os.Rename, retried while newpath is open elsewhere. Go's os.Open
// does not request FILE_SHARE_DELETE, so an unlocked reader (an older
// peaproxy, an AV scanner, the indexer) makes MoveFileEx fail with
// ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION until it closes the file.
// Retries back off from 10 ms, doubling to 200 ms, for about 2 s in total;
// scanners on a freshly written file routinely outlast a shorter budget. The
// last error is returned.
func Rename(oldpath, newpath string) error {
	delay := renameFirstDelay
	var slept time.Duration
	for {
		err := os.Rename(oldpath, newpath)
		if err == nil || !retryableRename(err) || slept >= renameBudget {
			return err
		}
		time.Sleep(delay)
		slept += delay
		delay = min(2*delay, renameMaxDelay)
	}
}

func retryableRename(err error) bool {
	var le *os.LinkError
	if !errors.As(err, &le) {
		return false
	}
	return errors.Is(le.Err, windows.ERROR_ACCESS_DENIED) || errors.Is(le.Err, windows.ERROR_SHARING_VIOLATION)
}
