package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Source-level sweep over the descriptions of BUILT-IN tools.
//
// LLM-authored tools are capped at authoring time (CheckAuthoredToolText);
// nothing capped ours, and they drifted — the worst was 2.2k characters
// re-sent on every turn for a spec that action="help" already returned in
// full. A description is prompt we pay for on every request forever, so it
// gets a ceiling like any other budget.
//
// This is a RATCHET, not a target. Most built-ins sit far below it; the
// ceiling exists so the next one that wants to be an essay has to argue for
// it out loud. Grouped tools that document several actions inline are why it
// is well above the 500 LLM-authored cap.
//
// Crude on purpose, in the style of the enum sweep next door: it matches the
// declaration form the codebase actually uses (a snake_case Name: followed by
// Description:), and a false positive is a one-line fix.
const maxBuiltinToolDescription = 1200

func TestBuiltinToolDescriptionsStayWithinBudget(t *testing.T) {
	decl := regexp.MustCompile(`Name:\s*"([a-z][a-z0-9_]*)"\s*,\s*(?:\n\s*)*Description:\s*"((?:[^"\\]|\\.)*)"`)

	type offender struct {
		name, path string
		n          int
	}
	var offenders []offender
	total, count := 0, 0

	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "build", "@eaDir":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			// Length as the model sees it: the literal is Go-escaped source,
			// so \n and \" count double here. Close enough for a ratchet.
			n := len([]rune(m[2]))
			total += n
			count++
			if n > maxBuiltinToolDescription {
				offenders = append(offenders, offender{m[1], path, n})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if count == 0 {
		t.Fatal("matched no tool declarations at all — the sweep regex has drifted from the codebase and is silently passing")
	}
	t.Logf("%d built-in tool descriptions, %d chars total, %d average", count, total, total/count)

	if len(offenders) == 0 {
		return
	}
	sort.Slice(offenders, func(i, j int) bool { return offenders[i].n > offenders[j].n })
	var b strings.Builder
	for _, o := range offenders {
		fmt.Fprintf(&b, "\n  %-28s %5d chars  %s", o.name, o.n, o.path)
	}
	t.Errorf("tool description(s) past the %d-character ceiling:%s\n\n"+
		"This text is re-sent on every turn the tool is in the catalog, forever. Move the detail "+
		"to where it is read only when needed: an action=\"help\" spec, the param descriptions, or "+
		"the error the tool returns when the rule is broken. If the length is genuinely earned, "+
		"raise maxBuiltinToolDescription in the same commit and say why.",
		maxBuiltinToolDescription, b.String())
}
