package orchestrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// JSON frontmatter, shared by the two embedded markdown libraries in this
// package: the built-in agents under builtin/ and the agent shapes under
// archetypes/. Both are documents with a header of settings, and both want the
// same two things from the header: keys that ARE the struct's keys, and a
// misspelled key that stops the build instead of silently dropping a setting.
//
// A document is a fenced JSON object followed by markdown:
//
//	---
//	{ "summary": "..." }
//	---
//	# The document
//
// JSON rather than YAML because the structs these decode into already carry
// json tags, and because a YAML parser would be a new external dependency in a
// tree that also builds in GOPATH mode.

const frontmatterFence = "---"

// libraryReadmeName is the one filename in either library that is not a
// document: it explains the format to whoever opens the directory next. Every
// other .md in those directories has to parse.
const libraryReadmeName = "README.md"

// splitFrontmatter separates the leading fenced JSON block from the body.
func splitFrontmatter(data []byte) ([]byte, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, frontmatterFence+"\n") {
		return nil, "", fmt.Errorf("file must start with a %q frontmatter fence", frontmatterFence)
	}
	rest := text[len(frontmatterFence)+1:]
	end := strings.Index(rest, "\n"+frontmatterFence+"\n")
	if end < 0 {
		return nil, "", fmt.Errorf("frontmatter is never closed by a %q line", frontmatterFence)
	}
	front := rest[:end+1]
	body := rest[end+len(frontmatterFence)+2:]
	return []byte(front), strings.TrimRight(body, "\n"), nil
}

// decodeFrontmatter decodes a frontmatter block strictly. An unknown key is an
// error: dropped silently, it would leave a document plainly asking for
// something the loader never applied.
func decodeFrontmatter(front []byte, into any) error {
	dec := json.NewDecoder(bytes.NewReader(front))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return fmt.Errorf("frontmatter: %v", err)
	}
	return nil
}
