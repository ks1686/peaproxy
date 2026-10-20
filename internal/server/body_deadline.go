package server

import (
	"io"
	"net/http"
	"time"
)

// bodyReadTimeout bounds how long a request may take to deliver its body.
// Only ReadHeaderTimeout was set, so a peer that sent headers and then stalled
// held a connection and a handler goroutine forever. A server-wide ReadTimeout
// would also apply while long SSE responses stream, so the deadline is set per
// request and cleared once the body has been read.
var bodyReadTimeout = 2 * time.Minute

func withBodyDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
			rc := http.NewResponseController(w)
			if rc.SetReadDeadline(time.Now().Add(bodyReadTimeout)) == nil {
				r.Body = &deadlineBody{ReadCloser: r.Body, rc: rc}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// deadlineBody clears the read deadline once the whole body has arrived, so it
// cannot fire while a response is still streaming. A failed read (the deadline
// itself, a reset) keeps it: net/http discards the unread rest of the body after
// the handler, and that discard must time out too.
type deadlineBody struct {
	io.ReadCloser
	rc      *http.ResponseController
	cleared bool
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF && !b.cleared {
		b.cleared = true
		_ = b.rc.SetReadDeadline(time.Time{})
	}
	return n, err
}
