package orchestrate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Save-time syntax check for an html section's inline JavaScript.
//
// An app_def update accepted whatever markup it was handed and reported
// success, so a game whose script had one stray `break` saved clean, went
// live broken, and said nothing. The only thing that could tell you otherwise
// was action=verify — a SEPARATE call, against whatever revision happened to
// be stored when it ran, which an author working in batches routinely fires
// against the copy it is in the middle of replacing. The observed result is a
// loop: update, verify the previous revision, "fix" the error the report named,
// update again, never converge.
//
// Parsing is not a browser's job here. `node --check` decides in milliseconds
// what a headless page load takes seconds to discover, so the answer arrives
// attached to the write that caused it. Same posture as the data-source
// auto-check that already runs on save, and the same posture as tool_def's
// scriptSyntaxCheck: only ever accuse when the checker's own output says
// SYNTAX. A missing node, a refused sandbox, a timeout — all yield no verdict
// rather than sending an author to rewrite code that was fine.

// scriptBlockRE captures the body of each inline <script> element. Deliberately
// simple: an html section is authored markup, not adversarial input, and the
// worst case of a mis-match is a block that doesn't get checked.
var scriptBlockRE = regexp.MustCompile(`(?is)<script([^>]*)>(.*?)</script\s*>`)

// scriptTypeRE pulls the type attribute so non-JavaScript blocks (JSON data,
// importmaps, x-templates) aren't fed to a JavaScript parser.
var scriptTypeRE = regexp.MustCompile(`(?i)\btype\s*=\s*["']?([^"'\s>]+)`)

// htmlScriptSyntaxProblems parses every inline script in an html blob and
// returns one line per block that fails to parse. An empty result means either
// "everything parses" or "no verdict was reachable" — checked reports which,
// so a caller never presents an unchecked save as a verified one.
func htmlScriptSyntaxProblems(ctx context.Context, html string) (problems []string, checked bool) {
	blocks := scriptBlockRE.FindAllStringSubmatch(html, -1)
	if len(blocks) == 0 {
		return nil, false
	}
	// Prefer the sandbox dispatch dir (same ground tool_def's syntax check
	// stands on). It needs the runtime workspaces dir, which a non-serving
	// process doesn't have — fall back to an ordinary temp dir so the check
	// still reaches a verdict rather than silently passing everything.
	dir, err := MintToolDispatchDir("appjs-")
	if err != nil {
		if dir, err = os.MkdirTemp("", "appjs-"); err != nil {
			return nil, false
		}
	}
	defer func() { _ = os.RemoveAll(dir) }()

	index := 0
	for _, m := range blocks {
		attrs, body := m[1], m[2]
		if strings.TrimSpace(body) == "" {
			continue
		}
		// An external script has no body to parse here; a typed block is only
		// JavaScript when it says so (or says nothing).
		if strings.Contains(strings.ToLower(attrs), " src=") {
			continue
		}
		if t := scriptTypeRE.FindStringSubmatch(attrs); t != nil {
			switch strings.ToLower(t[1]) {
			case "", "text/javascript", "application/javascript", "module":
			default:
				continue
			}
		}
		index++
		problem, ok := jsSyntaxProblem(ctx, dir, index, body)
		if !ok {
			// No verdict on this block — and therefore none for the section.
			// Better to say nothing than to half-check.
			return problems, false
		}
		if problem != "" {
			problems = append(problems, fmt.Sprintf("script block %d: %s", index, problem))
		}
	}
	return problems, true
}

// jsSyntaxProblem runs one script body through node --check inside the sandbox.
// Returns the problem (empty when it parses) and whether a verdict was reached.
func jsSyntaxProblem(ctx context.Context, dir string, index int, body string) (string, bool) {
	path := filepath.Join(dir, fmt.Sprintf("block%d.js", index))
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		return "", false
	}
	// Derived from the caller's context, not Background, so the admin stamp it
	// carries survives down to the sandbox. Without it an admin authoring an
	// app on a host that cannot confine gets "no verdict" from the very check
	// meant to catch their typo.
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Direct exec first. `node --check` READS and PARSES the file and never
	// executes it, so there is nothing for a sandbox to contain — unlike the
	// data-source scripts, which really do run and stay confined. Running it
	// directly also makes the EXIT CODE trustworthy: --check fails only when
	// the file doesn't parse, which is a far better verdict than pattern-
	// matching the message. (Message matching got this wrong twice: node 10
	// calls an invalid assignment target a ReferenceError, not a SyntaxError.)
	if problem, ok := nodeCheckDirect(ctx, path); ok {
		return problem, true
	}
	// No node on PATH — try the sandbox, where the exit code can also mean
	// "the sandbox refused", so fall back to reading the message.
	res := RunSandboxedShell(ctx, "node --check "+appShellQuote(path), dir)
	if res.Err == nil && !res.TimedOut {
		return "", true
	}
	if out := strings.TrimSpace(res.Output); syntaxVerdict(out) {
		return appOneLine(nodeCheckDetail(out), 300), true
	}
	// Non-zero for some other reason (node absent, sandbox refused). Not the
	// author's bug — no verdict.
	return "", false
}

