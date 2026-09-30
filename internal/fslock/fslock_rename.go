//go:build !windows

package fslock

import "os"

// Rename is os.Rename; on Windows it retries while another handle has newpath
// open.
func Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
