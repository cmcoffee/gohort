// Package flows is how gohort gets work done by outside tools.
//
// A JOB (jobs.go) is one thing gohort needs done: read an iPhone photo,
// pull the text out of a PDF, write a guide as Word. It is a contract,
// defined here in Go: what goes in (a file, a document, values like a
// count), what has to come out (an image the model can read, text, a file
// of a named format, several files), whether it may use the network, and how
// the result is checked before anything uses it.
//
// A FLOW is how a job gets done: one or more steps, each a command, each
// step's result the next one's {in}. Every job has a NATIVE flow, the ideal
// tool for it (or Go itself, or the app), and an admin ADAPTS a job whose
// tool is missing, or that they want done another way, with a flow of their
// own: saved as a connector of kind ConnectorKind (apps/admin), approved
// like every other connector. An admin can also add a format gohort does
// not support at all, as a job of their own.
//
// Every command, native or adapted, runs the same way (run.go): as written,
// never through a shell's reading of it; in the sandbox, with the network
// only for a job that needs it; in a folder of its own, seeing the system
// folders and any folder the flow says it reads; stopped at its time limit;
// and its result checked by the job. A broken or hostile command can at
// worst produce something that is thrown away.
package flows

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/cmcoffee/gohort/core/media"
)

// Kind is what an input job produces.
type Kind string

const (
	// KindImage is an image the model can read: JPEG, PNG, GIF or WebP.
	KindImage Kind = "image"
	// KindText is readable text.
	KindText Kind = "text"
	// KindAudio is audio a speech service can read: MP3, WAV, M4A, OGG,
	// FLAC or AAC.
	KindAudio Kind = "audio"
	// KindVideo is a video file: MP4, MOV, WebM, MKV or AVI.
	KindVideo Kind = "video"
)

// Kinds are the kinds, in the order the page lists them.
var Kinds = []Kind{KindImage, KindText, KindAudio, KindVideo}

// readableImages are the image formats every vision server reads.
var readableImages = []string{"image/jpeg", "image/png", "image/gif", "image/webp"}

// Label is the kind as the page says it.
func (k Kind) Label() string {
	switch k {
	case KindImage:
		return "an image the model can read"
	case KindText:
		return "text"
	case KindAudio:
		return "audio a speech service can read"
	case KindVideo:
		return "a video file"
	}
	return string(k)
}

// Valid reports whether k is a kind.
func (k Kind) Valid() bool {
	return k == KindImage || k == KindText || k == KindAudio || k == KindVideo
}

// defaultExt is the result file's name extension when a command does not
// choose one ({out.jpg}): a program that picks its format by the name is
// told something it can write.
func (k Kind) defaultExt() string {
	switch k {
	case KindImage:
		return ".png"
	case KindAudio:
		return ".wav"
	case KindVideo:
		return ".mp4"
	}
	return ".txt"
}

// accept checks a result is of this kind, and returns it as gohort will use
// it: text with any invalid UTF-8 replaced, an image as it is.
func (k Kind) accept(out []byte) ([]byte, error) {
	switch k {
	case KindImage:
		ct := http.DetectContentType(out)
		if !contains(readableImages, ct) {
			return nil, fmt.Errorf("it is %s, not an image the model can read (JPEG, PNG, GIF or WebP)", ct)
		}
		if _, _, err := media.DecodeImage(out); err != nil {
			return nil, fmt.Errorf("it does not open as an image: %v", err)
		}
		return out, nil
	case KindText:
		if bytes.IndexByte(out, 0) >= 0 {
			return nil, errors.New("it is binary, not text")
		}
		if !utf8.Valid(out) {
			out = []byte(strings.ToValidUTF8(string(out), "�"))
		}
		return out, nil
	case KindAudio:
		if !contains(audioFormats, Sniff(out)) {
			return nil, fmt.Errorf("it is %s, not audio a speech service can read", Sniff(out))
		}
		return out, nil
	case KindVideo:
		if !contains(videoFormats, Sniff(out)) {
			return nil, fmt.Errorf("it is %s, not a video file", Sniff(out))
		}
		return out, nil
	}
	return nil, fmt.Errorf("%q is not a kind of result", k)
}

// Readable reports whether data is already of kind k, so needs no
// conversion: a JPEG for an image, UTF-8 text for text.
func Readable(data []byte, k Kind) bool {
	switch k {
	case KindImage:
		return contains(readableImages, http.DetectContentType(data))
	case KindText:
		return bytes.IndexByte(data, 0) < 0 && utf8.Valid(data)
	case KindAudio:
		return contains(audioFormats, Sniff(data))
	case KindVideo:
		return contains(videoFormats, Sniff(data))
	}
	return false
}

// format is an input format gohort knows by name.
type format struct {
	mime, label, ext string
}

