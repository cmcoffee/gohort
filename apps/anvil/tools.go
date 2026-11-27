// The coding tool surface, bound to one project for one turn.
//
// These are the verbs a coding agent needs and gohort did not have. The
// framework already ships read_file, list_dir and search_code — all bound to
// a workspace, all read-only, and all built for an investigator answering
// questions about code rather than an assistant changing it. What was missing
// is the write half, and the write half is where the design decisions are.
//
// EDITS ARE ANCHORED, NOT ADDRESSED. edit_file replaces an exact string, and
// refuses when that string appears zero times or more than once. It does not
// take line numbers. Line numbers are stale the moment anything above them
// changes, and a model that edits three places in a file will use numbers it
// read before its own first edit — producing a write that lands in the wrong
// place and looks, in the tool result, like it worked. An anchor either
// matches what is actually there or fails loudly.
//
// WHOLE-FILE WRITES ARE FOR NEW FILES ONLY. create_file refuses to overwrite.
// The failure mode it prevents is the expensive one: a model asked to change
// one function re-emits the whole file from memory, and everything it did not
// happen to recall — a helper, a comment, an import — is gone, with a diff
// that reads as a rewrite rather than a mistake. To change an existing file
// you anchor into it.
//
// EVERY WRITE IS JOURNALED IN THE SAME CALL THAT MAKES IT. Not afterwards,
// not by the caller: a write whose pre-image was captured separately can lose
// the race and become unrevertable exactly when something has gone wrong. See
// edits.go for why the undo cannot just be git.
package anvil

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/bgrun"
	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/gohort/core/toolscript"
)

// Limits. These are deliberately per-call rather than configurable: they
// exist to keep one tool result from swallowing the context window, and an
// operator raising them would be trading away the agent's ability to think
// for the ability to read one more file at a time.
const (
	maxReadBytes    = 256 << 10 // a single read_file result
	maxSearchHits   = 200
	maxListEntries  = 500
	maxRunOutput    = 20000
	maxWriteBytes   = maxJournalBytes
	defaultReadTail = 2000 // lines returned when no range is asked for
)

// skipDirs are never walked by search or list. They are the directories whose
// contents are machine-produced or enormous, and whose presence in a search
// result is noise that pushes the real hit off the end.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".idea": true, ".vscode": true, "__pycache__": true,
	".venv": true, "venv": true, "dist": true, "build": true,
	".next": true, "target": true, "@eaDir": true,
}

// toolCtx is everything the tools need to act, resolved once per turn.
type toolCtx struct {
	udb       Database
	user      string
	isAdmin   bool
	project   Project
	base      string
	sessionID string
	// emit publishes a UI card into the live turn. Nil when the caller has
	// no live stream (a headless run), in which case the tools degrade to
	// their text results — which is why the text result always says what
	// happened rather than deferring to the card.
	emit func(kind string, e EditEntry)
	// readOnly withholds every mutating tool. Set for a caller who may open
	// the project but not change it.
	readOnly bool
	// planning is plan mode, as in Claude Code: the assistant reads,
	// searches, keeps its plan and runs the project's check, and proposes a
	// change rather than making it. Edits, commands and the project's actions
	// are withheld for the turn (planmode.go).
	planning bool
	// noScript leaves tool_script out (a measure run comparing without it).
	noScript bool

	// deps are the linked projects this caller may read, resolved once per
	// turn. Never stored: a link's usability depends on who is asking.
	deps []resolvedDep

	// ignore is git's answer to "which paths under here don't count",
	// resolved once per turn. Behind a sync.Once because tools in one batch
	// run concurrently and the answer costs a subprocess.
	ignoreOnce sync.Once
	ignoreSet  *ignoreSet

	// Where the project sits in its git repository, asked once per turn
	// (run_changes.go).
	prefixOnce sync.Once
	prefix     string
	inRepo     bool

	// emitBlock publishes any other card (a check run) into the live turn.
	// Nil for a headless run.
	emitBlock func(agenthost.Block)
	// The check's state for this turn (check.go): whether the tree changed
	// since the check last passed, the turn's context from the latest tool
	// call, and a counter naming each run's card.
	checkMu          sync.Mutex
	editedSinceCheck bool
	turnCtx          context.Context
	checkSeq         int
	// The turn's plan (plan.go), under checkMu; planSeq names its card, so
	// a new plan gets a card of its own and the old one stays as it was.
	plan    []planStep
	planSeq int
}

// alwaysShow are files that stay visible even when git ignores them.
//
// A project's instructions to whoever is editing it are the one category where
// git's answer is the wrong one for this purpose. gohort's own CLAUDE.md is
// gitignored — reasonably, it is local guidance rather than published source —
// and honoring that would hide, from a coding assistant, the file whose entire
// job is telling a coding assistant how this codebase works.
//
// Matched on the base NAME, so a nested one is found too. Deliberately a short
// list of conventional names rather than a pattern: the carve-out is only
// defensible while it stays this specific.
var alwaysShow = map[string]bool{
	"CLAUDE.md": true, "AGENTS.md": true, "CONVENTIONS.md": true,
}

// ignoreSet is the resolved ignore oracle for one project directory.
type ignoreSet struct {
	// fromGit distinguishes "git told us these are ignored" from "this is not
	// a repository, so fall back to the built-in list". The two need different
	// behavior: git's answer REPLACES the built-in guesses, because a repo
	// that deliberately tracks its vendor/ directory means it.
	fromGit bool
	files   map[string]bool
	dirs    []string
}

