package scribe

import (
	"os"
	"strings"
	"testing"
)

// A template is a starting skeleton for any new guide, so the New form offers
// it unconditionally. It used to show only for the article kind, which no
// longer exists; a leftover kind picker or condition would hide it again.
func TestTheTemplatePickerIsForEveryGuide(t *testing.T) {
	raw, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, `{Field: "kind"`) {
		t.Error("the New form still asks for a kind; Scribe has one kind of document")
	}
	idx := strings.Index(src, `{Field: "template"`)
	if idx < 0 {
		t.Fatal("the template field is gone from the New form")
	}
	line := src[idx:]
	if end := strings.Index(line, "\n"); end >= 0 {
		line = line[:end]
	}
	if strings.Contains(line, "ShowWhen") {
		t.Errorf("the template field should show for every new guide:\n%s", line)
	}
}
