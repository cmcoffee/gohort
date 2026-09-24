package netgate

// No route is unbounded: a body past the default cap fails the read, and only
// a handler that asks for more before reading gets more.

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readAll is a handler that reports how the body read went: 413 for the cap,
// 200 with the byte count otherwise.
func readAll(raise int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if raise > 0 {
			RaiseBodyLimit(r, raise)
		}
		n, err := io.Copy(io.Discard, r.Body)
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, strings.Repeat("x", int(n%10)))
	}
}

func TestLimitRequestBodyCapsAndRaises(t *testing.T) {
	const limit = 1 << 10
	cases := []struct {
		name    string
		size    int
		raise   int64
		chunked bool
		want    int
	}{
		{"under the cap", limit, 0, false, http.StatusOK},
		{"over the cap, declared length", limit + 1, 0, false, http.StatusRequestEntityTooLarge},
		{"over the cap, chunked", limit * 4, 0, true, http.StatusRequestEntityTooLarge},
		{"over the default but raised", limit * 4, limit * 8, false, http.StatusOK},
		{"raised, still over", limit * 16, limit * 8, true, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		h := LimitRequestBody(readAll(c.raise), limit)
		var body io.Reader = strings.NewReader(strings.Repeat("a", c.size))
		if c.chunked {
			body = io.MultiReader(body) // hides the length from NewRequest
		}
		r := httptest.NewRequest(http.MethodPost, "/", body)
		if c.chunked {
			r.ContentLength = -1
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d", c.name, w.Code, c.want)
		}
	}
}

// Raising after the body has been read from is refused: the cap is already a
// fact about what was consumed.
func TestRaiseBodyLimitOnlyBeforeFirstRead(t *testing.T) {
	var raisedLate bool
	h := LimitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4)
		r.Body.Read(buf)
		raisedLate = RaiseBodyLimit(r, 1<<20)
	}), 16)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("abcdefgh")))
	if raisedLate {
		t.Error("RaiseBodyLimit succeeded after the body was already being read")
	}
	if RaiseBodyLimit(httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")), 1) {
		t.Error("RaiseBodyLimit reported success on a request that never went through LimitRequestBody")
	}
}

// A bodiless request keeps http.NoBody, so streaming GETs are not wrapped.
func TestLimitRequestBodyLeavesBodilessRequestsAlone(t *testing.T) {
	var got io.ReadCloser
	h := LimitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Body
	}), 16)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if got != http.NoBody {
		t.Errorf("a GET's body was wrapped: %T", got)
	}
}

// X-Forwarded-Proto counts only from a trusted proxy.
func TestForwardedHTTPSBelievesOnlyTrustedProxies(t *testing.T) {
	prev := TrustedProxiesFunc
	t.Cleanup(func() { TrustedProxiesFunc = prev })
	TrustedProxiesFunc = func() string { return "10.0.0.5" }

	cases := []struct {
		name, remote, proto string
		want                bool
	}{
		{"local proxy says https", "127.0.0.1:5000", "https", true},
		{"configured proxy says https", "10.0.0.5:5000", "HTTPS", true},
		{"proxy chain, first hop https", "10.0.0.5:5000", "https, http", true},
		{"local proxy says http", "127.0.0.1:5000", "http", false},
		{"no header", "127.0.0.1:5000", "", false},
		{"direct client claims https", "203.0.113.9:4000", "https", false},
		{"untrusted LAN peer claims https", "10.0.0.6:5000", "https", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if got := ForwardedHTTPS(r); got != c.want {
			t.Errorf("%s: ForwardedHTTPS = %v, want %v", c.name, got, c.want)
		}
	}
}