// ignores reports whether a project-relative path should be left out of a
// listing or a search.
//
// Note what this is NOT used for: an explicit read_file. A path the user or
// the model names directly is fetched even when git ignores it — gohort's own
// CLAUDE.md is gitignored, and a tool that refused to read a file you asked
// for by name because of an ignore rule would be obeying the wrong instruction.
// Ignores shape what gets OFFERED, not what may be opened.
func (s *ignoreSet) ignores(rel string) bool {
	if s == nil {
		return false
	}
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "./"))
	if rel == "" || rel == "." {
		return false
	}
	if alwaysShow[path.Base(rel)] {
		return false
	}
	if s.files[rel] {
		return true
	}
	for _, d := range s.dirs {
		if rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	if s.fromGit {
		return false
	}
	// No repository: fall back to the built-in guesses, matched on any path
	// SEGMENT so a nested node_modules is skipped as readily as a top-level one.
	for _, seg := range strings.Split(rel, "/") {
		if skipDirs[seg] {
			return true
		}
	}
	return false
}

// ignores resolves the oracle on first use and asks it.
func (c *toolCtx) ignores(rel string) bool {
	c.ignoreOnce.Do(func() {
		files, dirs := ignoredEntries(c.base)
		c.ignoreSet = &ignoreSet{fromGit: isGitRepo(c.base), files: files, dirs: dirs}
	})
	return c.ignoreSet.ignores(rel)
}

// codeTools builds the per-turn tool set.
func (c *toolCtx) codeTools() []AgentToolDef {
	reads := []AgentToolDef{
		c.listFilesTool(),
		c.readFileTool(),
		c.searchCodeTool(),
		c.gitTool(),
	}
	tools := append([]AgentToolDef(nil), reads...)
	// A chain of reads in one round (docs/design/code-mode.md).
	if !c.noScript {
		if script, err := toolscript.New(reads...); err == nil {
			tools = append(tools, script)
		} else {
			Log("[anvil] tool_script not offered: %v", err)
		}
	}
	writes := !c.readOnly && !c.planning
	// Everything that changes something refuses while a revert has halted
	// the session (halt.go).
	if writes {
		tools = append(tools,
			c.haltable(c.editFileTool()),
			c.haltable(c.createFileTool()),
			c.haltable(c.deleteFileTool()),
			c.haltable(c.runTool()),
		)
	}
	if !c.readOnly {
		tools = append(tools, c.planTool())
	}
	if c.project.Source == sourceApp && writes {
		tools = append(tools, c.haltable(c.previewTool()), c.installMessageTool())
	}
	// A checkout's work is committed by the assistant, as the person
	// (commit.go); an app's commits are its installs.
	if writes && commitRefusal(c.project, c.user, c.isAdmin) == "" {
		tools = append(tools, c.haltable(c.commitTool()))
	}
	if c.project.Source == sourceApp || holdsAppFolder(c.base) {
		tools = append(tools, c.appChecksTool(), c.tryPageTool())
	}
	// What the installed app did in real use (app_runs.go).
	if c.project.Source == sourceApp {
		tools = append(tools, c.appRunsTool())
	}
	// A gohort app in the project, or one to start: how its folder is built.
	if c.project.Source == sourceApp || holdsAppFolder(c.base) {
		tools = append(tools, c.appGuideTool())
	} else if writes && c.project.Kind == KindClone {
		tools = append(tools, c.haltable(c.startAppTool()))
	}
	if c.hasCheck() {
		tools = append(tools, c.checkTool())
	}
	if !c.planning {
		for _, a := range c.actionTools() {
			tools = append(tools, c.haltable(a))
		}
	}
	// Every call leaves the turn's context with the check, which runs as the
	// turn ends and has no context of its own.
	for i := range tools {
		h := tools[i].Handler
		if h == nil {
			continue
		}
		tools[i].Handler = func(ctx context.Context, args map[string]any) (string, error) {
			c.noteCtx(ctx)
			return h(ctx, args)
		}
	}
	return tools
}

// --- reading -----------------------------------------------------------------

