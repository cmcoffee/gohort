// Document text extraction for inbound file attachments (PDFs, Word
// documents). Used by chat-style apps that let the user attach
// non-image files via the paperclip — the extracted text gets
// prepended to the user message so the LLM has the document content
// in context.
//
// Implementation: shell out to commonly-installed extractors
// (poppler-utils' pdftotext for PDFs, pandoc for DOCX, antiword for
// legacy binary DOC — pandoc cannot read the old .doc format). No CGo,
// no large new dependency — the tradeoff is the deployment needs
// these binaries on PATH. They're standard on most Linux servers and
// available via brew/apt/dnf.
//
// Extraction returns the plain-text body. Callers compose the final
// prompt: a typical "Attached: <filename>\n\n<text>\n\n<user msg>"
// preamble works well.

package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
	"golang.org/x/net/html"
)

// DocumentExtractTimeout caps any extraction call. Large PDFs can
// take seconds; a per-doc timeout keeps a runaway extraction from
// holding the user's send open. The concrete value comes from core's
// tunable via the ExtractTimeout hook (wired at startup); defaults to
// 30s when unset (media is a leaf and can't read the tunables registry).
func DocumentExtractTimeout() time.Duration {
	if ExtractTimeout != nil {
		return ExtractTimeout()
	}
	return 30 * time.Second
}

// DocumentAttachment is one inbound document the user uploaded via
// the paperclip. Data is base64-decoded raw bytes; MimeType drives
// extractor selection. Name surfaces in the prompt preamble.
type DocumentAttachment struct {
	Name     string
	MimeType string
	Data     []byte
}

// ExtractDocument decodes the attachment to plain text using the
// extractor matched to its MIME type. Returns the extracted text
// (possibly empty) or an error if the format is unsupported / the
// extractor isn't installed / the call failed.
//
// MIME-based dispatch handles the common types directly; falls back
// to inspecting the filename extension when the browser-supplied
// MIME is generic (application/octet-stream).
func ExtractDocument(ctx context.Context, doc DocumentAttachment) (string, error) {
	mime := strings.ToLower(strings.TrimSpace(doc.MimeType))
	ext := strings.ToLower(filepath.Ext(doc.Name))
	nfo.Debug("[document_extract] %q mime=%q ext=%q size=%d", doc.Name, mime, ext, len(doc.Data))
	switch {
	case mime == "application/pdf" || ext == ".pdf":
		return extractPDF(ctx, doc.Data)
	case mime == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || ext == ".docx":
		return extractWithPandoc(ctx, doc.Data, "docx")
	case mime == "application/msword" || ext == ".doc":
		// Legacy binary Word (.doc) — pandoc has NO reader for this
		// format (only .docx), so it routes to antiword instead.
		return extractWithAntiword(ctx, doc.Data)
	case strings.HasPrefix(mime, "text/html") || ext == ".html" || ext == ".htm":
		// HTML needs parsing — returning the raw bytes as "text"
		// dumps markup (typically 5-10× the actual readable text)
		// straight into the corpus, which then chokes the embedder
		// and pollutes search results with <script>/<style> noise.
		return extractHTML(doc.Data)
	case strings.HasPrefix(mime, "text/") || ext == ".txt" || ext == ".md" || ext == ".log" || ext == ".csv":
		return string(doc.Data), nil
	case isAudioAttachment(mime, ext):
		nfo.Debug("[document_extract] routing %q to audio transcription branch", doc.Name)
		return extractAudio(ctx, doc)
	}
	nfo.Debug("[document_extract] no extractor matched for %q (mime=%q ext=%q)", doc.Name, mime, ext)
	return "", fmt.Errorf("unsupported document type: mime=%q ext=%q", mime, ext)
}

// isAudioAttachment matches the audio mime + extension set that
// gates routing into the transcription pipeline. Includes the common
// formats whisper.cpp accepts directly (mp3/wav/m4a/ogg/flac) plus
// webm and 3gp for mobile-recorded clips.
func isAudioAttachment(mime, ext string) bool {
	if strings.HasPrefix(mime, "audio/") {
		return true
	}
	switch ext {
	case ".mp3", ".wav", ".m4a", ".aac", ".ogg", ".flac", ".webm", ".3gp", ".opus":
		return true
	}
	return false
}

