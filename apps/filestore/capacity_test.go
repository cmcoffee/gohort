package filestore

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/pathhold"
	"github.com/cmcoffee/snugforge/kvlite"
)

// pretendUsage makes the store read as holding n bytes, without writing them.
func pretendUsage(t *testing.T, st Store, n int64) {
	t.Helper()
	usageCache.Lock()
	usageCache.at[st.Slug] = usageEntry{bytes: n, when: time.Now()}
	usageCache.Unlock()
	t.Cleanup(st.wrote)
}

// A store at its cap refuses new work, saying how full it is and that nothing
// was deleted; under its cap, and with no cap, the same work goes ahead.
func TestAFullStoreRefusesAndSaysHowFull(t *testing.T) {
	st := Store{Name: "Intake", Slug: "intake_cap", Path: t.TempDir(), MaxGB: 1}
	pretendUsage(t, st, 900<<20)
	if err := st.roomFor(50 << 20); err != nil {
		t.Errorf("50 MB into a store with 124 MB to spare was refused: %v", err)
	}
	err := st.roomFor(200 << 20)
	if err == nil || !strings.Contains(err.Error(), "cap, and this needs") || !strings.Contains(err.Error(), "Nothing was deleted") {
		t.Errorf("past the cap: %v", err)
	}
	pretendUsage(t, st, 2<<30)
	if err := st.roomFor(0); err == nil || !strings.Contains(err.Error(), "is full") {
		t.Errorf("an over-cap store took work of unknown size: %v", err)
	}
	st.Ephemeral = true
	if err := st.roomFor(0); err == nil || !strings.Contains(err.Error(), "wait for old cases to expire") {
		t.Errorf("a temporary store's refusal should say its cases expire: %v", err)
	}
	if left := st.spaceLeft(); left != 0 {
		t.Errorf("space left in an over-cap store = %d", left)
	}
	if err := (Store{Name: "Open", Slug: "open_cap", Path: t.TempDir()}).roomFor(50 << 20); err != nil {
		t.Errorf("a store with no cap refused: %v", err)
	}
}

// Below the disk's free-space floor, nothing is written, cap or not.
func TestTheDiskFloorRefusesAWrite(t *testing.T) {
	tdb := &DBase{Store: kvlite.MemStore()}
	tdb.Set(WebTable, "tune_disk_floor_gb", float64(1000000)) // more than any test disk
	SetTunablesDB(tdb)
	t.Cleanup(func() { SetTunablesDB(nil) })
	err := (Store{Name: "Intake", Slug: "intake_floor", Path: t.TempDir()}).roomFor(0)
	if err == nil || !strings.Contains(err.Error(), "keeps free for the server") {
		t.Errorf("a write below the floor was allowed: %v", err)
	}
}

// An upload into a full store is refused before a byte lands, with the reason.
func TestAnUploadIntoAFullStoreIsRefused(t *testing.T) {
	app, slug, cookie := uploadFixture(t)
	st, _ := LoadStore(app.DB, slug)
	st.MaxGB = 1
	if _, err := SaveStore(app.DB, st); err != nil {
		t.Fatal(err)
	}
	pretendUsage(t, st, 2<<30)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "capture.log")
	fw.Write([]byte("data"))
	mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/filestore/api/upload?slug="+slug+"&within=CASE-1", &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	app.handleUpload(w, r)
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), "Nothing was deleted") {
		t.Errorf("upload into a full store: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(st.Path, "CASE-1", "capture.log")); err == nil {
		t.Error("the refused upload was written anyway")
	}
}

// A temporary store's folders go once they outlive its lifetime, on the
// automatic sweep, unless something is using them. Other stores are never on
// that sweep, whatever their retention says.
func TestATemporaryStoreSweepsItselfButNotWhatIsInUse(t *testing.T) {
	app := &FileStoreApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tmpRoot, keepRoot := t.TempDir(), t.TempDir()
	if _, err := SaveStore(app.DB, Store{Name: "Intake", Path: tmpRoot, Ephemeral: true, LifetimeHours: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveStore(app.DB, Store{Name: "Analysis", Path: keepRoot, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	old := aged(t, tmpRoot, "CASE-1", 1)      // a day old: past a 1h lifetime
	busy := aged(t, tmpRoot, "CASE-2", 1)     // as old, but in use
	fresh := filepath.Join(tmpRoot, "CASE-3") // just written
	kept := aged(t, keepRoot, "CASE-1", 5)    // past retention, but not temporary
	os.MkdirAll(fresh, 0o755)
	release := pathhold.Hold(filepath.Join(busy, "prepared"))

	if st, _ := LoadStore(app.DB, "intake"); st.LifetimeHours != 1 {
		t.Fatalf("lifetime not kept: %+v", st)
	}
	n, _ := reapExpired(t.Context(), app.DB, true)
	if n != 1 {
		t.Errorf("removed %d folders, want 1", n)
	}
	for path, want := range map[string]bool{old: false, busy: true, fresh: true, kept: true} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", path, err == nil, want)
		}
	}
	release()
	if n, _ := reapExpired(t.Context(), app.DB, true); n != 1 {
		t.Errorf("once released, the old folder should go: removed %d", n)
	}

	// Made temporary without a lifetime, it keeps folders a day.
	st, err := SaveStore(app.DB, Store{Name: "Scratch", Path: t.TempDir(), Ephemeral: true})
	if err != nil || st.LifetimeHours != defaultLifetimeHours {
		t.Errorf("default lifetime: %+v %v", st, err)
	}
	if note := (Store{Name: "Intake", Path: tmpRoot, Ephemeral: true, LifetimeHours: 1}).expiryNote("CASE-3"); !strings.Contains(note, "deleted in about") {
		t.Errorf("expiry note: %q", note)
	}
}