func (c *toolCtx) listFilesTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "list_files",
			Description: "List the entries in a directory of the project, marking which are directories. Use \".\" for the project root. Build directories, .git, node_modules and vendor are skipped. Start here when you don't know the layout yet: it is much cheaper than reading files to find out where things are. " +
				"To FIND a file by name anywhere in the project and its linked dependencies, pass name instead (\"esb.tgz\", \"*.tgz\", \"**/config/*.yaml\"): it searches every file, binary and git-ignored ones too. Never search outside the project (find /, ~): everything you work on is here.",
			Parameters: map[string]ToolParam{
				"path": {Type: "string", Description: "Project-relative directory path. Default \".\" (the project root)."},
				"name": {Type: "string", Description: "Optional: a file name or glob to find anywhere in the project and its linked dependencies, in place of listing one directory. A pattern without / matches a file's name at any depth."},
			},
			Caps: []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if name := strings.TrimSpace(strArg(args, "name")); name != "" {
				return c.findByName(name), nil
			}
			rel := strArg(args, "path")
			if rel == "" {
				rel = "."
			}
			t, err := c.resolve(rel)
			if err != nil {
				return "", err
			}
			abs := t.abs
			entries, err := os.ReadDir(abs)
			if err != nil {
				return "", fmt.Errorf("list %s: %w", rel, err)
			}
			var dirs, files []string
			hidden := 0
			for _, e := range entries {
				name := e.Name()
				childRel := strings.TrimPrefix(filepath.ToSlash(filepath.Join(rel, name)), "./")
				if name == ".git" || c.ignores(childRel) {
					hidden++
					continue
				}
				if e.IsDir() {
					dirs = append(dirs, name+"/")
					continue
				}
				if info, err := e.Info(); err == nil {
					files = append(files, fmt.Sprintf("%s (%s)", name, HumanSize(int64(info.Size()))))
				} else {
					files = append(files, name)
				}
			}
			sort.Strings(dirs)
			sort.Strings(files)
			all := append(dirs, files...)
			if len(all) == 0 {
				if hidden > 0 {
					return fmt.Sprintf("%s has nothing but ignored or hidden entries (%d).", rel, hidden), nil
				}
				return fmt.Sprintf("%s is empty.", rel), nil
			}
			truncated := ""
			if len(all) > maxListEntries {
				truncated = fmt.Sprintf("\n...and %d more entries (list a subdirectory to narrow).", len(all)-maxListEntries)
				all = all[:maxListEntries]
			}
			// Name the omission. A listing quietly missing the build output is
			// one the model will conclude does not exist, and then try to create.
			if hidden > 0 {
				truncated += fmt.Sprintf("\n(%d ignored or hidden %s not shown; read one by name if you need it.)",
					hidden, plural(hidden, "entry", "entries"))
			}
			return fmt.Sprintf("%s:\n%s%s", rel, strings.Join(all, "\n"), truncated), nil
		},
	}
}

// maxFoundFiles caps what a name search returns, and maxNameWalk how many
// entries it looks at in one tree.
const (
	maxFoundFiles = 200
	maxNameWalk   = 200000
)

// nameTree is one tree a name search walks: the project, or a linked
// dependency under its prefix.
type nameTree struct {
	prefix, base string
	c            *toolCtx
}

// findByName finds files whose name (a pattern without "/") or project path
// matches pattern, in the project and each linked dependency. It searches
// what search_code cannot: binary files, and ones git ignores, which is where
// an archive or a build's output usually is. Without it the assistant had
// only the shell, and searched the whole machine (find / -name esb.tgz).
// .git and node_modules are skipped.
func (c *toolCtx) findByName(pattern string) string {
	var found []string
	ignored := 0
	trees := []nameTree{{"", c.base, c}}
	for i := range c.deps {
		d := &c.deps[i]
		trees = append(trees, nameTree{d.Prefix + "/", d.Base, &toolCtx{project: c.depProject(d), base: d.Base}})
	}
	for _, tr := range trees {
		seen := 0
		filepath.Walk(tr.base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if seen++; seen > maxNameWalk || len(found) >= maxFoundFiles {
				return filepath.SkipAll
			}
			rel, relErr := filepath.Rel(tr.base, path)
			if relErr != nil || rel == "." {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if info.IsDir() {
				if info.Name() == ".git" || info.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			match := matchGlob(pattern, rel)
			if !strings.Contains(pattern, "/") {
				if ok, _ := filepath.Match(pattern, info.Name()); ok {
					match = true
				}
			}
			if !match {
				return nil
			}
			line := fmt.Sprintf("%s%s (%s)", tr.prefix, rel, HumanSize(info.Size()))
			if tr.c.ignores(rel) {
				line += ", ignored by git"
				ignored++
			}
			found = append(found, line)
			return nil
		})
	}
	if len(found) == 0 {
		where := "the project"
		if len(c.deps) > 0 {
			where += " or its linked dependencies"
		}
		return fmt.Sprintf("No file matching %q in %s. It is not here: ask the person for it rather than searching outside the project.", pattern, where)
	}
	sort.Strings(found)
	out := strings.Join(found, "\n")
	if len(found) >= maxFoundFiles {
		out += fmt.Sprintf("\n...stopped at %d. Narrow the pattern.", maxFoundFiles)
	}
	return out
}

func (c *toolCtx) readFileTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "read_file",
			Description: "Read a file from the project, with line numbers. Read before you edit: edit_file anchors on exact text, so you need the file's actual current content, not your memory of it. For a large file, pass start_line and line_count to read a window rather than pulling the whole thing into context.",
			Parameters: map[string]ToolParam{
				"path":       {Type: "string", Description: "Project-relative file path."},
				"start_line": {Type: "number", Description: "1-based first line to return. Default 1."},
				"line_count": {Type: "number", Description: "How many lines to return. Default 2000."},
			},
			Required: []string{"path"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rel := strArg(args, "path")
			t, err := c.resolve(rel)
			if err != nil {
				return "", err
			}
			abs := t.abs
			st, err := os.Stat(abs)
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("%s does not exist. Use list_files or search_code to find the right path", rel)
				}
				return "", err
			}
			if st.IsDir() {
				return "", fmt.Errorf("%s is a directory; use list_files", rel)
			}
			if st.Size() > maxReadBytes {
				return "", fmt.Errorf("%s is %s, too large to read whole. Pass start_line and line_count to read a window, or search_code to find the part you need",
					rel, HumanSize(int64(st.Size())))
			}
			b, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			if isBinary(b) {
				return "", fmt.Errorf("%s looks like a binary file", rel)
			}
			lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			start := intArg(args, "start_line", 1)
			if start < 1 {
				start = 1
			}
			count := intArg(args, "line_count", defaultReadTail)
			if count < 1 {
				count = defaultReadTail
			}
			if start > len(lines) {
				return "", fmt.Errorf("%s has only %d lines; start_line %d is past the end", rel, len(lines), start)
			}
			end := start - 1 + count
			if end > len(lines) {
				end = len(lines)
			}
			var out strings.Builder
			fmt.Fprintf(&out, "%s (lines %d-%d of %d):\n", rel, start, end, len(lines))
			for i := start - 1; i < end; i++ {
				fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
			}
			if end < len(lines) {
				fmt.Fprintf(&out, "...%d more lines below.\n", len(lines)-end)
			}
			return out.String(), nil
		},
	}
}

