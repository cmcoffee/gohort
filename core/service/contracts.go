package service

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/apijob"
)

// ValueType is the type of a contract input or output.
type ValueType string

const (
	TypeText   ValueType = "text"
	TypeNumber ValueType = "number"
	TypeJSON   ValueType = "json"
	TypeFile   ValueType = "file"
)

// Port is one input or output of a contract.
type Port struct {
	Name     string
	Type     ValueType
	Required bool
}

// Contract is a capability gohort defines: what goes in, what must come out,
// the check the output passes before anything uses it, and the smallest real
// call that proves a provider does it (Test). A spec can never redefine one.
type Contract struct {
	Name string
	// Gives is where the capability plugs into gohort, in the admin's words
	// (the setting that uses it), for the inside of a service's line.
	Gives   string
	Inputs  []Port
	Outputs []Port
	// Units are what a cost may be counted in.
	Units []string
	// Test are the inputs of a contract test; TestImages names its file
	// inputs that take a small sample picture, made fresh for each test.
	Test       map[string]any
	TestImages []string
	Check      func(r Result) error
	// units counts a successful call in Units[0], from what went in and
	// what came back.
	units func(in map[string]any, r Result) float64
	// TestAudio names file inputs that take a short sample of sound.
	TestAudio []string
	// testCheck is a check only a contract test makes, beyond Check: what a
	// sample call must give that a real one need not (a search for a common
	// phrase finds something; a real search may find nothing).
	testCheck func(r Result) error
}

