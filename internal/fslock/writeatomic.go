package fslock

import (
	"os"
	"path/filepath"
)

// WriteAtomic replaces path with b, so a reader sees either the old contents or
// the new ones and never a half-written file.
//
// The bytes go to a fresh temp file in the same directory (a rename across a
// filesystem boundary is not atomic, and would not even be permitted), are
// flushed before the rename, and the temp file is removed if anything fails.
// The temp name is unique per call rather than a fixed path+".tmp", because two
// writers of the same file -- the UI and the CLI both connecting the same
// harness, say -- would otherwise scribble on each other's partial output.
//
// It takes no lock: the files this is used for are either the caller's own
// (harness config belongs to the harness) or written only by the server
// (usage, compat data), which already serialises them.
func WriteAtomic(path string, b []byte, perm os.FileMode) error {
	return writeAtomicWith(path, b, perm, nil)
}

// writeAtomicWith is WriteAtomic with a hook that sees the open temp file
// before it is written, for tests that need to inject a failure or look at the
// scratch name. hook is called after the temp file is created; returning an
// error abandons the write the same way a failed write would.
func writeAtomicWith(path string, b []byte, perm os.FileMode, hook func(*os.File) error) (err error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	if hook != nil {
		if err = hook(f); err != nil {
			_ = f.Close()
			return err
		}
	}
	// CreateTemp makes the file 0600; Chmod is what applies a caller's wider
	// mode, and it has to happen before the rename so the target never exists
	// with the wrong permissions.
	if err = f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return Rename(tmp, path)
}