func (c *toolCtx) searchCodeTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "search_code",
			Description: "Search the project's text files for a regular expression, returning file:line plus the matching line. This is how you locate things (a symbol, a string in the UI, a config key) without reading files at random. Narrow with path_glob (e.g. \"*.go\", \"apps/**\") when a term is common.",
			Parameters: map[string]ToolParam{
				"pattern":   {Type: "string", Description: "Go regular expression. Case-sensitive unless you write it otherwise, e.g. \"(?i)todo\"."},
				"path_glob": {Type: "string", Description: "Optional filename filter, matched against the project-relative path (\"*.go\", \"apps/*\", \"**/page.go\"). To search a LINKED DEPENDENCY instead of this project, start the glob with its prefix: \"mylib/**\" searches that project and reports hits under the same prefix."},
				"max_hits":  {Type: "number", Description: "Cap on results. Default 200."},
			},
			Required: []string{"pattern"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			pattern := strArg(args, "pattern")
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "", fmt.Errorf("pattern %q is not a valid regular expression: %w", pattern, err)
			}
			glob := strArg(args, "path_glob")
			limit := intArg(args, "max_hits", maxSearchHits)
			if limit < 1 || limit > maxSearchHits {
				limit = maxSearchHits
			}
			hits, scanned, err := c.grepScoped(re, glob, limit)
			if err != nil {
				return "", err
			}
			if len(hits) == 0 {
				where := "the project"
				if glob != "" {
					where = fmt.Sprintf("files matching %q", glob)
				}
				return fmt.Sprintf("No matches for %s in %s (%d files searched).", pattern, where, scanned), nil
			}
			out := strings.Join(hits, "\n")
			if len(hits) >= limit {
				out += fmt.Sprintf("\n...stopped at %d matches. Narrow the pattern or pass path_glob.", limit)
			}
			return out, nil
		},
	}
}

// grepScoped picks which tree a search runs in.
//
// A glob whose first segment names a dependency searches THAT project, with
// hits reported under the same prefix so a path in a result can be pasted
// straight into read_file. Everything else searches the main project.
//
// Deliberately one tree per search rather than all of them at once. Sweeping
// every linked dependency by default would bury the project's own hits under a
// library's, and the limit would be spent before reaching them — the caller
// says where to look, which is what path_glob was already for.
func (c *toolCtx) grepScoped(re *regexp.Regexp, glob string, limit int) (hits []string, scanned int, err error) {
	head, rest, _ := strings.Cut(glob, "/")
	for i := range c.deps {
		d := &c.deps[i]
		if head != d.Prefix {
			continue
		}
		sub := &toolCtx{project: c.depProject(d), base: d.Base}
		hits, scanned, err = sub.grep(re, rest, limit)
		for j := range hits {
			hits[j] = d.Prefix + "/" + hits[j]
		}
		return hits, scanned, err
	}
	return c.grep(re, glob, limit)
}