var formats = []format{
	{"image/heic", "iPhone photos (HEIC)", ".heic"},
	{"image/avif", "AVIF images", ".avif"},
	{"image/tiff", "TIFF images", ".tiff"},
	{"image/bmp", "BMP images", ".bmp"},
	{"application/pdf", "PDF", ".pdf"},
	{MIMEDocx, "Word (.docx)", ".docx"},
	{MIMEOdt, "OpenDocument text (.odt)", ".odt"},
	{"application/rtf", "Rich Text (.rtf)", ".rtf"},
	{"application/msword", "Legacy Word (.doc)", ".doc"},
	{"video/mp4", "MP4 video", ".mp4"},
	{"video/quicktime", "QuickTime video (.mov)", ".mov"},
	{"video/webm", "WebM video", ".webm"},
	{"video/x-matroska", "Matroska video (.mkv)", ".mkv"},
	{"video/avi", "AVI video", ".avi"},
	{"audio/mpeg", "MP3 audio", ".mp3"},
	{"audio/wave", "WAV audio", ".wav"},
	{"audio/mp4", "M4A audio", ".m4a"},
	{"application/ogg", "Ogg audio", ".ogg"},
	{"audio/flac", "FLAC audio", ".flac"},
	{"audio/aac", "AAC audio", ".aac"},
}

// The document types with long names.
const (
	MIMEDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	MIMEOdt  = "application/vnd.oasis.opendocument.text"
)

// FormatLabel is a format as the page names it.
func FormatLabel(mime string) string {
	if mime == Any {
		return "anything else"
	}
	for _, f := range formats {
		if f.mime == mime {
			return f.label
		}
	}
	if _, sub, ok := strings.Cut(mime, "/"); ok {
		return strings.ToUpper(sub)
	}
	return mime
}

// FormatsLabel is several, joined.
func FormatsLabel(mimes []string) string {
	out := make([]string, len(mimes))
	for i, m := range mimes {
		out[i] = FormatLabel(m)
	}
	return strings.Join(out, ", ")
}

// Formats are the input formats a conversion may name: the ones a native
// conversion takes.
func Formats() []string {
	out := make([]string, len(formats))
	for i, f := range formats {
		out[i] = f.mime
	}
	return out
}

// ParseFormat reads a format as a person writes it: "heic", ".docx",
// "image/heic" or "*".
func ParseFormat(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == Any || strings.Contains(s, "/") {
		return s
	}
	s = strings.TrimPrefix(s, ".")
	for _, f := range formats {
		if strings.TrimPrefix(f.ext, ".") == s {
			return f.mime
		}
	}
	return s
}

// ShortName is a format as a person types it: "heic" for image/heic, the
// MIME type itself for one with no short name.
func ShortName(mime string) string {
	for _, f := range formats {
		if f.mime == mime {
			return strings.TrimPrefix(f.ext, ".")
		}
	}
	return mime
}

// extFor is the file extension for a format, so a program that reads its
// input's format from the name is told the right one.
func extFor(mime string) string {
	for _, f := range formats {
		if f.mime == mime {
			return f.ext
		}
	}
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/x-icon":
		return ".ico"
	case "text/markdown":
		return ".md"
	case "text/html":
		return ".html"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	}
	return ""
}

// Sniff is the format of data by its content, naming what Go's own
// detection does not: the HEIF family and AVIF by their ftyp brand, TIFF by
// its byte order mark, Word and OpenDocument by what their zip holds, RTF
// and legacy Word by their signatures.
func Sniff(data []byte) string {
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		switch string(data[8:12]) {
		case "avif", "avis":
			return "image/avif"
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return "image/heic"
		case "qt  ":
			return "video/quicktime"
		case "M4A ", "M4B ":
			return "audio/mp4"
		case "isom", "iso2", "mp41", "mp42", "avc1", "M4V ", "dash", "3gp4", "3gp5":
			return "video/mp4"
		}
	}
	switch {
	case bytes.HasPrefix(data, []byte("II*\x00")), bytes.HasPrefix(data, []byte("MM\x00*")):
		return "image/tiff"
	case bytes.HasPrefix(data, []byte(`{\rtf`)):
		return "application/rtf"
	case bytes.HasPrefix(data, []byte("\xD0\xCF\x11\xE0\xA1\xB1\x1A\xE1")):
		return "application/msword"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		if t := zipDocType(data); t != "" {
			return t
		}
	case bytes.HasPrefix(data, []byte("fLaC")):
		return "audio/flac"
	case bytes.HasPrefix(data, []byte("\x1A\x45\xDF\xA3")):
		if bytes.Contains(data[:min(len(data), 64)], []byte("webm")) {
			return "video/webm"
		}
		return "video/x-matroska"
	case len(data) > 2 && data[0] == 0xFF && data[1]&0xF6 == 0xF0:
		return "audio/aac" // an ADTS header
	case len(data) > 2 && data[0] == 0xFF && data[1]&0xE0 == 0xE0:
		return "audio/mpeg" // an MP3 frame with no ID3 tag ahead of it
	}
	ct := http.DetectContentType(data)
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return ct
}