func (c Contract) input(name string) (Port, bool) {
	for _, p := range c.Inputs {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

func (c Contract) output(name string) (Port, bool) {
	for _, p := range c.Outputs {
		if p.Name == name {
			return p, true
		}
	}
	return Port{}, false
}

// Result is what a capability gave back: each output by name.
type Result map[string]Value

// Value is one output: text, or a file on disk.
type Value struct {
	Text string
	File *File
}

// File is a file a call produced, written to disk as it arrived.
type File struct {
	Path string
	Type string // media type, from the bytes
	Size int64
}

var contracts = map[string]Contract{}

func register(c Contract) { contracts[c.Name] = c }

// Lookup returns the contract named name.
func Lookup(name string) (Contract, bool) {
	c, ok := contracts[name]
	return c, ok
}

// Names lists the contracts, sorted.
func Names() []string {
	out := make([]string, 0, len(contracts))
	for n := range contracts {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() {
	register(Contract{
		Name:  "image.generate",
		Gives: "Image Generation",
		Inputs: []Port{
			{Name: "prompt", Type: TypeText, Required: true},
			{Name: "negative", Type: TypeText},
			{Name: "width", Type: TypeNumber},
			{Name: "height", Type: TypeNumber},
			{Name: "seed", Type: TypeNumber},
			{Name: "steps", Type: TypeNumber},
			{Name: "model", Type: TypeText},
			// aspect is a ratio ("1:1", "16:9") for a service that takes one
			// in place of a size.
			{Name: "aspect", Type: TypeText},
		},
		Outputs: []Port{{Name: "image", Type: TypeFile, Required: true}},
		Units:   []string{"image"},
		Test:    map[string]any{"prompt": "A plain red circle on a white background.", "width": 256, "height": 256},
		Check:   checkImage("image"),
		units:   func(map[string]any, Result) float64 { return 1 },
	})
	register(Contract{
		Name:  "image.edit",
		Gives: "Image editing",
		Inputs: []Port{
			{Name: "prompt", Type: TypeText, Required: true},
			{Name: "image", Type: TypeFile, Required: true},
			{Name: "negative", Type: TypeText},
			{Name: "width", Type: TypeNumber},
			{Name: "height", Type: TypeNumber},
			{Name: "seed", Type: TypeNumber},
			{Name: "steps", Type: TypeNumber},
			{Name: "model", Type: TypeText},
		},
		Outputs:    []Port{{Name: "image", Type: TypeFile, Required: true}},
		Units:      []string{"image"},
		Test:       map[string]any{"prompt": "Make the circle blue."},
		TestImages: []string{"image"},
		Check:      checkImage("image"),
		units:      func(map[string]any, Result) float64 { return 1 },
	})
	register(Contract{
		Name:  "speech.transcribe",
		Gives: "Transcription",
		Inputs: []Port{
			{Name: "audio", Type: TypeFile, Required: true},
			// prompt is words the speaker is likely to say (names), which
			// an OpenAI-shaped transcriber reads as text before the clip.
			{Name: "prompt", Type: TypeText},
			{Name: "language", Type: TypeText},
			{Name: "model", Type: TypeText},
		},
		Outputs:   []Port{{Name: "text", Type: TypeText, Required: true}},
		Test:      map[string]any{},
		TestAudio: []string{"audio"},
		// A tone has no words: the test passes on any answer, empty text
		// included, since what is under test is that the service took the
		// audio and replied in the shape the spec reads.
		Check: func(r Result) error { return nil },
	})
	register(Contract{
		Name:  "speech.synthesize",
		Gives: "Speech",
		Inputs: []Port{
			{Name: "text", Type: TypeText, Required: true},
			{Name: "voice", Type: TypeText},
			{Name: "model", Type: TypeText},
		},
		Outputs: []Port{{Name: "audio", Type: TypeFile, Required: true}},
		Units:   []string{"character"},
		Test:    map[string]any{"text": "This is a test."},
		Check:   checkAudio("audio"),
		units: func(in map[string]any, _ Result) float64 {
			return float64(len([]rune(fmt.Sprint(in["text"]))))
		},
	})
	register(Contract{
		Name: "music.generate",
		Inputs: []Port{
			{Name: "prompt", Type: TypeText, Required: true},
			{Name: "lyrics", Type: TypeText},
			{Name: "style", Type: TypeText},
			{Name: "title", Type: TypeText},
			{Name: "duration", Type: TypeNumber},
			{Name: "model", Type: TypeText},
		},
		Outputs: []Port{{Name: "audio", Type: TypeFile, Required: true}},
		Units:   []string{"song"},
		Test:    map[string]any{"prompt": "A short, calm piano melody.", "duration": 10},
		Check:   checkAudio("audio"),
		units:   func(map[string]any, Result) float64 { return 1 },
	})
	// message.send is what lets a flow end by sending: a messaging API's
	// connector gives it. to is whom to send to, when the service needs one
	// and the connector does not set it; id is what the service calls the
	// message, when it says.
	register(Contract{
		Name:  "message.send",
		Gives: "Sending messages",
		Inputs: []Port{
			{Name: "text", Type: TypeText, Required: true},
			{Name: "file", Type: TypeFile},
			{Name: "to", Type: TypeText},
		},
		Outputs: []Port{{Name: "id", Type: TypeText}},
		Units:   []string{"message"},
		Test:    map[string]any{"text": "A test message from gohort."},
		// A sample picture goes with the test: a service that sends files
		// (a multipart send takes one every time) attaches it, and one that
		// sends text leaves it out.
		TestImages: []string{"file"},
		// What comes back is the service's receipt, in whatever shape it
		// has: a reply at all means the send was taken.
		Check: func(Result) error { return nil },
		units: func(map[string]any, Result) float64 { return 1 },
	})
	// llm.chat is a model: what gohort's tiers run on. It is not mapped step
	// by step like the others: its binding names the dialect gohort's own
	// client speaks to it, and its test is a short real chat.
	register(Contract{
		Name:  "llm.chat",
		Gives: "LLM",
		Inputs: []Port{
			{Name: "model", Type: TypeText},
			{Name: "context", Type: TypeNumber},
		},
		Units: []string{"token"},
		Check: func(Result) error { return nil },
	})
	// file.convert turns a file into another format: from is its format as
	// an extension (docx), to the one wanted (pdf, txt, png). What comes back
	// is checked here only for being there; the job that asked checks it is
	// the format it wanted.
	register(Contract{
		Name:  "file.convert",
		Gives: "File conversion",
		Inputs: []Port{
			{Name: "file", Type: TypeFile, Required: true},
			{Name: "to", Type: TypeText, Required: true},
			{Name: "from", Type: TypeText},
		},
		Outputs:    []Port{{Name: "file", Type: TypeFile, Required: true}},
		Units:      []string{"file"},
		Test:       map[string]any{"from": "png", "to": "jpg"},
		TestImages: []string{"file"},
		Check: func(r Result) error {
			if f := r["file"].File; f == nil || f.Size == 0 {
				return fmt.Errorf("no file came back")
			}
			return nil
		},
		units: func(map[string]any, Result) float64 { return 1 },
	})
	// mail.send is an email: to, subject and body, from the connector's
	// own sender unless one is given; id is the service's for the message.
	register(Contract{
		Name:  "mail.send",
		Gives: "Mail",
		Inputs: []Port{
			{Name: "to", Type: TypeText, Required: true},
			{Name: "subject", Type: TypeText, Required: true},
			{Name: "body", Type: TypeText, Required: true},
			{Name: "from", Type: TypeText},
			{Name: "file", Type: TypeFile},
		},
		Outputs: []Port{{Name: "id", Type: TypeText}},
		Units:   []string{"message"},
		Test:    map[string]any{"subject": "A test from gohort", "body": "This is a test from gohort's Connectors page. If it arrived, the connector sends mail."},
		// A reply at all means the service took the message.
		Check: func(Result) error { return nil },
		units: func(map[string]any, Result) float64 { return 1 },
	})
	// page.render is a page read the way a browser shows it: the readable
	// text of the page at url (and its title, when the service gives one).
	register(Contract{
		Name:  "page.render",
		Gives: "Page rendering",
		Inputs: []Port{
			{Name: "url", Type: TypeText, Required: true},
		},
		Outputs: []Port{
			{Name: "text", Type: TypeText, Required: true},
			{Name: "title", Type: TypeText},
		},
		Units: []string{"page"},
		Test:  map[string]any{"url": "https://example.com"},
		Check: func(r Result) error {
			if strings.TrimSpace(r["text"].Text) == "" {
				return fmt.Errorf("no text came back: check where the page's text is read from")
			}
			return nil
		},
		units: func(map[string]any, Result) float64 { return 1 },
	})
	// web.search is a search engine: results are a list of objects with a
	// title, a url and a snippet. A search can find nothing; its test, of a
	// phrase every engine knows, must find something, which is what proves
	// a spec reads the results from the right place.
	register(Contract{
		Name:  "web.search",
		Gives: "Web search",
		Inputs: []Port{
			{Name: "query", Type: TypeText, Required: true},
			{Name: "count", Type: TypeNumber},
		},
		Outputs: []Port{{Name: "results", Type: TypeJSON, Required: true}},
		Units:   []string{"search"},
		Test:    map[string]any{"query": "open source software", "count": 3},
		Check:   checkResults("results"),
		testCheck: func(r Result) error {
			if rs, _ := SearchResults(r); len(rs) == 0 {
				return fmt.Errorf("a search for a common phrase found nothing: check where the results are read from")
			}
			return nil
		},
		units: func(map[string]any, Result) float64 { return 1 },
	})
	register(Contract{
		Name:  "embed.text",
		Gives: "Embeddings",
		Inputs: []Port{
			{Name: "texts", Type: TypeJSON, Required: true},
			{Name: "model", Type: TypeText},
		},
		Outputs: []Port{{Name: "vectors", Type: TypeJSON, Required: true}},
		Units:   []string{"character"},
		// Two texts, so a service that answers a batch with one vector
		// fails its test rather than gohort's searches.
		Test:  map[string]any{"texts": []any{"The cat sat on the mat.", "Interest rates rose today."}},
		Check: checkVectors("vectors"),
		units: func(in map[string]any, _ Result) float64 {
			n := 0
			for _, t := range textsOf(in["texts"]) {
				n += len([]rune(t))
			}
			return float64(n)
		},
	})
}

// SearchResult is one result of a web.search.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// SearchResults reads a web.search result.
func SearchResults(r Result) ([]SearchResult, error) {
	var out []SearchResult
	if err := json.Unmarshal([]byte(r["results"].Text), &out); err != nil {
		return nil, fmt.Errorf("the results are not a list of title, url and snippet: %v", err)
	}
	return out, nil
}

// checkResults passes a list of results each with a web address: a result
// with none points nowhere, and a spec that gives one has read the wrong
// field for it.
func checkResults(output string) func(Result) error {
	return func(r Result) error {
		rs, err := SearchResults(Result{"results": r[output]})
		if err != nil {
			return err
		}
		for i, x := range rs {
			if !strings.HasPrefix(x.URL, "https://") && !strings.HasPrefix(x.URL, "http://") {
				return fmt.Errorf("result %d has no web address (%q): check which field url is read from", i+1, x.URL)
			}
		}
		return nil
	}
}

// textsOf reads a texts input as given in Go ([]string) or from JSON ([]any).
func textsOf(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
		return out
	}
	return nil
}

// Vectors reads an embed.text result: one vector of numbers per text.
func Vectors(r Result) ([][]float32, error) {
	var vecs [][]float32
	if err := json.Unmarshal([]byte(r["vectors"].Text), &vecs); err != nil {
		return nil, fmt.Errorf("the vectors are not lists of numbers: %v", err)
	}
	return vecs, nil
}

// checkVectors passes a list of vectors that are not empty and all of one
// length: vectors of two lengths come from two spaces and cannot be compared.
func checkVectors(output string) func(Result) error {
	return func(r Result) error {
		if _, ok := r[output]; !ok {
			return fmt.Errorf("no %s came back", output)
		}
		vecs, err := Vectors(Result{"vectors": r[output]})
		if err != nil {
			return err
		}
		if len(vecs) == 0 {
			return fmt.Errorf("no vectors came back")
		}
		for i, v := range vecs {
			if len(v) == 0 {
				return fmt.Errorf("vector %d is empty", i+1)
			}
			if len(v) != len(vecs[0]) {
				return fmt.Errorf("vector %d has %d numbers and the first has %d", i+1, len(v), len(vecs[0]))
			}
		}
		return nil
	}
}

// checkAudio passes a result whose output is sound gohort can play: MP3, WAV,
// OGG, FLAC, AAC or MP4 audio, by its first bytes.
func checkAudio(output string) func(Result) error {
	return func(r Result) error {
		v, ok := r[output]
		if !ok || v.File == nil {
			return fmt.Errorf("no %s came back", output)
		}
		f, err := os.Open(v.File.Path)
		if err != nil {
			return fmt.Errorf("the audio file is unreadable: %w", err)
		}
		defer f.Close()
		head := make([]byte, 16)
		n, _ := f.Read(head)
		if !looksLikeAudio(head[:n]) {
			return fmt.Errorf("what came back is not audio (%s)", sniffNote(head[:n]))
		}
		return nil
	}
}

func looksLikeAudio(h []byte) bool {
	switch {
	case len(h) >= 3 && string(h[:3]) == "ID3":
		return true
	case len(h) >= 2 && h[0] == 0xFF && h[1]&0xE0 == 0xE0: // an MPEG or AAC frame
		return true
	case len(h) >= 12 && string(h[:4]) == "RIFF" && string(h[8:12]) == "WAVE":
		return true
	case len(h) >= 4 && (string(h[:4]) == "OggS" || string(h[:4]) == "fLaC"):
		return true
	case len(h) >= 8 && string(h[4:8]) == "ftyp": // MP4 / M4A
		return true
	}
	return false
}

// sampleTone is one second of a 440 Hz tone, written as a WAV file.
func sampleTone() (*File, error) {
	const rate = 16000
	var pcm bytes.Buffer
	for i := 0; i < rate; i++ {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i)/rate))
		binary.Write(&pcm, binary.LittleEndian, v)
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+pcm.Len()))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(pcm.Len()))
	b.Write(pcm.Bytes())
	f, _, err := writeFile(&b, defaultMaxBytes)
	return f, err
}