// grep walks the project and collects matching lines.
func (c *toolCtx) grep(re *regexp.Regexp, glob string, limit int) (hits []string, scanned int, err error) {
	walkErr := filepath.Walk(c.base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable corner is not a reason to abandon the search
		}
		rel, relErr := filepath.Rel(c.base, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if path == c.base {
				return nil
			}
			// .git is skipped unconditionally: git does not list its own
			// directory as ignored, and walking it is pure noise.
			if info.Name() == ".git" || c.ignores(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(hits) >= limit {
			return filepath.SkipAll
		}
		if !info.Mode().IsRegular() || info.Size() > maxReadBytes {
			return nil
		}
		if c.ignores(rel) {
			return nil
		}
		if glob != "" && !matchGlob(glob, rel) {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil || isBinary(b) {
			return nil
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			if !re.MatchString(line) {
				continue
			}
			line = textutil.Clip(line, 400)
			hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimRight(line, "\r")))
			if len(hits) >= limit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return hits, scanned, walkErr
}

// matchGlob matches a path against a shell-ish pattern, understanding "**" as
// "any number of path segments". filepath.Match alone stops at separators,
// which makes the pattern people actually type ("**/page.go", "apps/**")
// match nothing and read as a broken search rather than a wrong pattern.
func matchGlob(pattern, path string) bool {
	if ok, _ := filepath.Match(pattern, path); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, filepath.Base(path)); ok && !strings.Contains(pattern, "/") {
		return true
	}
	if !strings.Contains(pattern, "**") {
		return false
	}
	// Translate the glob into a regexp: "**" spans separators, "*" does not.
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**/"):
			re.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(pattern[i:], "**"):
			re.WriteString(".*")
			i++
		case pattern[i] == '*':
			re.WriteString("[^/]*")
		case pattern[i] == '?':
			re.WriteString("[^/]")
		default:
			re.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	re.WriteString("$")
	compiled, err := regexp.Compile(re.String())
	if err != nil {
		return false
	}
	return compiled.MatchString(path)
}

// --- writing -----------------------------------------------------------------

func (c *toolCtx) editFileTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "edit_file",
			Description: "Change part of an existing file by replacing an exact piece of its text. " +
				"old_text must appear EXACTLY ONCE in the file. If it appears more than once the edit is refused, so include enough surrounding lines to make it unique, or set replace_all to change every one (a rename). " +
				"Read the file first: the anchor has to match what is actually there, indentation included. A miss is retried with the file's line endings, with curly quotes and dashes read as plain ones, and without spaces at line ends, and the result says when one of those was needed. " +
				"This is the tool for every change to an existing file; do not re-emit whole files.",
			Parameters: map[string]ToolParam{
				"path":        {Type: "string", Description: "Project-relative file path."},
				"old_text":    {Type: "string", Description: "The exact text to replace, copied from the file. Must be unique in the file: add surrounding lines until it is."},
				"new_text":    {Type: "string", Description: "What to put in its place. Pass an empty string to delete the anchored text."},
				"replace_all": {Type: "boolean", Description: "Replace every occurrence of old_text instead of requiring exactly one. Default false."},
			},
			Required: []string{"path", "old_text", "new_text"},
			Caps:     []Capability{CapWrite},
			Prompt:   "",
		},
		// Two edits to the same file in one batch must observe each other:
		// the second's anchor is only valid against the first's result.
		BatchLane: func(args map[string]any) string { return "anvil:" + strArg(args, "path") },
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rel := strArg(args, "path")
			oldText := strArg(args, "old_text")
			newText := strArg(args, "new_text")
			if oldText == "" {
				return "", fmt.Errorf("old_text is required: it is the anchor that says WHERE to edit. To create a new file use create_file")
			}
			if oldText == newText {
				return "", fmt.Errorf("old_text and new_text are identical, so there is nothing to change")
			}
			t, err := c.resolve(rel)
			if err != nil {
				return "", err
			}
			if err := t.requireWritable(); err != nil {
				return "", err
			}
			abs := t.abs
			before, existed, err := readIfPresent(abs)
			if err != nil {
				return "", err
			}
			if !existed {
				return "", fmt.Errorf("%s does not exist; use create_file to make a new file", rel)
			}
			if len(before) > maxWriteBytes {
				return "", fmt.Errorf("%s is %s, above the %s limit for a revertable edit. Anvil will not make an edit it cannot undo",
					rel, HumanSize(int64(len(before))), HumanSize(maxWriteBytes))
			}
			m := findAnchor(before, oldText, newText)
			switch n := len(m.spans); {
			case n == 0 && alreadyApplied(before, newText):
				return "", fmt.Errorf("old_text was not found in %s, but new_text is already there, so this edit may have been made already. Read the file to check before trying again", rel)
			case n == 0:
				return "", fmt.Errorf("old_text was not found in %s.%s", rel, anchorHint(before, oldText))
			case n > 1 && !boolArg(args, "replace_all"):
				return "", fmt.Errorf("old_text appears %d times in %s, so it is ambiguous. Include more surrounding lines until it is unique, or set replace_all to change every one", n, rel)
			}
			after := m.apply(before)
			if err := c.notesFit(rel, before, after); err != nil {
				return "", err
			}
			if err := writeFilePreservingMode(abs, after); err != nil {
				return "", err
			}
			e := journalWrite(c.udb, c.project.ID, c.session(), rel, before, true, after, false)
			c.publish(e)
			where := ""
			if n := len(m.spans); n > 1 {
				where = fmt.Sprintf(", %d places", n)
			}
			return fmt.Sprintf("Edited %s (+%d −%d%s). The change is recorded and can be reverted.%s", rel, e.Added, e.Removed, where, m.tier.note()), nil
		},
	}
}

func (c *toolCtx) createFileTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "create_file",
			Description: "Create a NEW file with the given content, making parent directories as needed. Refuses if the path already exists. To change an existing file use edit_file, which anchors on exact text and cannot silently drop the parts you didn't recall.",
			Parameters: map[string]ToolParam{
				"path":    {Type: "string", Description: "Project-relative path for the new file."},
				"content": {Type: "string", Description: "The file's full content."},
			},
			Required: []string{"path", "content"},
			Caps:     []Capability{CapWrite},
		},
		BatchLane: func(args map[string]any) string { return "anvil:" + strArg(args, "path") },
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rel := strArg(args, "path")
			content := strArg(args, "content")
			if len(content) > maxWriteBytes {
				return "", fmt.Errorf("content is %s, above the %s limit", HumanSize(int64(len(content))), HumanSize(maxWriteBytes))
			}
			t, err := c.resolve(rel)
			if err != nil {
				return "", err
			}
			if err := t.requireWritable(); err != nil {
				return "", err
			}
			abs := t.abs
			if _, existed, err := readIfPresent(abs); err != nil {
				return "", err
			} else if existed {
				return "", fmt.Errorf("%s already exists; use edit_file to change it", rel)
			}
			if err := c.notesFit(rel, "", content); err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return "", fmt.Errorf("create parent directory for %s: %w", rel, err)
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return "", fmt.Errorf("write %s: %w", rel, err)
			}
			e := journalWrite(c.udb, c.project.ID, c.session(), rel, "", false, content, false)
			c.publish(e)
			return fmt.Sprintf("Created %s (%d lines).", rel, e.Added), nil
		},
	}
}

