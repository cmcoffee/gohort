// THIRD_PARTY_NOTICES for a release binary.
//
// A compiled gohort contains the object code of every module it links, and
// each of those arrives with terms that ask for their notice to travel with it.
// The source tree does not need this file — the dependencies are not in it, and
// NOTICE says so — but a downloaded binary is a redistribution of all of them at
// once, with nothing in the box saying whose work it is.
//
// It reads what was ACTUALLY LINKED (go list -deps on the built package), not
// what go.mod requires: a requirement can be present and unused, and listing an
// unused module is as wrong as omitting a used one, just in the harmless
// direction. Test-only and tool dependencies never appear for the same reason.
//
// IT FAILS RATHER THAN GUESSING. A module whose license file cannot be found
// stops the build with its name. The failure mode this exists to prevent is a
// notices file that looks complete and quietly is not, and a missing license is
// exactly the case where a human has to go and look.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// licenseNames are the files a module might keep its terms in, in the order we
// prefer them. A module offering several (a dual-licensed one) contributes all
// of them — picking one for the reader would be choosing on their behalf.
var licenseNames = []string{
	"LICENSE", "LICENSE.md", "LICENSE.txt",
	"LICENCE", "LICENCE.md", "LICENCE.txt",
	"COPYING", "COPYING.md", "COPYING.txt",
	"LICENSE-APACHE", "LICENSE-MIT", "LICENSE-BSD",
	"LICENSE.APACHE2", "LICENSE.MIT",
}

// mod is the slice of `go list -deps -json` we need.
type mod struct {
	Path    string
	Version string
	Dir     string
	Main    bool
	Replace *mod
}

type pkg struct {
	Standard bool
	Module   *mod
}

func main() {
	out := flag.String("out", "THIRD_PARTY_NOTICES", "file to write")
	target := flag.String("pkg", ".", "package whose linked dependencies to list")
	flag.Parse()

	// The module being BUILT is the only one to skip. Not "any main module":
	// under a go.work every workspace member is main, so building inside one
	// silently dropped snugforge from the list — a dependency with its own
	// copyright and its own license, absent because of how this machine
	// happens to be set up. The notices a binary carries must not depend on
	// that.
	self, err := moduleOf(*target)
	if err != nil {
		fail("resolving the module being built: %v", err)
	}
	mods, err := linkedModules(*target, self)
	if err != nil {
		fail("listing dependencies: %v", err)
	}
	if len(mods) == 0 {
		fail("no dependencies found for %s — that is almost certainly a broken invocation rather than a dependency-free build", *target)
	}

	var b strings.Builder
	b.WriteString(header)
	var missing []string
	for _, m := range mods {
		texts := licenseTexts(m.Dir)
		if len(texts) == 0 {
			missing = append(missing, m.Path+" ("+m.Dir+")")
			continue
		}
		b.WriteString(entry(m, texts))
	}
	// Reported together and last: fixing them one build at a time, each after a
	// full compile, is how a five-minute job becomes an afternoon.
	if len(missing) > 0 {
		fail("no license file found for %d module(s) — find their terms and add the filename to licenseNames, or vendor the license beside them:\n  %s",
			len(missing), strings.Join(missing, "\n  "))
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fail("writing %s: %v", *out, err)
	}
	fmt.Fprintf(os.Stderr, "%s: %d module(s)\n", *out, len(mods))
}

// linkedModules returns every non-stdlib module contributing code to target,
// deduped by path and sorted, with replacements resolved to what will actually
// be compiled.
// moduleOf is the module path of the package being built.
func moduleOf(target string) (string, error) {
	cmd := exec.Command("go", "list", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", target)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func linkedModules(target, self string) ([]mod, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", target)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	seen := map[string]mod{}
	dec := json.NewDecoder(strings.NewReader(string(stdout)))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return nil, err
		}
		if p.Standard || p.Module == nil || p.Module.Path == self {
			continue
		}
		m := *p.Module
		// A replaced module is compiled from the replacement, so the
		// replacement is what has to be attributed — and its version, or
		// lack of one, is what the reader needs to see.
		if m.Replace != nil {
			r := *m.Replace
			r.Path = m.Path // keep the import path the reader recognises
			m = r
		}
		if m.Dir == "" {
			continue
		}
		if _, dup := seen[m.Path]; !dup {
			seen[m.Path] = m
		}
	}
	out := make([]mod, 0, len(seen))
	for _, m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// licenseTexts reads every license file a module root carries.
func licenseTexts(dir string) map[string]string {
	out := map[string]string{}
	for _, name := range licenseNames {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			out[name] = s
		}
	}
	return out
}

// entry renders one module: what it is, which version of it, and its terms in
// full. The full text, not a name and a link — a link is not a license, and
// somebody reading this offline is exactly who the file is for.
func entry(m mod, texts map[string]string) string {
	var b strings.Builder
	b.WriteString("\n" + strings.Repeat("=", 74) + "\n")
	b.WriteString(m.Path)
	switch {
	case m.Version != "":
		b.WriteString(" " + m.Version)
	default:
		// No version means a workspace or a replace pointed the build at a
		// working copy on the machine that built it. Say so: the binary
		// contains whatever was in that directory, which is not something a
		// reader can look up.
		b.WriteString(" (LOCAL WORKING COPY — not a released version; built from " + m.Dir + ")")
	}
	b.WriteString("\n" + strings.Repeat("=", 74) + "\n\n")
	names := make([]string, 0, len(texts))
	for n := range texts {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		if len(names) > 1 {
			fmt.Fprintf(&b, "--- %s ---\n\n", n)
		}
		b.WriteString(texts[n])
		b.WriteString("\n")
		if i < len(names)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "license notices: "+format+"\n", args...)
	os.Exit(1)
}

const header = `THIRD-PARTY NOTICES

This binary is built from gohort, which is licensed under the Apache License,
Version 2.0 (see LICENSE), and it links the third-party modules listed below.
Each remains under its own terms, reproduced here in full. Nothing in this file
changes gohort's license, and gohort's license changes nothing about theirs.

Generated from the modules ACTUALLY LINKED into this build. Modules that go.mod
requires but nothing imports are absent on purpose, as are test-only and build
tooling dependencies.

Third-party code that lives IN the gohort source tree, rather than being fetched
as a module, is listed in NOTICE instead.
`