// extractHTML walks a parsed HTML document and returns just the
// readable text — strips <script>, <style>, <nav>, <header>, <footer>,
// and aria-hidden content. Output is normalized whitespace (single
// spaces between words, blank-line-separated blocks). Way more
// embedding-friendly than raw HTML (typically 5-10× smaller and
// without markup noise that pollutes search results).
//
// Failure path: if the HTML parser errors, fall back to returning
// the raw bytes as text — better partial extraction than total
// rejection on a quirky page.
func extractHTML(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		// Fall back to raw bytes — degraded but still usable for
		// embedding/search vs. dropping the page entirely.
		return string(data), nil
	}
	var sb strings.Builder
	walkHTMLText(doc, &sb)
	text := strings.TrimSpace(sb.String())
	// Collapse multiple blank lines and tighten whitespace per
	// line — HTML parsing introduces newline-runs around block
	// elements that don't add semantic content.
	text = collapseHTMLWhitespace(text)
	return text, nil
}

// walkHTMLText recursively descends an HTML node tree, appending
// text content from nodes that aren't markup-only. Skips
// <script>, <style>, <noscript>, <nav>, <header>, <footer>,
// aria-hidden, and any node whose class/id matches well-known
// noise patterns (comment threads, sidebars, ads, share widgets,
// cookie banners, related-content blocks, newsletter signups).
func walkHTMLText(n *html.Node, sb *strings.Builder) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		switch strings.ToLower(n.Data) {
		case "script", "style", "noscript", "nav", "header", "footer", "aside", "iframe", "svg", "form":
			return
		}
		// aria-hidden is always a hard drop — invisible to readers
		// by author intent.
		for _, a := range n.Attr {
			if strings.EqualFold(a.Key, "aria-hidden") && strings.EqualFold(a.Val, "true") {
				return
			}
		}
		// Class / id / role pattern filtering. Hard-drop nodes
		// return immediately; tag-eligible nodes get their
		// content extracted but inside a marker so the chunker
		// (if it's HTML-extract-only flow) sees the boundary.
		// For the basic walkHTMLText caller, tag info is dropped
		// — full tagging requires the section-aware extractor
		// (see extractHTMLWithTags below).
		if _, hit := classifyHTMLNode(n.Attr); hit {
			// In the simple/plain walker we hard-drop both
			// categories — preserving the OLD filtering semantic
			// for callers that don't ask for tag-aware output.
			// extractHTMLWithTags walks via a different path and
			// surfaces the tag.
			return
		}
		// Insert paragraph breaks around block-level elements so
		// the output has reasonable paragraph structure.
		switch strings.ToLower(n.Data) {
		case "p", "div", "section", "article", "h1", "h2", "h3", "h4", "h5", "h6", "li", "br", "tr":
			sb.WriteString("\n")
		}
	}
	if n.Type == html.TextNode {
		t := strings.TrimSpace(n.Data)
		if t != "" {
			sb.WriteString(t)
			sb.WriteString(" ")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkHTMLText(c, sb)
	}
}

// hardDropPatterns are class/id tokens that mark pure UI chrome
// with zero information value — extracted text would just be
// noise polluting the corpus. Dropped entirely at extraction
// time.
var hardDropPatterns = []string{
	"sidebar", "side-bar",
	"newsletter", "subscribe", "subscription", "signup", "sign-up",
	"social", "share", "sharing", "share-buttons",
	"cookie", "cookies", "gdpr", "consent",
	"ad", "ads", "advert", "advertisement", "promo", "promotion",
	"banner",
	"breadcrumb", "breadcrumbs",
	"pagination",
	"toc", "table-of-contents", // navigational, not content
	"footnotes", "footer-content",
}

// tagPatterns map class/id tokens to a Kind tag that gets
// attached to chunks extracted from those nodes. Content is
// PRESERVED (not dropped) so a downstream consumer can still
// surface it with provenance — knowledge agents are taught to
// frame "user_comment" / "related_link" / "author_bio" hits
// differently from default authoritative content.
var tagPatterns = map[string]string{
	"comment":          "user_comment",
	"comments":         "user_comment",
	"related":          "related_link",
	"related-posts":    "related_link",
	"related-articles": "related_link",
	"you-might-also":   "related_link",
	"trending":         "related_link",
	"popular":          "related_link",
	"recommended":      "related_link",
	"author-bio":       "author_bio",
	"author-card":      "author_bio",
}

// classifyHTMLNode inspects a node's class/id/role attributes
// and returns one of:
//   - ("", false) → no match (extract normally, no tag)
//   - ("drop", true) → hard drop, don't extract at all
//   - ("<kind>", true) → extract but tag as <kind>
//
// Token-bounded matching prevents "main-content" from triggering
// "ad" (no token boundary around the substring), while "ad-slot",
// "advert", and "comments-section" all match.
func classifyHTMLNode(attrs []html.Attribute) (string, bool) {
	for _, a := range attrs {
		k := strings.ToLower(a.Key)
		v := strings.ToLower(a.Val)
		if k == "role" && (v == "navigation" || v == "complementary" || v == "banner" || v == "contentinfo") {
			return "drop", true
		}
		if k != "class" && k != "id" {
			continue
		}
		for _, classToken := range strings.Fields(v) {
			for _, p := range strings.FieldsFunc(classToken, func(r rune) bool {
				return r == '-' || r == '_'
			}) {
				if p == "" {
					continue
				}
				if kind, ok := tagPatterns[p]; ok {
					return kind, true
				}
				for _, np := range hardDropPatterns {
					if p == np {
						return "drop", true
					}
				}
			}
		}
	}
	return "", false
}

