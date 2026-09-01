package ui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A component field the runtime never reads is a promise core/ui cannot keep.
// The app author declares it in Go, it compiles, it marshals into the page, and
// nothing happens — with no error to chase, because every layer did its job
// except the last one. That is the most expensive shape a bug can have here,
// and it is invisible to the compiler on both sides: Go cannot see the
// JavaScript, and the JavaScript cannot see the struct.
//
// This is what caught the stranded Suggest-name button — an app serving a live
// LLM endpoint that the panel never rendered a control for — along with three
// fields documenting behavior nothing implemented.
//
// Matching is deliberately loose (substring, any quoting). A field that slips
// through because its name appears incidentally is a miss; a field wrongly
// flagged would be a broken build. Prefer the miss.
func TestEveryComponentFieldIsReadByTheRuntime(t *testing.T) {
	src, err := os.ReadFile("components.go")
	if err != nil {
		t.Fatal(err)
	}
	js := runtimeJSSource(t)

	structRE := regexp.MustCompile(`(?ms)^type ([A-Z]\w+) struct \{(.*?)^\}`)
	tagRE := regexp.MustCompile(`json:"([a-z0-9_]+)`)

	for _, m := range structRE.FindAllStringSubmatch(string(src), -1) {
		name, body := m[1], m[2]
		for _, tm := range tagRE.FindAllStringSubmatch(body, -1) {
			tag := tm[1]
			if strings.Contains(js, `"`+tag+`"`) || strings.Contains(js, `'`+tag+`'`) || strings.Contains(js, "."+tag) {
				continue
			}
			t.Errorf("%s.%s (json:%q) is read by nothing in the runtime.\n"+
				"An app can set it, and it will silently do nothing. Either wire it up in the\n"+
				"runtime, or delete the field so no one declares it expecting behavior.",
				name, fieldNameFor(body, tag), tag)
		}
	}
}

// runtimeJSSource concatenates the runtime fragments — the runtime is one
// program split across ordered files, so a field may be read in any of them.
func runtimeJSSource(t *testing.T) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("assets", "runtime", "*.js"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no runtime JS found (%v) — did assets/runtime move?", err)
	}
	var b strings.Builder
	for _, p := range paths {
		by, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(by)
		b.WriteByte('\n')
	}
	return b.String()
}

// fieldNameFor recovers the Go field name for a json tag, so the failure names
// what the author would actually type.
func fieldNameFor(structBody, tag string) string {
	for _, line := range strings.Split(structBody, "\n") {
		if !strings.Contains(line, `json:"`+tag) {
			continue
		}
		if f := strings.Fields(strings.TrimSpace(line)); len(f) > 0 {
			return f[0]
		}
	}
	return "?"
}
