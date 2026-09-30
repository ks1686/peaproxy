package config

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestLockConfigOnlyReportsBusyForContention(t *testing.T) {
	dir := t.TempDir()
	readOnlyDir(t, dir)
	_, _, _, err := EnsureFile(filepath.Join(dir, "config.yaml"))
	if err == nil {
		t.Fatal("EnsureFile created a config in a read-only dir")
	}
	if strings.Contains(err.Error(), "busy") {
		t.Fatalf("permission failure reported as busy: %v", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("err = %v, want a permission error", err)
	}
}