func (c *toolCtx) deleteFileTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "delete_file",
			Description: "Delete a file from the project. Its content is recorded first, so the deletion can be reverted. Only for files the user has clearly asked to remove.",
			Parameters: map[string]ToolParam{
				"path": {Type: "string", Description: "Project-relative file path."},
			},
			Required: []string{"path"},
			Caps:     []Capability{CapWrite},
		},
		// A delete is the one write with no partial form: there is no
		// half-deleted file to notice and stop. It is asked about, and the
		// journal can put it back. "Always" is remembered for this project,
		// as an approval is in Claude Code: asking about every delete in a
		// refactor that removes a dozen files was friction with no safety
		// in it, since each one can be reverted.
		NeedsConfirm: true,
		Confirmation: &ToolConfirmation{
			Prompt: "Delete a file from " + c.project.Name + "?",
			Scope:  c.grantScope(),
		},
		SingleFirePerBatch: true,
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			rel := strArg(args, "path")
			t, err := c.resolve(rel)
			if err != nil {
				return "", err
			}
			if err := t.requireWritable(); err != nil {
				return "", err
			}
			abs := t.abs
			st, err := os.Stat(abs)
			if err != nil {
				if os.IsNotExist(err) {
					return "", fmt.Errorf("%s does not exist", rel)
				}
				return "", err
			}
			if st.IsDir() {
				return "", fmt.Errorf("%s is a directory, and Anvil deletes files, not directories", rel)
			}
			if st.Size() > maxWriteBytes {
				return "", fmt.Errorf("%s is %s, too large to record before deleting. Anvil will not delete what it cannot restore", rel, HumanSize(int64(st.Size())))
			}
			before, _, err := readIfPresent(abs)
			if err != nil {
				return "", err
			}
			if err := os.Remove(abs); err != nil {
				return "", fmt.Errorf("delete %s: %w", rel, err)
			}
			e := journalWrite(c.udb, c.project.ID, c.session(), rel, before, true, "", true)
			c.publish(e)
			return fmt.Sprintf("Deleted %s. Its content is recorded and the deletion can be reverted.", rel), nil
		},
	}
}

// writeFilePreservingMode writes content, keeping the file's existing
// permission bits. Without this an edit to a checked-in script silently
// clears its execute bit, and the breakage surfaces somewhere else entirely.
func writeFilePreservingMode(abs, content string) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(abs); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(abs), err)
	}
	return nil
}

// publish sends an edit card to the live turn, when there is one, and marks
// the tree as changed since the check last passed.
func (c *toolCtx) publish(e EditEntry) {
	c.markEdited()
	c.noteWrite(e.Path)
	if c.emit != nil {
		c.emit("anvil_edit", e)
	}
}

// --- running -----------------------------------------------------------------

// runsWithoutAsking reports whether the run tool goes without a card: the
// project is a checkout Anvil made, its owner chose Run commands without
// asking, and the person whose turn it is may change it (the owner's choice
// is theirs, not a reader's). A directory on this host always asks: a
// command there runs against the files somebody is using.
func (c *toolCtx) runsWithoutAsking() bool {
	return c.project.Kind == KindClone && c.project.RunWithoutAsking && !c.readOnly
}

// runConfirmation is the run tool's question, nil when it goes unasked.
func (c *toolCtx) runConfirmation() *ToolConfirmation {
	if c.runsWithoutAsking() {
		return nil
	}
	return &ToolConfirmation{
		Prompt:   "Run a command in " + c.project.Name + "?",
		Scope:    c.grantScope(),
		GrantArg: "command",
	}
}

func (c *toolCtx) runTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "run",
			Description: "Run a shell command in the project root and return its combined output. Use it to check your work: compile, run a test, inspect state. The command runs sandboxed with the project directory as its writable root, so it can be slower and more restricted than the same command in a terminal. Package managers (go, npm, pip) download through gohort into this project's own cache. For the project's own build or test commands, prefer the project actions if any are offered: those are the operator's own commands and run with a real environment. " +
				"A command still running after " + bgWords(TuneDuration("tune_tool_detach_after")) + " moves to the background: you are told so, you can carry on, and its output arrives as its own message when it ends. One that prints nothing for " + bgWords(TuneDuration("tune_tool_idle_limit")) + " is taken to be stuck and stopped, so a server or a watcher that never exits cannot be run here; nothing else could reach it anyway.",
			Parameters: map[string]ToolParam{
				"command": {Type: "string", Description: "Shell command, sh -c semantics. Runs with the project root as the working directory."},
			},
			Required: []string{"command"},
			Caps:     []Capability{CapExecute},
		},
		// The command is the model's own composition, so the user is shown it
		// and asked before it runs. The sandbox bounds what a command can
		// reach; it does not make running an arbitrary one the user's idea.
		//
		// GrantArg is what makes the edit-build-fix loop bearable: a standing
		// approval is offered on the command's VERB ("go build"), so the
		// second iteration does not re-ask the first one's question. The
		// framework refuses to offer or honor one for a command that chains,
		// so the grant can only ever mean the thing the user read.
		//
		// Unless the owner has said not to ask, on a checkout, where the
		// sandbox and the copy are the whole of what a command reaches
		// (runsWithoutAsking).
		// A Confirmation asks on its own (the loop reads either), so going
		// without one means leaving both off.
		NeedsConfirm: !c.runsWithoutAsking(),
		Confirmation: c.runConfirmation(),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			command := strArg(args, "command")
			if command == "" {
				return "", fmt.Errorf("command is required")
			}
			// A long command moves to the background (core/bgrun): inline
			// until the detach time, then the turn goes on and its output
			// arrives as its own message. A run with no session to deliver
			// to (an eval) stays inline, as before.
			sess := ToolSessionFromContext(ctx)
			if sess == nil {
				return c.runCommand(ctx, ctx, command, nil)
			}
			return bgrun.BackgroundRun{Tool: "run", Label: "run: " + oneLine(command, 60),
				Run: func(work *ToolSession) (string, error) {
					act := bgrun.RunActivityFrom(work.Context())
					if act != nil {
						act.WatchDir(c.base)
					}
					return c.runCommand(work.ContextWithSession(work.Context()), ctx, command, act)
				}}.Start(sess)
		},
	}
}

