package adapter

import (
	"errors"
	"strings"
	"testing"
)

func TestReadBodyLimitRefusesToTruncateSilently(t *testing.T) {
	b, err := ReadBodyLimit(strings.NewReader("12345"), 5)
	if err != nil || string(b) != "12345" {
		t.Fatalf("at the limit: %q, %v", b, err)
	}
	if _, err := ReadBodyLimit(strings.NewReader("123456"), 5); !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("over the limit: err = %v, want ErrBodyTooLarge", err)
	}
}