// testInputs are the inputs of a contract test, with a fresh sample picture
// for each file input that takes one.
func (c Contract) testInputs() (map[string]any, error) {
	in := map[string]any{}
	for k, v := range c.Test {
		in[k] = v
	}
	for _, name := range c.TestImages {
		f, err := samplePicture()
		if err != nil {
			return nil, err
		}
		in[name] = f
	}
	for _, name := range c.TestAudio {
		f, err := sampleTone()
		if err != nil {
			return nil, err
		}
		in[name] = f
	}
	return in, nil
}

// samplePicture is a small red circle on white, written as a PNG.
func samplePicture() (*File, error) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if dx, dy := x-32, y-32; dx*dx+dy*dy < 20*20 {
				c = color.RGBA{220, 30, 30, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return nil, err
	}
	f, _, err := writeFile(&b, defaultMaxBytes)
	return f, err
}

// checkImage passes a result whose output is a picture gohort can show: PNG,
// JPEG, GIF or WebP, with a size.
func checkImage(output string) func(Result) error {
	return func(r Result) error {
		v, ok := r[output]
		if !ok || v.File == nil {
			return fmt.Errorf("no %s came back", output)
		}
		return imageFileOK(v.File.Path)
	}
}

func imageFileOK(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("the image file is unreadable: %w", err)
	}
	defer f.Close()
	head := make([]byte, 16)
	n, _ := f.Read(head)
	head = head[:n]
	// WebP has no decoder in the standard library; its header says enough.
	if len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")) {
		return nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	cfg, format, err := image.DecodeConfig(f)
	if err != nil {
		return fmt.Errorf("what came back is not an image (%s)", sniffNote(head))
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return fmt.Errorf("the %s image has no size", format)
	}
	return nil
}

