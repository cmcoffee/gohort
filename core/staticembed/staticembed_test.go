package staticembed

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tinyModel writes a model of a few tokens into a fresh directory, each
// token's vector one-hot at its own index (scaled), and points files at it
// for the length of the test.
func tinyModel(t *testing.T) (dir string, blobs map[string][]byte) {
	t.Helper()
	vocab := map[string]int32{"[UNK]": 0, "cafe": 1, "open": 2, "##ing": 3, "late": 4, ",": 5}
	tok, _ := json.Marshal(map[string]any{"model": map[string]any{"vocab": vocab, "unk_token": "[UNK]"}})
	rows := len(vocab)
	hdr, _ := json.Marshal(map[string]any{"embeddings": map[string]any{
		"dtype": "F32", "shape": []int{rows, Dims}, "data_offsets": []int64{0, int64(rows * Dims * 4)}}})
	var st []byte
	st = binary.LittleEndian.AppendUint64(st, uint64(len(hdr)))
	st = append(st, hdr...)
	for r := 0; r < rows; r++ {
		for c := 0; c < Dims; c++ {
			v := float32(0)
			if c == r {
				v = float32(r) // a distinct length per token, so a sum shows its parts
			}
			st = binary.LittleEndian.AppendUint32(st, math.Float32bits(v))
		}
	}
	blobs = map[string][]byte{"model.safetensors": st, "tokenizer.json": tok}
	saved := files
	t.Cleanup(func() { files = saved })
	files = nil
	for _, name := range []string{"model.safetensors", "tokenizer.json"} {
		sum := sha256.Sum256(blobs[name])
		files = append(files, file{name, int64(len(blobs[name])), hex.EncodeToString(sum[:])})
	}
	dir = t.TempDir()
	return dir, blobs
}

func writeAll(t *testing.T, dir string, blobs map[string][]byte) {
	t.Helper()
	for name, b := range blobs {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A text's vector is the unit-length sum of its tokens' vectors: lowercased,
// accents off, a word split into its pieces, punctuation a token of its own,
// and a word the vocabulary cannot spell left out.
func TestEmbedSumsItsTokens(t *testing.T) {
	dir, blobs := tinyModel(t)
	writeAll(t, dir, blobs)
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	v := m.Embed("Café OPENING, zzz")
	// cafe (1) + open (2) + ##ing (3) + "," (5); zzz is not spelled, so left out.
	want := map[int]float64{1: 1, 2: 2, 3: 3, 5: 5}
	norm := math.Sqrt(1 + 4 + 9 + 25)
	for i, x := range v {
		if math.Abs(float64(x)-want[i]/norm) > 0.01 {
			t.Fatalf("v[%d] = %.3f, want %.3f (vector %v)", i, x, want[i]/norm, v[:6])
		}
	}
	if z := m.Embed("zzz qqq"); z[0] != 0 || z[1] != 0 {
		t.Errorf("a text with no known word should be the zero vector: %v", z[:6])
	}
}

// A file that is not the pinned one is refused, by its sum, however it got
// there: one copied in by hand is held to the same check as a download.
func TestLoadRefusesAnAlteredFile(t *testing.T) {
	dir, blobs := tinyModel(t)
	blobs["tokenizer.json"] = []byte(strings.Replace(string(blobs["tokenizer.json"]), "late", "lata", 1))
	writeAll(t, dir, blobs)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "not the expected file") {
		t.Fatalf("an altered tokenizer loaded: %v", err)
	}
}

// Fetch downloads what is missing, tells progress up to the total, and the
// result loads. A download whose sum is wrong never takes the file's name.
func TestFetchDownloadsAndChecks(t *testing.T) {
	dir, blobs := tinyModel(t)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := filepath.Base(r.URL.Path)
		asked = append(asked, name)
		w.Write(blobs[name])
	}))
	defer srv.Close()
	saved := Source
	t.Cleanup(func() { Source = saved })
	Source = srv.URL + "/rev/"

	var last, total int64
	if err := Fetch(context.Background(), dir, func(d, tot int64) { last, total = d, tot }); err != nil {
		t.Fatal(err)
	}
	if want := TotalBytes(); last != want || total != want {
		t.Errorf("progress ended at %d of %d, want %d of %d", last, total, want, want)
	}
	if !Present(dir) {
		t.Fatal("the files are not there after a download")
	}
	if _, err := Load(dir); err != nil {
		t.Fatalf("the download does not load: %v", err)
	}
	// Everything is there: nothing is downloaded again.
	asked = nil
	if err := Fetch(context.Background(), dir, nil); err != nil || len(asked) != 0 {
		t.Errorf("a second fetch asked for %v (err %v)", asked, err)
	}

	// An altered download fails on its sum and leaves nothing behind.
	other := t.TempDir()
	blobs["model.safetensors"] = append([]byte(nil), blobs["model.safetensors"]...)
	blobs["model.safetensors"][len(blobs["model.safetensors"])-1] ^= 1
	if err := Fetch(context.Background(), other, nil); err == nil || !strings.Contains(err.Error(), "not the expected file") {
		t.Fatalf("an altered download was accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "model.safetensors")); err == nil {
		t.Error("the altered download took the file's name")
	}
	if _, err := os.Stat(filepath.Join(other, "model.safetensors.part")); err == nil {
		t.Error("the altered download left its .part file")
	}
}

// The real model, when GOHORT_TEST_STATICEMBED names a folder holding it
// (it is not downloaded by a test): it loads against the pinned sums, and
// it ranks a related text above an unrelated one.
func TestTheRealModel(t *testing.T) {
	dir := os.Getenv("GOHORT_TEST_STATICEMBED")
	if dir == "" {
		t.Skip("GOHORT_TEST_STATICEMBED is not set")
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	dot := func(a, b []float32) (s float32) {
		for i := range a {
			s += a[i] * b[i]
		}
		return s
	}
	q := m.Embed("how do I reset my password")
	near := m.Embed("To change a forgotten password, open Settings and choose Reset password.")
	far := m.Embed("The quarterly sales figures rose in the northern region.")
	if dot(q, near) <= dot(q, far) {
		t.Errorf("related %.3f is not above unrelated %.3f", dot(q, near), dot(q, far))
	}
}

// Files at the right size that fail their sum are what Present cannot tell
// from good ones: Remove clears them, and the next Fetch brings them whole.
func TestRemoveThenFetchReplacesBadFiles(t *testing.T) {
	dir, blobs := tinyModel(t)
	good := map[string][]byte{}
	for k, v := range blobs {
		good[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(good[filepath.Base(r.URL.Path)])
	}))
	defer srv.Close()
	saved := Source
	t.Cleanup(func() { Source = saved })
	Source = srv.URL + "/"

	bad := append([]byte(nil), blobs["model.safetensors"]...)
	bad[len(bad)-1] ^= 1
	writeAll(t, dir, map[string][]byte{"model.safetensors": bad, "tokenizer.json": blobs["tokenizer.json"]})
	if !Present(dir) {
		t.Fatal("files at the right size should read as present")
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a bad file loaded")
	}
	if err := Remove(dir); err != nil || Present(dir) {
		t.Fatalf("Remove: %v, present after: %v", err, Present(dir))
	}
	if err := Fetch(context.Background(), dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Errorf("after a fresh fetch: %v", err)
	}
}
