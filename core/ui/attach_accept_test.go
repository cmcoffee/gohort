package ui

// The agent chat's paperclip takes every document the server reads.
//
// Its list drifted from the extractor's: .log, .json, .csv, .yaml and .html
// were read fine by the server and could not be picked, because the picker
// only had .txt, .md and text/*, and text/* matches only a file the operating
// system has a type for. A picker narrower than the extractor is invisible
// from the code that causes it, so this holds the one to the other.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/media"
)

func TestThePaperclipTakesEveryDocumentTheServerReads(t *testing.T) {
	raw, err := os.ReadFile("assets/runtime/30_agent_loop_panel.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`var attachAccept = ((?:'[^']*'\s*\+?\s*)+);`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("could not find the paperclip's accept list in the runtime")
	}
	var list strings.Builder
	for _, part := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(m[1], -1) {
		list.WriteString(part[1])
	}
	have := map[string]bool{}
	for _, a := range strings.Split(list.String(), ",") {
		have[strings.TrimSpace(a)] = true
	}
	for _, want := range strings.Split(media.DocumentAcceptAttr, ",") {
		if w := strings.TrimSpace(want); w != "" && !have[w] {
			t.Errorf("the server reads %s but the paperclip will not pick it", w)
		}
	}
	if !have["image/*"] {
		t.Error("the paperclip no longer takes pictures")
	}
}
