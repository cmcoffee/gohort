package service

import (
	"os"
	"strings"
	"testing"
)

// Every contract says what it is, what its check requires and where gohort
// uses it: the reference, the docs and the assistant's text are made from
// these, so a contract added without them would be explained as nothing.
func TestEveryContractIsExplained(t *testing.T) {
	for _, c := range ContractInfos() {
		for field, v := range map[string]string{"About": c.About, "Checks": c.Checks, "Where": c.Where} {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s has no %s", c.Name, field)
			}
			if strings.Contains(v, "—") {
				t.Errorf("%s's %s has an em-dash", c.Name, field)
			}
		}
		if !strings.HasSuffix(c.About, ".") || !strings.HasSuffix(c.Checks, ".") {
			t.Errorf("%s: About and Checks are sentences: %q %q", c.Name, c.About, c.Checks)
		}
	}
}

// What an assistant writing or fixing a spec is told names the contract's
// own check, not another's.
func TestContractTextSaysItsOwnCheck(t *testing.T) {
	search := ContractText("web.search")
	if !strings.Contains(search, "query (text, required)") || !strings.Contains(search, "gives results (json)") ||
		!strings.Contains(search, "http or https") || strings.Contains(search, "PNG") {
		t.Errorf("web.search: %s", search)
	}
	if img := ContractText("image.generate"); !strings.Contains(img, "PNG, JPEG, GIF or WebP") {
		t.Errorf("image.generate: %s", img)
	}
	if voices := ContractText("speech.voices"); !strings.Contains(voices, "takes nothing") {
		t.Errorf("speech.voices: %s", voices)
	}
}

const docsPath = "../../docs/integrations.md"

// The docs' table of contracts is the registry's: a contract added, or a
// check changed, without the docs following fails here. UPDATE_DOCS=1
// rewrites the table.
func TestTheDocsListTheContracts(t *testing.T) {
	raw, err := os.ReadFile(docsPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	open := strings.Index(doc, "<!-- contracts:")
	end := strings.Index(doc, "<!-- end contracts -->")
	if open < 0 || end < open {
		t.Fatal("docs/integrations.md has no contracts markers")
	}
	from := open + strings.Index(doc[open:], "-->\n") + len("-->\n")
	want := ContractsMarkdown()
	if doc[from:end] == want {
		return
	}
	if os.Getenv("UPDATE_DOCS") != "" {
		if err := os.WriteFile(docsPath, []byte(doc[:from]+want+doc[end:]), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Errorf("docs/integrations.md's contracts table is not the registry's: run UPDATE_DOCS=1 go test ./core/service -run TestTheDocsListTheContracts")
}
