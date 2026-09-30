package gateway

import (
	"bytes"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistErrLoggedOnceAcrossTempFileNames(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	failures := func() int { return strings.Count(logs.String(), "config save failed") }

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	tempErr := func(name string) error {
		return &fs.PathError{Op: "open", Path: filepath.Join(dir, name), Err: fs.ErrPermission}
	}
	g := &Gateway{}
	g.notePersistErr(path, tempErr("config.yaml.1234567.tmp"))
	g.notePersistErr(path, tempErr("config.yaml.7654321.tmp"))
	if n := failures(); n != 1 {
		t.Fatalf("logged %d failures for one persistent error, want 1:\n%s", n, logs.String())
	}
	g.notePersistErr(path, errors.New("a different failure"))
	if n := failures(); n != 2 {
		t.Fatalf("logged %d failures after the error changed, want 2:\n%s", n, logs.String())
	}
	g.notePersistErr(path, nil)
	g.notePersistErr(path, tempErr("config.yaml.42.tmp"))
	if n := failures(); n != 3 {
		t.Fatalf("logged %d failures after a success cleared the last error, want 3:\n%s", n, logs.String())
	}
}
