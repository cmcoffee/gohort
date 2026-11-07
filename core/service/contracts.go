package service

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"math"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/apijob"
	"github.com/cmcoffee/gohort/core/media"
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
	Gives string
	// About says what the capability is, with the shape of any JSON it
	// gives; Checks what its check requires of an answer, in words; Where
	// where in gohort it is used. The Contracts reference, the docs' table
	// and what Fix and Add tell the assistant are made from them.
	About   string
	Checks  string
	Where   string
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
	// tidy, when set, settles an answer before its check: what every
	// provider of the capability needs done the same way (a search's
	// relative links made whole), given the service's address.
	tidy func(r Result, base string) Result
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
		Name:   "image.generate",
		About:  "A picture made from a prompt.",
		Checks: "image is a picture gohort can show: PNG, JPEG, GIF or WebP, with a size.",
		Where:  "Admin > Services > Image Generation, and agents' image tools",
		Gives:  "Image Generation",
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
		Name:   "image.edit",
		About:  "A picture changed as a prompt asks.",
		Checks: "image is a picture gohort can show: PNG, JPEG, GIF or WebP, with a size.",
		Where:  "Admin > Services > Image Generation, and agents' image tools",
		Gives:  "Image editing",
		Inputs: []Port{
			{Name: "prompt", Type: TypeText, Required: true},
			{Name: "image", Type: TypeFile, Required: true},
			// More pictures to work from: a blend, a composite, a face from
			// one onto another. A spec that uses them takes that many.
			{Name: "image2", Type: TypeFile},
			{Name: "image3", Type: TypeFile},
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
		Name:   "speech.transcribe",
		About:  "The words spoken in a sound file.",
		Checks: "Any answer the spec can read, empty text included: the test's sound is a tone with no words.",
		Where:  "Admin > Services > Audio Transcription: \"Transcribe with\"",
		Gives:  "Transcription",
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
		Name:   "speech.synthesize",
		About:  "A text spoken aloud, in a voice when one is given.",
		Checks: "audio is sound gohort can play: MP3, WAV, OGG, FLAC, AAC or MP4 audio, judged by its first bytes.",
		Where:  "Admin > Services > Speech (TTS): \"Speak with\"",
		Gives:  "Speech",
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
	// speech.voices is the list a speech service's voices are chosen from
	// (the Voice field's chips): each a bare id, or an object naming it under
	// id or voice_id, with a person's name for it under name. A service
	// without a list leaves it out, and the voice is typed.
	register(Contract{
		Name:    "speech.voices",
		About:   "The voices a speech service offers. voices is a list, each a bare id or an object naming the voice under id, voice_id or name (name is what people read, when there is an id too).",
		Checks:  "voices is a list, and each one names a voice.",
		Where:   "the Speech (TTS) Voice field's choices, from the connector that speaks",
		Gives:   "Speech voices",
		Outputs: []Port{{Name: "voices", Type: TypeJSON, Required: true}},
		Test:    map[string]any{},
		Check:   checkVoices("voices"),
	})
	register(Contract{
		Name:   "music.generate",
		About:  "A piece of music made from a prompt, with lyrics, a style, a title and a length when given.",
		Checks: "audio is sound gohort can play: MP3, WAV, OGG, FLAC, AAC or MP4 audio, judged by its first bytes.",
		Where:  "an agent tool of its own, named generate_music_ and the connector's name",
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
	// media.generate is the open end of media generation: any kind of media
	// a service makes from a prompt (video, 3D models, sound effects, icons),
	// named by its binding's kind, so a kind nobody planned for needs a spec
	// and no code. A file in, when given, is what to work from.
	register(Contract{
		Name:   "media.generate",
		About:  "A media file made from a prompt, of the kind the connector says it makes (its binding's kind: video, 3D model, sound effects), from a file to work from when given.",
		Checks: "file is a file that is not empty; what kind of file is the connector's to say.",
		Where:  "Admin > Services > Media Generation, and an agent tool of its own named generate_<kind>_<connector>",
		Gives:  "Media Generation",
		Inputs: []Port{
			{Name: "prompt", Type: TypeText, Required: true},
			{Name: "file", Type: TypeFile},
			{Name: "model", Type: TypeText},
		},
		Outputs: []Port{{Name: "file", Type: TypeFile, Required: true}},
		Units:   []string{"item"},
		Test:    map[string]any{"prompt": "A short, simple test of what you make."},
		Check: func(r Result) error {
			v, ok := r["file"]
			if !ok || v.File == nil || v.File.Size == 0 {
				return fmt.Errorf("no file came back")
			}
			return nil
		},
		units: func(map[string]any, Result) float64 { return 1 },
	})
	// message.send is a messaging API's send: a linked service's replies go
	// out through it. to is whom to send to, when the service needs one
	// and the connector does not set it; id is what the service calls the
	// message, when it says.
	register(Contract{
		Name:   "message.send",
		About:  "A message sent through a messaging API, with a file when one is given; to is whom to send to, when the service needs one and the connector does not set it, and id is the service's name for the message.",
		Checks: "Any answer: a reply at all means the service took the message.",
		Where:  "Extensions > Integrations, a linked service: the replies to what the same connector fetches",
		Gives:  "Sending messages",
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
	// message.fetch is a messaging API's new messages: since is where the
	// last fetch left off (its cursor), chat the conversation to fetch when
	// the service fetches one at a time. A message that does not say its
	// chat is in the chat asked for.
	register(Contract{
		Name:  "message.fetch",
		About: "New messages from a messaging API: since is where the last fetch left off (the cursor it gave), and chat the conversation, for a service that fetches one at a time. messages is a list of objects: id, chat, from, from_name, text and at; cursor is where the next fetch starts.",
		Checks: "messages is a list, and every message in it has an id. A message that does not say its chat is in the chat asked for. " +
			"The test fetches without a cursor, as a link's first fetch does.",
		Where: "Extensions > Integrations, a linked service: a linked service's messages reach its channels",
		Gives: "Fetching messages",
		Inputs: []Port{
			{Name: "since", Type: TypeText},
			{Name: "chat", Type: TypeText},
		},
		Outputs: []Port{
			{Name: "messages", Type: TypeJSON, Required: true},
			{Name: "cursor", Type: TypeText},
		},
		Units: []string{"fetch"},
		Test:  map[string]any{},
		Check: func(r Result) error {
			ms, err := FetchedMessages(r, "")
			if err != nil {
				return err
			}
			for i, m := range ms {
				if strings.TrimSpace(m.ID) == "" {
					return fmt.Errorf("message %d has no id: check which field id is read from", i+1)
				}
			}
			return nil
		},
		units: func(map[string]any, Result) float64 { return 1 },
	})
	// file.convert turns a file into another format: from is its format as
	// an extension (docx), to the one wanted (pdf, txt, png). What comes back
	// is checked here only for being there; the job that asked checks it is
	// the format it wanted.
	register(Contract{
		Name:   "file.convert",
		About:  "A file turned into another format: from is its format and to the one wanted, each as an extension (docx, pdf, png).",
		Checks: "A file that is not empty came back; the job that asked checks it is the format it wanted.",
		Where:  "Admin > Services > System Hooks: a job's Adapt, \"Done by\"",
		Gives:  "File conversion",
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
		Name:   "mail.send",
		About:  "An email: to, subject and body, from the connector's own sender unless one is given, with a file when one is given; id is the service's name for the message.",
		Checks: "Any answer: a reply at all means the service took the message.",
		Where:  "Admin > Services > Mail (SMTP): \"Send with\"",
		Gives:  "Mail",
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
		Name:   "page.render",
		About:  "A page read the way a browser shows it: the readable text of the page at url, and its title when the service gives one.",
		Checks: "text is not empty.",
		Where:  "Admin > Services > Page Rendering (Browser): \"Render pages on\"",
		Gives:  "Page rendering",
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
	// title, a url and a snippet, and when the engine says, published. A search can find nothing; its test, of a
	// phrase every engine knows, must find something, which is what proves
	// a spec reads the results from the right place.
	register(Contract{
		Name:   "web.search",
		About:  "A search of the web. results is a list of objects: title, url and snippet, and published when the engine says when a page was published.",
		Checks: "results is a list, and every url in it is an http or https address. The test, a search for a common phrase, must find at least one.",
		Where:  "Admin > Services > Web Search: \"Search with\"",
		Gives:  "Web search",
		Inputs: []Port{
			{Name: "query", Type: TypeText, Required: true},
			{Name: "count", Type: TypeNumber},
		},
		Outputs: []Port{{Name: "results", Type: TypeJSON, Required: true}},
		Units:   []string{"search"},
		Test:    map[string]any{"query": "open source software", "count": 3},
		Check:   checkResults("results"),
		tidy:    tidyResults,
		testCheck: func(r Result) error {
			if rs, _ := SearchResults(r); len(rs) == 0 {
				return fmt.Errorf("a search for a common phrase found nothing: check where the results are read from")
			}
			return nil
		},
		units: func(map[string]any, Result) float64 { return 1 },
	})
	// image.search finds pictures already on the web: results are a list of
	// objects with an image_url (the picture itself), and its title, the
	// site it is on (source) and the page it is on (link).
	register(Contract{
		Name:   "image.search",
		About:  "Pictures already on the web. results is a list of objects: image_url (the picture itself), title, source (the site it is on) and link (the page it is on).",
		Checks: "results is a list, and every image_url in it is an http or https address. The test, a search for a common subject, must find at least one.",
		Where:  "agents' find_image, through the connector Web Search is set to",
		Gives:  "Image search",
		Inputs: []Port{
			{Name: "query", Type: TypeText, Required: true},
			{Name: "count", Type: TypeNumber},
		},
		Outputs: []Port{{Name: "results", Type: TypeJSON, Required: true}},
		Units:   []string{"search"},
		Test:    map[string]any{"query": "lighthouse", "count": 3},
		Check:   checkImageResults("results"),
		testCheck: func(r Result) error {
			if rs, _ := ImageResults(r); len(rs) == 0 {
				return fmt.Errorf("a search for a common subject found no pictures: check where the results are read from")
			}
			return nil
		},
		units: func(map[string]any, Result) float64 { return 1 },
	})
	register(Contract{
		Name:   "embed.text",
		About:  "Vectors for texts, for semantic search: texts is a list of strings, and vectors a list of lists of numbers, one for each text.",
		Checks: "At least one vector came back, none of them empty, all the same length. The test embeds two texts.",
		Where:  "Admin > Services > Embeddings: \"Embed with\"",
		Gives:  "Embeddings",
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

// SearchResult is one result of a web.search. Published is when the page
// was published, as the engine gives it (a stamp, a day, "3 days ago"), ""
// when it gives none.
type SearchResult struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Snippet   string `json:"snippet"`
	Published string `json:"published,omitempty"`
}

// SearchResults reads a web.search result.
func SearchResults(r Result) ([]SearchResult, error) {
	var out []SearchResult
	if err := json.Unmarshal([]byte(r["results"].Text), &out); err != nil {
		return nil, fmt.Errorf("the results are not a list of title, url and snippet: %v", err)
	}
	return out, nil
}

// FetchedMessage is one message a message.fetch gave.
type FetchedMessage struct {
	ID       string `json:"id"`
	Chat     string `json:"chat"`
	From     string `json:"from"`
	FromName string `json:"from_name"`
	Text     string `json:"text"`
	At       string `json:"at"`
}

// FetchedMessages reads a message.fetch result, each message without a chat
// of its own in chat, the one asked for.
func FetchedMessages(r Result, chat string) ([]FetchedMessage, error) {
	var out []FetchedMessage
	if err := json.Unmarshal([]byte(r["messages"].Text), &out); err != nil {
		return nil, fmt.Errorf("the messages are not a list of id, chat, from, from_name, text and at: %v", err)
	}
	for i := range out {
		if strings.TrimSpace(out[i].Chat) == "" {
			out[i].Chat = chat
		}
	}
	return out, nil
}

// ImageResult is one result of an image.search.
type ImageResult struct {
	ImageURL string `json:"image_url"`
	Title    string `json:"title"`
	Source   string `json:"source"`
	Link     string `json:"link"`
}

// ImageResults reads an image.search result.
func ImageResults(r Result) ([]ImageResult, error) {
	var out []ImageResult
	if err := json.Unmarshal([]byte(r["results"].Text), &out); err != nil {
		return nil, fmt.Errorf("the results are not a list of image_url, title, source and link: %v", err)
	}
	return out, nil
}

// Voices reads a speech.voices result.
func Voices(r Result) ([]media.Voice, error) {
	var list []json.RawMessage
	if err := json.Unmarshal([]byte(r["voices"].Text), &list); err != nil {
		return nil, fmt.Errorf("the voices are not a list: %v", err)
	}
	return media.ReadVoices(list), nil
}

// checkVoices passes a list in which every entry names a voice: an entry
// naming none means the spec reads the wrong field for it.
func checkVoices(output string) func(Result) error {
	return func(r Result) error {
		var list []json.RawMessage
		if err := json.Unmarshal([]byte(r[output].Text), &list); err != nil {
			return fmt.Errorf("the voices are not a list: %v", err)
		}
		if n := len(media.ReadVoices(list)); n != len(list) {
			return fmt.Errorf("%d of the %d voices name no voice: each is an id, or an object with id, voice_id or name", len(list)-n, len(list))
		}
		return nil
	}
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

// tidyResults settles a search's results as every engine's are: tags out of
// titles and snippets (an engine marks the words it matched), a link given
// relative to the service, or as a bare DOI, made whole, and a result with
// neither title nor link left out. A result with no link is left out too
// when others have one, since the spec reads the right field and that
// result has none to give; when none has one, the check says the spec reads
// the wrong field.
func tidyResults(r Result, base string) Result {
	rs, err := SearchResults(r)
	if err != nil || len(rs) == 0 {
		return r
	}
	origin := ""
	if u, err := url.Parse(strings.TrimSpace(base)); err == nil && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	out := make([]SearchResult, 0, len(rs))
	linked := 0
	for _, x := range rs {
		x.Title, x.Snippet = plainText(x.Title), plainText(x.Snippet)
		x.URL = wholeLink(strings.TrimSpace(x.URL), origin)
		if x.Title == "" && x.URL == "" {
			continue
		}
		if x.Title == "" {
			x.Title = x.URL
		}
		if webLink(x.URL) {
			linked++
		}
		out = append(out, x)
	}
	if linked > 0 {
		kept := out[:0]
		for _, x := range out {
			if webLink(x.URL) {
				kept = append(kept, x)
			}
		}
		out = kept
	}
	b, _ := json.Marshal(out)
	r["results"] = Value{Text: string(b)}
	return r
}

// wholeLink is a result's link made whole: a bare DOI at doi.org, a path
// on the service's own origin.
func wholeLink(link, origin string) string {
	switch {
	case link == "" || webLink(link):
		return link
	case strings.HasPrefix(link, "10.") && strings.Contains(link, "/"):
		return "https://doi.org/" + link
	case strings.HasPrefix(link, "//"):
		return "https:" + link
	case strings.HasPrefix(link, "/") && origin != "":
		return origin + link
	}
	return link
}

func webLink(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")
}

// plainText is s without HTML tags or entities, its spaces folded.
func plainText(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>' && in:
			in = false
			b.WriteByte(' ')
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}

// checkImageResults passes a list of pictures each with a web address: one
// with none is no picture, and a spec that gives one has read the wrong
// field for image_url.
func checkImageResults(output string) func(Result) error {
	return func(r Result) error {
		rs, err := ImageResults(Result{"results": r[output]})
		if err != nil {
			return err
		}
		for i, x := range rs {
			if !strings.HasPrefix(x.ImageURL, "https://") && !strings.HasPrefix(x.ImageURL, "http://") {
				return fmt.Errorf("picture %d has no web address (%q): check which field image_url is read from", i+1, x.ImageURL)
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
	f, _, err := writeFile(bytes.NewReader(SampleTone()), defaultMaxBytes)
	return f, err
}

// SampleTone is one second of a 440 Hz tone as a WAV file's bytes: what a
// transcriber is tested with. It has no words, so any text back, none
// included, means the audio was taken.
func SampleTone() []byte {
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
	// One field at a time: binary.Write takes no []any, and given one it
	// wrote nothing and said so in an error nobody read. The tone went out
	// with no format chunk, and whisper.cpp answered the Test with a 400.
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(rate), uint32(rate * 2), uint16(2), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(pcm.Len()))
	b.Write(pcm.Bytes())
	return b.Bytes()
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