// zipDocType is the document type a zip is, by what it holds.
func zipDocType(data []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ""
	}
	for _, f := range zr.File {
		switch f.Name {
		case "word/document.xml":
			return MIMEDocx
		case "mimetype":
			rc, err := f.Open()
			if err != nil {
				continue
			}
			var b [64]byte
			n, _ := rc.Read(b[:])
			rc.Close()
			if strings.HasPrefix(string(b[:n]), MIMEOdt) {
				return MIMEOdt
			}
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// splitArgs splits a command line the way a person typing it means it: on
// spaces, with '...' and "..." holding a value that has spaces in it, and a
// backslash escaping the next character. Nothing is expanded or run. The
// same reading as a file store's command lines (apps/filestore).
func splitArgs(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in, quote, escaped := false, byte(0), false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case escaped:
			cur.WriteByte(c)
			escaped, in = false, true
		case c == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if quote != 0 {
		return nil, errors.New("a quote is not closed")
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

// outFormat is a format gohort writes documents as.
type outFormat struct {
	ext, label, mime string
	// check proves a result is this format; nil only proves it is not empty.
	check func(out []byte) error
}

var outFormats = []outFormat{
	{"pdf", "PDF", "application/pdf", func(out []byte) error { return prefixed(out, "%PDF-", "a PDF") }},
	{"html", "HTML", "text/html; charset=utf-8", func(out []byte) error {
		if bytes.IndexByte(out, 0) >= 0 || !bytes.Contains(out, []byte("<")) {
			return errors.New("it is not HTML")
		}
		return nil
	}},
	{"md", "Markdown", "text/markdown; charset=utf-8", textOnly},
	{"docx", "Word (.docx)", MIMEDocx, func(out []byte) error { return zipped(out, MIMEDocx, "a Word document") }},
	{"odt", "OpenDocument (.odt)", MIMEOdt, func(out []byte) error { return zipped(out, MIMEOdt, "an OpenDocument file") }},
	{"epub", "EPUB", "application/epub+zip", func(out []byte) error { return prefixed(out, "PK\x03\x04", "an EPUB") }},
	{"rtf", "Rich Text (.rtf)", "application/rtf", func(out []byte) error { return prefixed(out, `{\rtf`, "an RTF file") }},
	{"txt", "Plain text", "text/plain; charset=utf-8", textOnly},
	{"xlsx", "Excel (.xlsx)", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", func(out []byte) error { return zipHolds(out, "xl/workbook.xml", "an Excel workbook") }},
	{"pptx", "PowerPoint (.pptx)", "application/vnd.openxmlformats-officedocument.presentationml.presentation", func(out []byte) error { return zipHolds(out, "ppt/presentation.xml", "a PowerPoint deck") }},
}

// zipHolds checks out is a zip holding name: how an Office file is told
// from any other zip.
func zipHolds(out []byte, name, what string) error {
	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err == nil {
		for _, f := range zr.File {
			if f.Name == name {
				return nil
			}
		}
	}
	return fmt.Errorf("it is not %s", what)
}

func prefixed(out []byte, prefix, what string) error {
	if !bytes.HasPrefix(out, []byte(prefix)) {
		return fmt.Errorf("it is not %s", what)
	}
	return nil
}

func zipped(out []byte, mime, what string) error {
	if zipDocType(out) != mime {
		return fmt.Errorf("it is not %s", what)
	}
	return nil
}

func textOnly(out []byte) error {
	if bytes.IndexByte(out, 0) >= 0 {
		return errors.New("it is binary, not text")
	}
	return nil
}

var extRE = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// ParseOutFormat reads an output format as a person writes it: "docx",
// ".docx" or "Word". "" when it is not a file extension.
func ParseOutFormat(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	for _, f := range outFormats {
		if strings.EqualFold(f.label, s) || strings.HasPrefix(strings.ToLower(f.label), s+" ") {
			return f.ext
		}
	}
	if extRE.MatchString(s) {
		return s
	}
	return ""
}

// OutLabel is an output format as the page names it.
func OutLabel(ext string) string {
	for _, f := range outFormats {
		if f.ext == ext {
			return f.label
		}
	}
	return strings.ToUpper(ext)
}

// OutMIME is the Content-Type an output is served with.
func OutMIME(ext string) string {
	for _, f := range outFormats {
		if f.ext == ext {
			return f.mime
		}
	}
	return "application/octet-stream"
}

// outTarget is an output format as a command's target: its result is named
// for it and checked by it.
type outTarget string

func (t outTarget) defaultExt() string { return "." + string(t) }

func (t outTarget) accept(out []byte) ([]byte, error) {
	if len(out) == 0 {
		return nil, errors.New("it is empty")
	}
	for _, f := range outFormats {
		if f.ext == string(t) && f.check != nil {
			if err := f.check(out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
