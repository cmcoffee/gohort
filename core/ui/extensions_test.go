package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHeadRenderEmpty(t *testing.T) {
	if got := NewHead().Render(); got != "" {
		t.Fatalf("empty Head should render \"\", got %q", got)
	}
	var nilHead *Head
	if got := nilHead.Render(); got != "" {
		t.Fatalf("nil Head should render \"\", got %q", got)
	}
}

func TestHeadRenderClientActionAndCSS(t *testing.T) {
	out := NewHead().
		CSS(".x{color:red}").
		ClientAction("myapp_do", "function(ctx){return ctx;}").
		Render()

	for _, want := range []string{
		"<style>\n.x{color:red}\n</style>",
		"function reg(){",
		"if (!window.uiRegisterClientAction)",
		`window.uiRegisterClientAction("myapp_do", function(ctx){return ctx;});`,
		"reg();",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered head missing %q\n---\n%s", want, out)
		}
	}
}

func TestHeadRenderAllRegistries(t *testing.T) {
	out := NewHead().
		BlockRenderer("blk", "function(d){return {};}").
		MarkdownExtension("function(s){return s;}").
		MessageDecorator("function(m){return m;}").
		JS("console.log('init');").
		Render()

	for _, want := range []string{
		`window.uiRegisterBlockRenderer("blk", function(d){return {};});`,
		"window.uiRegisterMarkdownExtension(function(s){return s;});",
		"window.uiRegisterMessageDecorator(function(m){return m;});",
		"console.log('init');",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered head missing %q\n---\n%s", want, out)
		}
	}
}

// A name with a quote must not break out of the JS string literal.
func TestHeadNameIsEscaped(t *testing.T) {
	out := NewHead().ClientAction(`ev"il`, "function(){}").Render()
	if strings.Contains(out, `"ev"il"`) {
		t.Fatalf("name was not escaped: %s", out)
	}
	if !strings.Contains(out, `"ev\"il"`) {
		t.Fatalf("expected JSON-escaped name, got: %s", out)
	}
}

// A literal </script inside a handler body must be neutralized so it can't
// terminate the enclosing <script> element.
func TestHeadScriptSafe(t *testing.T) {
	out := NewHead().JS(`var s = "</script>";`).Render()
	if strings.Contains(out, "</script>\";") {
		t.Fatalf("</script not neutralized: %s", out)
	}
	if !strings.Contains(out, `<\/script`) {
		t.Fatalf("expected escaped </script, got: %s", out)
	}
}

// Page.Render must emit Head output before the raw ExtraHeadHTML escape hatch.
func TestPageHeadBeforeExtraHead(t *testing.T) {
	p := Page{
		Title:         "t",
		Head:          NewHead().CSS(".a{}"),
		ExtraHeadHTML: "<!--extra-->",
	}
	rec := httptest.NewRecorder()
	if err := p.Render(rec); err != nil {
		t.Fatal(err)
	}
	out := rec.Body.String()
	iCSS := strings.Index(out, ".a{}")
	iExtra := strings.Index(out, "<!--extra-->")
	if iCSS < 0 || iExtra < 0 {
		t.Fatalf("missing head content: css=%d extra=%d", iCSS, iExtra)
	}
	if iCSS > iExtra {
		t.Fatalf("Head should render before ExtraHeadHTML (css at %d, extra at %d)", iCSS, iExtra)
	}
}
