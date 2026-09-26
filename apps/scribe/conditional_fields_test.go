package scribe

import (
	"os"
	"strings"
	"testing"
)

// A template is a starting body for an article; a guide ignores it. The New
// form opens on Guide, so the field starts hidden and appears for Article.
func TestTheTemplatePickerIsForArticlesOnly(t *testing.T) {
	raw, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatalf("reading the source: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, `{Value: "`+KindArticle+`", Label: "Article"`) {
		t.Fatal("the New form no longer offers the article kind under its stored value")
	}
	idx := strings.Index(src, `{Field: "template"`)
	if idx < 0 {
		t.Fatal("the template field is gone from the New form")
	}
	line := src[idx:]
	if end := strings.Index(line, "\n"); end >= 0 {
		line = line[:end]
	}
	if !strings.Contains(line, `ShowWhen: "kind:`+KindArticle+`"`) {
		t.Errorf("the template field must show only for articles:\n%s", line)
	}
}
