package main

// The scrub gate: nothing traceable to a real person, customer or deployment
// goes into this repo. It is PUBLIC, and a fix always starts from a real
// incident — so the real names are what is in mind while the fixture gets
// written, and they land without anybody deciding to put them there.
//
// WHY THIS IS A TEST AND NOT A HOOK. A .git/hooks script is not versioned, does
// not reach a fresh clone, and is skipped by --no-verify. A test runs for
// everyone who runs the suite and cannot be forgotten into.
//
// A SCRUB COMMIT MESSAGE IS PART OF THE LEAK. Naming what was removed, how many
// times, and which files it sat in points a reader straight at the history that
// still holds it. Keep the message boring — "use placeholder names in fixtures"
// — and put the reasoning somewhere that is not the public log.
//
// WHY THE COMMITTED CHECKS ARE STRUCTURAL. A denylist of the forbidden words
// would put those words back into the public repo — searchable, in the very
// file that exists to keep them out. So what ships here are rules with no names
// in them: an address must use a reserved domain, a path must not be somebody's
// home directory, key material must not appear at all. The deployment-specific
// name list lives in .scrub-denylist, which is gitignored; when it is absent
// these checks still run and that part is skipped.

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// scrubExts are the files worth reading: source, docs and web assets. Binary
// and data files are skipped — a match in a .png is noise.
var scrubExts = map[string]bool{
	".go": true, ".js": true, ".md": true, ".css": true, ".json": true, ".txt": true, ".html": true,
}

// scrubSkipDirs never get walked. private/ is the other repo (symlinked in and
// not public), and the rest hold no authored text.
var scrubSkipDirs = map[string]bool{
	".git": true, "private": true, "vendor": true, "node_modules": true, "data": true,
	// Third-party frontend runtime, vendored in. Its authors' addresses are
	// theirs and are not ours to rewrite — the gate is about what WE write.
	"wailsjs": true,
}

// reservedEmailDomain is what a fixture address may use: the RFC 2606 / 6761
// names reserved precisely so they can never belong to anybody, plus acme.com
// (this repo's company placeholder, already used in the graph fixtures).
//
// Any .test / .invalid / .example / .localhost domain passes, not just
// "example.*" — someone@else.test is as unownable as alice@example.test, and a
// gate that rejects a correct answer teaches people to switch it off.
var reservedEmailDomain = regexp.MustCompile(`(?i)@([A-Za-z0-9.\-]+\.)?(test|invalid|example|localhost)$|@(example\.(com|org|net)|acme\.com)$`)

// notAnEmail are shapes the loose address pattern catches that are not
// addresses at all: a protocol identifier that happens to contain "@", and the
// documented FORMAT of a field rendered as a placeholder in the editor. Both
// name nobody — the point of the gate is a real person or deployment.
var notAnEmail = regexp.MustCompile(`(?i)@thread\.tacv|@project\.iam\.gserviceaccount\.com`)

var (
	// emailRE is deliberately loose. It over-matches things like "@param" in
	// prose, which the domain check then lets through; the cost of a wide net
	// here is a rule that also catches the address nobody thought of.
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// homePathRE catches an absolute path out of somebody's machine. These
	// arrive in comments describing a real incident.
	homePathRE = regexp.MustCompile(`/(Users|home)/[a-z][a-z0-9._\-]{2,}/`)
	// keyMaterialRE is the one category that is genuinely dangerous rather
	// than merely identifying.
	keyMaterialRE = regexp.MustCompile(`AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{20,}|xox[baprs]-[A-Za-z0-9\-]{10,}|BEGIN [A-Z ]*PRIVATE KEY`)
)

// walkScrubFiles visits every file the gate applies to.
func walkScrubFiles(t *testing.T, visit func(path string, line int, text string)) {
	t.Helper()
	root := "."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // unreadable entry is not this test's business
		}
		if info.IsDir() {
			if scrubSkipDirs[info.Name()] || strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !scrubExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		// This file names the patterns it forbids, so reading itself would
		// report itself.
		if filepath.Base(path) == "scrub_test.go" {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for n := 1; sc.Scan(); n++ {
			visit(path, n, sc.Text())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// An address in this repo must be one that cannot belong to anybody. Catches a
// personal address in a fixture without this file having to name one.
func TestNoRealEmailAddresses(t *testing.T) {
	walkScrubFiles(t, func(path string, line int, text string) {
		for _, m := range emailRE.FindAllString(text, -1) {
			if reservedEmailDomain.MatchString(m) {
				continue
			}
			if notAnEmail.MatchString(m) {
				continue
			}
			// A commit-attribution line is the one address that belongs here.
			if strings.Contains(m, "noreply@anthropic.com") {
				continue
			}
			t.Errorf("%s:%d: %s is not a reserved test domain — use @example.test so it can never belong to anybody", path, line, m)
		}
	})
}

// An absolute home path is somebody's machine, and usually arrives in a comment
// describing the incident that motivated the change. The SHAPE belongs in the
// comment; the path does not.
func TestNoAbsoluteHomePaths(t *testing.T) {
	walkScrubFiles(t, func(path string, line int, text string) {
		if m := homePathRE.FindString(text); m != "" {
			t.Errorf("%s:%d: %s names a real home directory — describe the shape, not the path", path, line, m)
		}
	})
}

// The one category that is dangerous rather than merely identifying.
func TestNoKeyMaterial(t *testing.T) {
	walkScrubFiles(t, func(path string, line int, text string) {
		if m := keyMaterialRE.FindString(text); m != "" {
			t.Errorf("%s:%d: key material (%.12s…) must never be committed", path, line, m)
		}
	})
}

// Deployment-specific names — a customer, an employer's product, an internal
// tool — cannot be checked structurally, and listing them here would put them
// back in the public repo. They live one per line in .scrub-denylist, which is
// gitignored; blank lines and # comments are ignored. Absent, this skips and
// the structural gates above still run.
func TestNoDenylistedNames(t *testing.T) {
	raw, err := os.ReadFile(".scrub-denylist")
	if err != nil {
		t.Skip("no .scrub-denylist — structural checks still apply")
	}
	var terms []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			terms = append(terms, strings.ToLower(l))
		}
	}
	if len(terms) == 0 {
		t.Skip(".scrub-denylist is empty")
	}
	walkScrubFiles(t, func(path string, line int, text string) {
		low := strings.ToLower(text)
		for _, term := range terms {
			if strings.Contains(low, term) {
				// The term is NOT echoed: printing it would put it in CI logs
				// and scrollback, which is the leak this exists to stop.
				t.Errorf("%s:%d: line contains a denylisted name (entry %d in .scrub-denylist) — replace it with Acme / node-7 / @example.test",
					path, line, indexOf(terms, term)+1)
			}
		}
	})
}

func indexOf(all []string, want string) int {
	for i, s := range all {
		if s == want {
			return i
		}
	}
	return -1
}