// ExtractHTMLByKind extracts an HTML document into kind-bucketed
// text. Returns a map { kind → concatenated text }. The empty-key
// "" bucket holds default (authoritative) content; named keys
// hold content extracted from tagged regions ("user_comment" for
// comments threads, "related_link" for related-post rails,
// "author_bio" for byline blurbs). Hard-drop noise (ads, cookie
// banners, share widgets, sidebars) is NOT in any bucket.
//
// Callers can then ingest each bucket separately with its Kind
// tag so consumers at retrieval time know how to frame each hit.
func ExtractHTMLByKind(data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return map[string]string{"": string(data)}, nil
	}
	buckets := map[string]*strings.Builder{}
	getBuf := func(kind string) *strings.Builder {
		if b, ok := buckets[kind]; ok {
			return b
		}
		b := &strings.Builder{}
		buckets[kind] = b
		return b
	}
	var walk func(n *html.Node, currentKind string)
	walk = func(n *html.Node, currentKind string) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript", "nav", "header", "footer", "aside", "iframe", "svg", "form":
				return
			}
			for _, a := range n.Attr {
				if strings.EqualFold(a.Key, "aria-hidden") && strings.EqualFold(a.Val, "true") {
					return
				}
			}
			// Per-node classification — descend into tagged
			// regions with their kind so chunks land in the
			// right bucket. Hard-drop returns without
			// descending. Untagged nodes inherit the parent
			// kind (so a div inside a "comments" section stays
			// in the user_comment bucket).
			if kind, hit := classifyHTMLNode(n.Attr); hit {
				if kind == "drop" {
					return
				}
				currentKind = kind
			}
			switch strings.ToLower(n.Data) {
			case "p", "div", "section", "article", "h1", "h2", "h3", "h4", "h5", "h6", "li", "br", "tr":
				getBuf(currentKind).WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			t := strings.TrimSpace(n.Data)
			if t != "" {
				buf := getBuf(currentKind)
				buf.WriteString(t)
				buf.WriteString(" ")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, currentKind)
		}
	}
	walk(doc, "")
	out := make(map[string]string, len(buckets))
	for k, b := range buckets {
		text := collapseHTMLWhitespace(strings.TrimSpace(b.String()))
		if text != "" {
			out[k] = text
		}
	}
	return out, nil
}

// collapseHTMLWhitespace tightens the post-walk text: collapse
// 3+ newlines to 2, strip trailing spaces, drop leading whitespace
// on lines.
func collapseHTMLWhitespace(s string) string {
	// First normalize line-internal whitespace.
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blankRun := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Collapse multiple spaces inside a line.
		fields := strings.Fields(trimmed)
		joined := strings.Join(fields, " ")
		if joined == "" {
			blankRun++
			if blankRun <= 1 {
				out = append(out, "")
			}
			continue
		}
		blankRun = 0
		out = append(out, joined)
	}
	return strings.Join(out, "\n")
}

