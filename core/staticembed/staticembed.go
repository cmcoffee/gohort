// Package staticembed is an embedder that runs inside gohort: a static
// (model2vec) model, potion-retrieval-32M, where a text's vector is the mean
// of a stored vector per token. No network, no GPU, no queue: a chunk takes
// well under a millisecond on one core. On SciFact it scored within a point
// of nomic-embed served by llama.cpp (nDCG@10 0.622 against 0.628).
//
// The weights are not in the binary. Fetch downloads them once, pinned to one
// revision and checked against pinned SHA-256 sums, and Load reads them back,
// checking the sums again, so a file copied in by hand (a machine with no
// way out) is held to the same check.
package staticembed

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Name is the model, as the embedding space is stamped with it.
const Name = "potion-retrieval-32M"

// Dims is the length of every vector it makes.
const Dims = 512

// maxTokens is where a text is cut, as model2vec cuts it.
const maxTokens = 512

// file is one file the model is made of, as the pinned revision has it.
type file struct {
	name   string
	size   int64
	sha256 string
}

// files are the two files Load reads: the token vectors and the vocabulary.
var files = []file{
	{"model.safetensors", 129210456, "07609e5bd33aad37900b3fd62f4ec96f6daec88ca4d46b9d8b928bfababf6ea0"},
	{"tokenizer.json", 1493150, "7d75cbc54318138807c401b0f0c9721117c628b39de8e8e0edb6cb17e0ee7d18"},
}

// TotalBytes is what Fetch downloads when nothing is there yet.
func TotalBytes() int64 {
	var n int64
	for _, f := range files {
		n += f.size
	}
	return n
}

// Present reports whether dir holds every file at its pinned size. A size
// says the download finished; Load checks the contents.
func Present(dir string) bool {
	for _, f := range files {
		st, err := os.Stat(filepath.Join(dir, f.name))
		if err != nil || st.Size() != f.size {
			return false
		}
	}
	return true
}

// Model is the loaded embedder. Safe for concurrent use: Embed only reads.
type Model struct {
	vocab map[string]int32
	unk   int32
	q8    []int8    // vocab x Dims, each row scaled to fit 8 bits
	scale []float32 // per row
}

// Load reads the model from dir, checking each file against its pinned sum
// as it reads. The token vectors are kept at 8 bits (about 32 MB), which
// scored the same as the 32-bit originals.
func Load(dir string) (*Model, error) {
	m := &Model{}
	if err := checkedRead(dir, files[1], m.readTokenizer); err != nil {
		return nil, err
	}
	if err := checkedRead(dir, files[0], m.readVectors); err != nil {
		return nil, err
	}
	if int(m.unk) < 0 || int(m.unk) >= len(m.scale) {
		return nil, fmt.Errorf("%s: the unknown token is outside the vocabulary", Name)
	}
	return m, nil
}

// checkedRead opens f in dir and hands read the file through a hash, then
// reads whatever read left, and fails when the sum is not the pinned one.
func checkedRead(dir string, f file, read func(io.Reader) error) error {
	fh, err := os.Open(filepath.Join(dir, f.name))
	if err != nil {
		return err
	}
	defer fh.Close()
	h := sha256.New()
	r := bufio.NewReaderSize(io.TeeReader(fh, h), 1<<20)
	if err := read(r); err != nil {
		return fmt.Errorf("%s: %w", f.name, err)
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		return fmt.Errorf("%s: %w", f.name, err)
	}
	return sumMatches(f, h)
}

func sumMatches(f file, h hash.Hash) error {
	if got := hex.EncodeToString(h.Sum(nil)); got != f.sha256 {
		return fmt.Errorf("%s is not the expected file (sha256 %s, want %s): delete it and download again", f.name, got, f.sha256)
	}
	return nil
}

func (m *Model) readTokenizer(r io.Reader) error {
	var tok struct {
		Model struct {
			Vocab map[string]int32 `json:"vocab"`
			Unk   string           `json:"unk_token"`
		} `json:"model"`
	}
	if err := json.NewDecoder(r).Decode(&tok); err != nil {
		return err
	}
	if len(tok.Model.Vocab) == 0 {
		return fmt.Errorf("no vocabulary")
	}
	unk, ok := tok.Model.Vocab[tok.Model.Unk]
	if !ok {
		return fmt.Errorf("the unknown token %q is not in the vocabulary", tok.Model.Unk)
	}
	m.vocab, m.unk = tok.Model.Vocab, unk
	return nil
}