// runCommand runs command in the sandbox, ctx being the run's own, and
// reports its output and what it changed. turn is the tool call's context:
// once it is over (the run went to the background and the turn ended) the
// changes are still recorded, without cards in a turn that is gone. tee, when
// set, hears the output as it comes, which is how a background run is seen
// to be alive.
func (c *toolCtx) runCommand(ctx, turn context.Context, command string, tee io.Writer) (string, error) {
	// What the command changes is recorded like a file tool's write, so it
	// shows and reverts the same way (run_changes.go).
	watch := c.watchRun(ctx)
	if watch != nil {
		watch.turn = turn
	}
	res := c.sandboxedTee(ctx, command, 0, tee)
	changed := ""
	if watch != nil {
		changed = watch.finish(context.WithoutCancel(ctx))
	}
	out := strings.TrimSpace(res.Output)
	if len(out) > maxRunOutput {
		out = "...(earlier output trimmed)...\n" + out[len(out)-maxRunOutput:]
	}
	if res.Err != nil {
		if out == "" && changed == "" {
			return "", fmt.Errorf("command failed: %w", res.Err)
		}
		return fmt.Sprintf("Command failed (%s):\n%s%s", res.Err, out, changed), nil
	}
	if out == "" {
		return "Command finished with no output." + changed, nil
	}
	return out + changed, nil
}

// bgWords is a limit as a person says it ("a minute", "5 minutes").
func bgWords(d time.Duration) string {
	switch {
	case d <= 0:
		return "a while"
	case d == time.Minute:
		return "a minute"
	case d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return fmt.Sprintf("%d seconds", int(d/time.Second))
}

// gitTool exposes the read-only half of git.
//
// Read-only on purpose. Committing is a judgement about what constitutes a
// change worth recording and under what message, and it is the point at which
// work becomes hard to walk back. The agent's job here is to show you what it
// did; deciding that a state deserves to be history stays yours. An operator
// who disagrees can add a commit ACTION, which is at least their own command
// with their own message.
func (c *toolCtx) gitTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "git",
			Description: "Inspect the project's git state: status (what has changed), diff (the actual changes, optionally for one path), log (recent commits), branch (which branch you are on), blame (who last changed each line). Read-only: Anvil does not commit. Use status or diff to see the effect of your own edits, and log or blame to understand why code is the way it is before changing it. A project may be a shallow clone with no history, in which case log is nearly empty and blame is refused outright, and the tool tells you so rather than answering from the one commit it has.",
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: "What to inspect.", Enum: []string{"status", "diff", "log", "branch", "blame"}},
				"path":   {Type: "string", Description: "Optional project-relative path to narrow diff, log or blame to. Required for blame."},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			action := strings.ToLower(strArg(args, "action"))
			rel := strArg(args, "path")
			// Say WHICH of the two "no git here" situations this is. They
			// need different responses from the model: one is a project the
			// user chose not to version, the other is a host missing a
			// binary, and "git status failed" covers both badly.
			if !gitAvailable() {
				return "", fmt.Errorf("git is not installed on this host, so version-control history is unavailable here")
			}
			if !isGitRepo(c.base) {
				return "", fmt.Errorf("%s is not inside a git repository, so there is no history to inspect. Work from the files themselves", c.project.Name)
			}
			if rel != "" {
				// Resolve for containment, then hand git the RELATIVE path —
				// it is the form git reports back, so results stay consistent
				// with what the other tools call the same file.
				if _, err := c.resolve(rel); err != nil {
					return "", err
				}
			}
			var argv []string
			switch action {
			case "status":
				// Scoped to the project directory. Unscoped, git reports the
				// whole repository, so a project pointing at one package would
				// show changes from three directories away as its own.
				argv = []string{"status", "--short", "--branch", "--", "."}
			case "diff":
				argv = []string{"diff", "--no-ext-diff", "--no-textconv", "--stat", "-p"}
				if rel != "" {
					argv = append(argv, "--", rel)
				}
			case "log":
				argv = []string{"log", "--oneline", "-n", "25"}
				if rel != "" {
					argv = append(argv, "--", rel)
				}
			case "branch":
				argv = []string{"branch", "--show-current"}
			case "blame":
				if rel == "" {
					return "", fmt.Errorf("blame needs a path")
				}
				// Refused rather than served with a caveat. On a shallow
				// clone blame does not fail — it returns EVERY line
				// attributed to the single boundary commit, with that
				// commit's author and date, for code written by different
				// people years apart. A short answer can be caveated; a
				// confidently wrong one about who wrote something cannot,
				// because nothing in the output looks wrong.
				if isShallowRepo(c.base) {
					return "", fmt.Errorf("blame is unavailable here: %s Every line would be attributed to the one commit this clone has, which would be wrong rather than incomplete", shallowHistoryNote)
				}
				argv = []string{"blame", "--date=short", "-w", "--", rel}
			default:
				return "", fmt.Errorf("unknown git action %q; use status, diff, log, branch or blame", action)
			}
			out, err := runGit(c.base, argv...)
			if err != nil {
				return "", err
			}
			// A one-line header naming the branch, and the subdirectory when
			// the project is not the repository root. Without it, a `log` of
			// a package reads as the whole repo's history and a `status` of a
			// subdirectory reads as the whole tree being clean.
			header := ""
			if info := inspectRepo(c.base); info.IsRepo {
				where := info.Branch
				if where == "" {
					where = "detached at " + info.Head
				}
				if info.Prefix != "" {
					where += ", scoped to " + info.Prefix + "/"
				}
				header = "[" + where + "]\n"
			}
			if strings.TrimSpace(out) == "" {
				return fmt.Sprintf("%sgit %s: nothing to report.", header, action), nil
			}
			// A one-commit log is TRUE, which is exactly why it needs the
			// note: without it, "this file has one commit" reads as a fact
			// about the project rather than about the clone.
			if action == "log" && isShallowRepo(c.base) {
				out += "\n\n" + shallowHistoryNote
			}
			out = header + out
			if len(out) > maxRunOutput {
				out = out[:maxRunOutput] + "\n...(truncated; narrow with a path)"
			}
			return out, nil
		},
	}
}