// extractAudio runs the configured STT endpoint over the attachment
// bytes and returns the transcribed text. Refuses cleanly when
// transcription isn't enabled so the UI can surface a helpful error
// (rather than silently dropping the upload). All paths log so the
// user can see the route in the debug log when troubleshooting
// "why didn't my audio file get transcribed?"
func extractAudio(ctx context.Context, doc DocumentAttachment) (string, error) {
	cfg := GetTranscribeConfig()
	if !cfg.Enabled {
		nfo.Log("[transcribe] refused %q (%d bytes mime=%q): transcription disabled — configure via `gohort --setup`",
			doc.Name, len(doc.Data), doc.MimeType)
		return "", fmt.Errorf("audio attachments need transcription enabled — configure via `gohort --setup` (Audio transcription section)")
	}
	name := strings.TrimSpace(doc.Name)
	if name == "" {
		name = "audio.mp3"
	}
	nfo.Log("[transcribe] sending %q (%d bytes mime=%q) to %s", name, len(doc.Data), doc.MimeType, cfg.Endpoint)
	text, err := Transcribe(ctx, doc.Data, name)
	if err != nil {
		nfo.Log("[transcribe] %q failed: %v", name, err)
		return "", fmt.Errorf("transcribe failed: %w", err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		nfo.Log("[transcribe] %q returned empty text (no speech detected)", name)
		return "", fmt.Errorf("transcription returned empty text (clip may have no speech)")
	}
	nfo.Log("[transcribe] %q → %d chars", name, len(text))
	return text, nil
}

// extractPDF runs `pdftotext` (poppler-utils) against the bytes via
// stdin / stdout, avoiding a tempfile. -layout preserves rough column
// structure which generally reads better than the default tight stream.
func extractPDF(ctx context.Context, data []byte) (string, error) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return "", fmt.Errorf("pdftotext not installed (poppler-utils): %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, DocumentExtractTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", "-", "-")
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftotext failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// extractWithPandoc runs pandoc with the given input format,
// outputting plain text. pandoc reads docx/doc directly from a path,
// so we drop the data to a temp file. The file lives in the OS temp
// dir and is removed on return; pandoc is sandboxed enough to not
// need an additional layer here (extraction is read-only).
func extractWithPandoc(ctx context.Context, data []byte, inputFmt string) (string, error) {
	if _, err := exec.LookPath("pandoc"); err != nil {
		return "", fmt.Errorf("pandoc not installed: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, DocumentExtractTimeout())
	defer cancel()
	tmp, err := os.CreateTemp("", "gohort-doc-*."+inputFmt)
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write temp: %w", err)
	}
	tmp.Close()
	cmd := exec.CommandContext(ctx, "pandoc", "-f", inputFmt, "-t", "plain", tmp.Name())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pandoc failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// extractWithAntiword runs `antiword` against a legacy binary Word
// (.doc) file, outputting plain text. antiword reads a file path (not
// stdin reliably), so we drop the bytes to a temp file mirroring
// extractWithPandoc. `-w 0` disables line wrapping so paragraphs come
// through as unbroken lines — better for embedding/search than
// column-wrapped output. pandoc cannot read this format, which is why
// .doc gets its own extractor.
func extractWithAntiword(ctx context.Context, data []byte) (string, error) {
	if _, err := exec.LookPath("antiword"); err != nil {
		return "", fmt.Errorf("antiword not installed (needed for legacy .doc files; convert to .docx or install antiword): %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, DocumentExtractTimeout())
	defer cancel()
	tmp, err := os.CreateTemp("", "gohort-doc-*.doc")
	if err != nil {
		return "", fmt.Errorf("temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write temp: %w", err)
	}
	tmp.Close()
	cmd := exec.CommandContext(ctx, "antiword", "-w", "0", tmp.Name())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("antiword failed: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// FormatDocumentPreamble builds the "Attached: name\n\n<text>" block
// that callers prepend to the user message so the LLM sees the
// document content alongside the question. Header is picked from the
// attachment's name/mime so audio attachments come in labeled as
// transcripts — without that distinction the LLM tends to look at an
// .mp3 filename, ignore the transcript text below, and reach for a
// transcription tool that's redundant work.
//
// Pass mime as "" if you only have the filename — the helper still
// picks the right header from the extension.
//
// Empty text returns empty string; the caller decides what to do.
func FormatDocumentPreamble(name, text string) string {
	return FormatAttachmentPreamble(name, "", text)
}

// FormatAttachmentPreamble is the mime-aware variant. Audio mime
// types / extensions get a "Transcribed audio" header that explicitly
// tells the LLM the text below IS the complete transcript — preempts
// the "let me write a transcribe tool" loop. Other types fall back
// to the generic document header.
func FormatAttachmentPreamble(name, mime, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	mimeLower := strings.ToLower(strings.TrimSpace(mime))
	ext := strings.ToLower(filepath.Ext(name))
	var b strings.Builder
	if isAudioAttachment(mimeLower, ext) {
		fmt.Fprintf(&b, "## Transcribed audio: %s\n\n", name)
		b.WriteString("The text below is the COMPLETE transcript of the attached audio file (already transcribed via whisper). When asked to transcribe, return the transcript verbatim — NO meta preamble (no \"the transcription reads:\", no \"here is what it says:\", no parenthetical attribution like \"(that's the full content)\"). Just deliver the words directly. When answering other questions about the audio, cite the transcript inline as needed. Do NOT try to transcribe it yourself; the work is done.\n\n")
		b.WriteString(text)
		b.WriteString("\n\n---\n\n")
		return b.String()
	}
	fmt.Fprintf(&b, "## Attached document: %s\n\n", name)
	b.WriteString(text)
	b.WriteString("\n\n---\n\n")
	return b.String()
}
