package filestore

// An upload has a ceiling, the route can go past the server-wide default to
// reach it, and going past the ceiling is a 413 that says what to do.

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/snugforge/kvlite"
)

// uploadFixture is a store that takes non-admin uploads and a signed-in user.
func uploadFixture(t *testing.T) (*FileStoreApp, string, *http.Cookie) {
	t.Helper()
	prevAuth := AuthDB
	authDB := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(authDB, "user-a", "pw-a-123", false)
	AuthDB = func() Database { return authDB }
	t.Cleanup(func() { AuthDB = prevAuth })
	token := AuthCreateSession(authDB, "user-a")

	root := t.TempDir()
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	st, err := SaveStore(app.DB, Store{Name: "Drop", Path: root, AllowUploads: true})
	if err != nil {
		t.Fatal(err)
	}
	return app, st.Slug, &http.Cookie{Name: "gohort_session", Value: token}
}

// multipartBody is one file part of size n. The reader hides the length, so
// the request goes out without a Content-Length, as a streamed one would.
func multipartBody(t *testing.T, n int) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "capture.log")
	if err != nil {
		t.Fatal(err)
	}
	fw.Write([]byte(strings.Repeat("x", n)))
	mw.Close()
	return io.MultiReader(&buf), mw.FormDataContentType()
}

func TestUploadHasACeilingAndSaysSo(t *testing.T) {
	app, slug, cookie := uploadFixture(t)
	prev := maxUploadBytes
	t.Cleanup(func() { maxUploadBytes = prev })
	maxUploadBytes = 8 << 10

	// The server-wide cap is set well BELOW this route's ceiling, so a
	// within-ceiling upload only succeeds if the route raised it.
	h := netgate.LimitRequestBody(http.HandlerFunc(app.handleUpload), 1<<10)
	send := func(n int, declared int64) *httptest.ResponseRecorder {
		body, ct := multipartBody(t, n)
		r := httptest.NewRequest(http.MethodPost, "/filestore/api/upload?slug="+slug+"&within=scan-1", body)
		r.Header.Set("Content-Type", ct)
		r.ContentLength = declared
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	if w := send(4<<10, -1); w.Code != http.StatusOK {
		t.Fatalf("a 4 KiB upload under an 8 KiB ceiling: %d %s", w.Code, w.Body.String())
	}

	for _, c := range []struct {
		name     string
		declared int64
	}{
		{"streamed past the ceiling", -1},
		{"declared past the ceiling", 64 << 10},
	} {
		w := send(32<<10, c.declared)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: status %d, want 413 (%s)", c.name, w.Code, w.Body.String())
			continue
		}
		if msg := w.Body.String(); !strings.Contains(msg, "larger than") || !strings.Contains(msg, "smaller uploads") {
			t.Errorf("%s: the refusal should say what the limit is and what to do: %q", c.name, msg)
		}
	}
	// The refused streamed file must not be left behind half-written.
	entries, _ := os.ReadDir(filepath.Join(storeRoot(t, app, slug), "scan-1"))
	for _, e := range entries {
		if info, _ := e.Info(); info != nil && info.Size() > maxUploadBytes {
			t.Errorf("%s was kept at %d bytes, past the ceiling", e.Name(), info.Size())
		}
	}
}

func storeRoot(t *testing.T, app *FileStoreApp, slug string) string {
	t.Helper()
	st, ok := LoadStore(app.DB, slug)
	if !ok {
		t.Fatal("fixture store vanished")
	}
	return st.Path
}
