package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows checks out with CRLF. A multi-line mutation anchor written with \n
// matches nothing there, and the gate reports "0 occurrences, want exactly 1" --
// a message that reads as the guarded code having moved, and is not.
//
// It stayed invisible because every mutation that passed on Windows so far had a
// single-line anchor. This is here so the fix cannot be reverted on a machine
// that checks out LF and quietly re-break Windows.

func TestReadSourceNormalisesWindowsLineEndings(t *testing.T) {
	root := t.TempDir()
	rel := "thing.go"
	crlf := "package p\r\n\r\nfunc a() {\r\n\treturn 1\r\n}\r\n"
	if err := os.WriteFile(filepath.Join(root, rel), []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readSource(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "\r") {
		t.Fatalf("carriage returns survived:\n%q", got)
	}
	// The point: an anchor written the way the gate writes them now matches.
	anchor := "func a() {\n\treturn 1\n}"
	if strings.Count(string(got), anchor) != 1 {
		t.Fatalf("a multi-line anchor did not match CRLF input:\n%q", got)
	}
}

func TestReadSourceLeavesLFAlone(t *testing.T) {
	root := t.TempDir()
	rel := "thing.go"
	lf := "package p\n\nfunc a() {\n\treturn 1\n}\n"
	if err := os.WriteFile(filepath.Join(root, rel), []byte(lf), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readSource(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != lf {
		t.Fatalf("LF content was altered:\ngot  %q\nwant %q", got, lf)
	}
}

func TestReadSourceReportsAMissingFile(t *testing.T) {
	if _, err := readSource(t.TempDir(), "nope.go"); err == nil {
		t.Fatal("a missing file read as success")
	}
}