// actionTools exposes the operator's opted-in actions as tools, one each.
//
// One tool per action rather than a single run_action(id) tool: the model
// picks a tool by reading its name and description, and "run_action" with an
// opaque id parameter tells it nothing about what running one would do. A
// tool called run_tests whose description is the operator's own sentence is
// something a model can choose correctly.
func (c *toolCtx) actionTools() []AgentToolDef {
	if !actionsAllowed(c.project, c.user, c.isAdmin) {
		return nil
	}
	var out []AgentToolDef
	for _, a := range c.project.agentInvokableActions() {
		action := a // capture per iteration
		name := toolNameFor(action.Label)
		desc := strings.TrimSpace(action.AgentHint)
		if desc == "" {
			desc = fmt.Sprintf("Run the project's %q command.", action.Label)
		}
		desc += " This is a command the project's operator configured; it runs in the project root with a real environment."
		out = append(out, AgentToolDef{
			Tool: Tool{
				Name:        name,
				Description: desc,
				Parameters:  map[string]ToolParam{},
				Caps:        []Capability{CapExecute},
			},
			// The command is the operator's, but the DECISION to fire it is
			// the model's, and a build or a deploy is not something to
			// discover after the fact.
			//
			// No GrantArg: this tool takes no arguments, because the command
			// is one fixed string the operator wrote. So a standing grant is
			// exactly as broad as it sounds — "always run THIS command" —
			// which is the one case where a tool-wide grant is honest.
			NeedsConfirm: true,
			Confirmation: &ToolConfirmation{
				Prompt: "Run " + action.Label + " on " + c.project.Name + "?",
				Scope:  c.grantScope(),
			},
			SingleFirePerBatch: true,
			Handler: func(context.Context, map[string]any) (string, error) {
				res := c.project.runAction(context.Background(), c.base, action)
				if res.GateFailed {
					return "", fmt.Errorf("%s", res.Output)
				}
				if !res.OK {
					return fmt.Sprintf("%s failed:\n%s", action.Label, res.Output), nil
				}
				return res.Output, nil
			},
		})
	}
	return out
}

// toolNameFor turns an operator's label into a legal tool name. Tool names
// are matched by the model against what it read in the catalog, so a label
// with spaces or punctuation has to become something stable and lowercase
// rather than being rejected.
var nonToolNameChar = regexp.MustCompile(`[^a-z0-9]+`)

func toolNameFor(label string) string {
	name := nonToolNameChar.ReplaceAllString(strings.ToLower(strings.TrimSpace(label)), "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = "project_action"
	}
	if len(name) > 48 {
		name = name[:48]
	}
	return name
}

// grantScope namespaces this project's standing approvals.
//
// The project id, not the project NAME and not the user: a grant to build in a
// throwaway checkout must not carry over to the source tree the server runs
// from, and a renamed project is still the same directory. Deleting a project
// leaves its grants orphaned under an id nothing resolves, which is the
// harmless direction — they can never match a call again.
func (c *toolCtx) grantScope() string { return "anvil:" + c.project.ID }

// --- shared helpers ----------------------------------------------------------

// plural picks a word form. Small, but a tool result reading "1 entries" is
// the kind of thing that makes a model distrust the rest of the sentence.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func strArg(args map[string]any, key string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// boolArg reads a flag a model may send as a boolean or as its text.
func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	}
	return false
}

func intArg(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		var parsed int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &parsed); err == nil {
			return parsed
		}
	}
	return def
}

// isBinary sniffs for a NUL byte in the first block, the same cheap test git
// uses. A binary file in a grep result is noise at best and a mangled
// terminal at worst.
func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}