// readVectors reads the safetensors file's one tensor, "embeddings", a row
// at a time, quantizing each as it goes so the 32-bit copy is never held.
func (m *Model) readVectors(r io.Reader) error {
	var n uint64
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return err
	}
	if n > 1<<20 {
		return fmt.Errorf("a header of %d bytes", n)
	}
	hdr := make([]byte, n)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return err
	}
	var h map[string]json.RawMessage
	if err := json.Unmarshal(hdr, &h); err != nil {
		return err
	}
	var e struct {
		Dtype   string  `json:"dtype"`
		Shape   []int   `json:"shape"`
		Offsets []int64 `json:"data_offsets"`
	}
	if err := json.Unmarshal(h["embeddings"], &e); err != nil {
		return err
	}
	if e.Dtype != "F32" || len(e.Shape) != 2 || e.Shape[1] != Dims || len(e.Offsets) != 2 ||
		e.Offsets[1]-e.Offsets[0] != int64(e.Shape[0])*Dims*4 {
		return fmt.Errorf("unexpected tensor: %s %v", e.Dtype, e.Shape)
	}
	if _, err := io.CopyN(io.Discard, r, e.Offsets[0]); err != nil {
		return err
	}
	rows := e.Shape[0]
	m.q8 = make([]int8, rows*Dims)
	m.scale = make([]float32, rows)
	raw := make([]byte, Dims*4)
	row := make([]float32, Dims)
	for i := 0; i < rows; i++ {
		if _, err := io.ReadFull(r, raw); err != nil {
			return err
		}
		var mx float32
		for j := range row {
			row[j] = math.Float32frombits(binary.LittleEndian.Uint32(raw[j*4:]))
			if a := float32(math.Abs(float64(row[j]))); a > mx {
				mx = a
			}
		}
		s := mx / 127
		if s == 0 {
			s = 1
		}
		m.scale[i] = s
		q := m.q8[i*Dims : (i+1)*Dims]
		for j, v := range row {
			q[j] = int8(math.Round(float64(v / s)))
		}
	}
	return nil
}

// Embed is text's vector, of unit length. A text with no known word in it
// is the zero vector, which compares as unrelated to everything.
func (m *Model) Embed(text string) []float32 {
	v := make([]float32, Dims)
	for _, id := range m.tokenize(text) {
		if id == m.unk {
			continue
		}
		s := m.scale[id]
		row := m.q8[int(id)*Dims : (int(id)+1)*Dims]
		for i := range v {
			v[i] += float32(row[i]) * s
		}
	}
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= n
	}
	return v
}

// tokenize is the model's tokenizer (bge-base-en-v1.5's: uncased WordPiece),
// cut at maxTokens. A word with a piece the vocabulary lacks is the unknown
// token, which Embed leaves out, as model2vec does.
func (m *Model) tokenize(text string) []int32 {
	var ids []int32
	for _, w := range words(text) {
		if len([]rune(w)) > 100 {
			continue
		}
		var pieces []int32
		for start := 0; start < len(w); {
			end, found := len(w), int32(-1)
			for end > start {
				s := w[start:end]
				if start > 0 {
					s = "##" + s
				}
				if id, ok := m.vocab[s]; ok {
					found = id
					break
				}
				end--
				for end > start && w[end]&0xC0 == 0x80 { // back to the start of a character
					end--
				}
			}
			if found < 0 {
				pieces = nil
				break
			}
			pieces = append(pieces, found)
			start = end
		}
		ids = append(ids, pieces...)
		if len(ids) >= maxTokens {
			return ids[:maxTokens]
		}
	}
	return ids
}

// words is the tokenizer's normalizer (drop control characters, lowercase,
// strip accents, a CJK character on its own) and pre-tokenizer (split on
// space and punctuation, each punctuation mark a word of its own).
func words(text string) []string {
	text = norm.NFD.String(strings.ToLower(text))
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range text {
		switch {
		case r == 0 || r == 0xFFFD || (unicode.IsControl(r) && r != '\t' && r != '\n' && r != '\r'):
		case unicode.Is(unicode.Mn, r):
		case unicode.IsSpace(r):
			flush()
		case isPunct(r) || isCJK(r):
			flush()
			out = append(out, string(r))
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// isPunct is BERT's punctuation: every ASCII symbol, and Unicode's.
func isPunct(r rune) bool {
	if (r >= 33 && r <= 47) || (r >= 58 && r <= 64) || (r >= 91 && r <= 96) || (r >= 123 && r <= 126) {
		return true
	}
	return unicode.IsPunct(r)
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) || (r >= 0x2B740 && r <= 0x2B81F) || (r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) || (r >= 0x2F800 && r <= 0x2FA1F)
}