func sniffNote(head []byte) string {
	s := strings.TrimSpace(string(head))
	switch {
	case s == "":
		return "it was empty"
	case strings.HasPrefix(s, "{") || strings.HasPrefix(s, "["):
		return "it starts like JSON"
	case strings.HasPrefix(strings.ToLower(s), "<"):
		return "it starts like HTML or XML"
	}
	return "its first bytes are not a known image format"
}

// cond is a condition over a reply: path == one|of|these, path != value, or
// a bare path that holds something.
type cond struct {
	path   string
	op     string // "==", "!=", or "" for present
	values []string
}

func parseCond(s string) (*cond, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	c := &cond{}
	for _, op := range []string{"==", "!="} {
		if i := strings.Index(s, op); i >= 0 {
			c.path, c.op = strings.TrimSpace(s[:i]), op
			for _, v := range strings.Split(s[i+len(op):], "|") {
				if v = strings.TrimSpace(v); v != "" {
					c.values = append(c.values, v)
				}
			}
			if len(c.values) == 0 {
				return nil, fmt.Errorf("condition %q compares with nothing", s)
			}
			break
		}
	}
	if c.op == "" {
		c.path = s
	}
	if c.path == "" || strings.ContainsAny(c.path, " =!") {
		return nil, fmt.Errorf("condition %q is not path == value, path != value, or a path", s)
	}
	if err := apijob.ValidPath(c.path); err != nil {
		return nil, err
	}
	return c, nil
}

// holds reports whether c is true of node, with vars filling any {token}
// in its path. A nil condition never holds.
func (c *cond) holds(node any, vars map[string]any) bool {
	if c == nil {
		return false
	}
	v := apijob.Lookup(node, fillPath(c.path, vars), "")
	switch c.op {
	case "==":
		return matchAny(v, c.values)
	case "!=":
		return v != "" && !matchAny(v, c.values)
	}
	return v != ""
}

func matchAny(v string, want []string) bool {
	for _, w := range want {
		if strings.EqualFold(w, v) {
			return true
		}
	}
	return false
}

// fillPath writes vars into a path's {tokens} ("outputs.{id}.images").
func fillPath(p string, vars map[string]any) string {
	return fill(p, vars, func(s string) string { return s })
}
