package netgate

import (
	"io"
	"net/http"
)

// DefaultBodyLimit caps a request body unless the handler asks for more.
//
// 64 MiB, not smaller, because that is already the largest JSON body the tree
// accepts on purpose: a chat send carries its attachments base64-encoded
// (about 1.37x the file), and the import doors, the account import and the
// peer model proxy all cap at this figure. A lower default would turn those
// into refusals nobody asked for; the point here is that NO route is
// unbounded, not that every route is small.
const DefaultBodyLimit int64 = 64 << 20

// limitedBody is the request body under a cap the handler can still raise.
//
// Raising is only possible before the first read. After that the cap is a
// fact about what has already been consumed, and swapping the reader would
// lose or double-count bytes.
type limitedBody struct {
	w       http.ResponseWriter
	orig    io.ReadCloser
	rc      io.ReadCloser
	max     int64
	length  int64 // Content-Length, -1 when unknown
	started bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if !b.started {
		b.started = true
		// A declared length already over the cap is refused before a byte is
		// read, so a multi-gigabyte upload to the wrong route fails at once
		// instead of after the first 64 MiB crosses the wire.
		if b.length > b.max {
			return 0, &http.MaxBytesError{Limit: b.max}
		}
	}
	return b.rc.Read(p)
}

func (b *limitedBody) Close() error { return b.orig.Close() }

func (b *limitedBody) raise(max int64) bool {
	if b.started {
		return false
	}
	b.max = max
	b.rc = http.MaxBytesReader(b.w, b.orig, max)
	return true
}

// LimitRequestBody wraps every request body in a cap of max bytes. A read past
// it fails with *http.MaxBytesError, which a handler can test for with
// errors.As to answer 413.
//
// Bodiless requests (GET, WebSocket upgrades, SSE) are left alone: there is
// nothing to cap, and not wrapping them keeps the streaming paths untouched.
func LimitRequestBody(next http.Handler, max int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = &limitedBody{
				w:      w,
				orig:   r.Body,
				rc:     http.MaxBytesReader(w, r.Body, max),
				max:    max,
				length: r.ContentLength,
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RaiseBodyLimit lets a handler that legitimately takes more than the default
// (a streamed file upload) set its own cap. Call it before reading the body.
// Reports false when there is no cap to raise (the request did not come
// through LimitRequestBody, or something replaced the body) or the body has
// already been read from.
func RaiseBodyLimit(r *http.Request, max int64) bool {
	if r == nil {
		return false
	}
	b, ok := r.Body.(*limitedBody)
	if !ok {
		return false
	}
	return b.raise(max)
}
