package adapter

import (
	"errors"
	"io"
)

// ErrBodyTooLarge reports an upstream body longer than the caller's buffer.
var ErrBodyTooLarge = errors.New("upstream response exceeded the buffer limit")

// ReadBodyLimit reads at most limit bytes and fails rather than truncating.
// io.LimitReader stops quietly at the limit, and a truncated stream that is
// then assembled can pass for a finished answer.
func ReadBodyLimit(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, ErrBodyTooLarge
	}
	return b, nil
}