// syntaxVerdict reports whether a checker's output is complaining about the
// SOURCE, as opposed to failing for its own reasons. Only consulted on the
// sandbox path, where a non-zero exit is ambiguous. The whole check hangs on
// this distinction: a missing interpreter must never read as "your code is
// broken".
func syntaxVerdict(out string) bool {
	low := strings.ToLower(out)
	for _, marker := range []string{"syntaxerror", "syntax error", "referenceerror", "unexpected", "unterminated", "invalid", "illegal"} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// nodeCheckDirect parses the file with the local node. Returns the problem
// (empty when it parses) and whether node could be reached at all.
//
// Depth follows the local node's V8: every version rejects unbalanced braces,
// bad tokens and invalid assignment targets, while a stray `break` outside a
// loop is only an early error on newer ones. What this misses, the page check
// on save still catches — this is the fast first pass, not the whole gate.
func nodeCheckDirect(ctx context.Context, path string) (string, bool) {
	bin, err := exec.LookPath("node")
	if err != nil {
		return "", false
	}
	out, err := exec.CommandContext(ctx, bin, "--check", path).CombinedOutput()
	if ctx.Err() != nil {
		return "", false // timed out — no verdict
	}
	if err == nil {
		return "", true
	}
	if _, isExit := err.(*exec.ExitError); !isExit {
		return "", false // couldn't run it at all
	}
	return appOneLine(nodeCheckDetail(strings.TrimSpace(string(out))), 300), true
}

// nodeCheckDetail trims node's report to the part an author can act on: the
// offending source line, the caret, and the error itself. The full output
// leads with a temp path and trails a stack, neither of which means anything
// to someone editing an html section.
func nodeCheckDetail(out string) string {
	lines := strings.Split(out, "\n")
	var kept []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "at ") || strings.HasPrefix(t, "Node.js v") {
			continue
		}
		// Drop the leading temp-file path but keep its line:col suffix, which
		// is the one navigational fact worth having.
		if i := strings.Index(t, "block"); i >= 0 && strings.Contains(t, ".js:") {
			if j := strings.Index(t[i:], ".js:"); j >= 0 {
				t = "line " + t[i+j+4:]
			}
		}
		kept = append(kept, t)
		if len(kept) >= 4 {
			break
		}
	}
	if len(kept) == 0 {
		return out
	}
	return strings.Join(kept, " | ")
}

// appPageRuntimeErrors loads a just-saved app in the headless browser and
// returns the JS failures it hit. Only the fatal shapes — uncaught exceptions
// and console errors — because this runs on every save of an html-section app
// and a noisy gate teaches an author to ignore it.
//
// Returns nothing when no browser is available in the build: an unavailable
// checker must never read as a clean bill of health, and the caller only ever
// uses a non-empty result to REFUSE, never an empty one to bless.
func appPageRuntimeErrors(user, slug string) []string {
	if BrowserCheckPage == nil {
		return nil
	}
	rep, err := CheckPageAsUser(RootDB, user, "/custom/"+slug+"/", "")
	if err != nil || rep == nil {
		return nil
	}
	var out []string
	for _, e := range rep.PageErrors {
		out = append(out, "uncaught JS exception — "+appOneLine(e, 300))
	}
	for _, e := range rep.ConsoleErrors {
		out = append(out, "console error — "+appOneLine(e, 300))
	}
	if len(out) > 6 {
		out = append(out[:6], fmt.Sprintf("…and %d more", len(out)-6))
	}
	return out
}

// appHTMLSectionScripts collects the html blobs an authored section array
// carries, so the caller can check them without re-walking the page.
func appHTMLSectionScripts(raw any) []string {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		m = normalizeSection(m)
		if strings.EqualFold(strings.TrimSpace(mapStr(m, "kind")), "html") {
			if html := mapStr(m, "html"); strings.TrimSpace(html) != "" {
				out = append(out, html)
			}
		}
	}
	return out
}

// appShellQuote wraps a path for the sandbox shell.
func appShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// appOneLine flattens and caps a multi-line checker report.
func appOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
