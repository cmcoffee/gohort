package orchestrate

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// Seed agents live in seeds/*.md, one file per agent, rather than as
// AgentRecord literals in Go. A seed is mostly a long persona prompt with a
// short header of settings, which is a document with a config block on top,
// not code. Every edit to one used to be a recompile plus an escaping exercise
// (a persona that wants a fenced code block cannot live in a Go raw string at
// all: see the backtick rule).
//
// The format is JSON frontmatter between "---" fences, then the
// OrchestratorPrompt as the markdown body:
//
//	---
//	{ "id": "seed-kb", "name": "Knowledge Base", ... }
//	---
//	You are a knowledge-base assistant. ...
//
// JSON rather than YAML because AgentRecord already carries json tags for
// every field, so the file's keys ARE the record's keys with no translation
// table to keep in sync, and because a YAML parser would be a new external
// dependency (this tree builds in GOPATH mode, where a dependency has to be
// vendored, not merely required).
//
// Files are embedded, so a seed edit still ships inside the single binary and
// a deployment cannot end up with a half-populated agent list because a
// directory was not copied.
//
//go:embed seeds/*.md
var seedFilesFS embed.FS

// seedFile is what one seed document decodes into: the record itself, plus a
// notes object that exists only for the humans reading the file. JSON has no
// comments, and the rationale for a setting ("why disable_skills is on here")
// is the part of a seed most worth keeping next to the setting. The loader
// reads notes and discards them.
type seedFile struct {
	AgentRecord
	Notes map[string]string `json:"notes,omitempty"`
}

// seedSnippets are the runtime-resolved fragments a seed body can splice in
// with a {{name}} placeholder. They exist because two of these prompts are not
// fixed text: one names the memory tools by their live surface (the collapsed
// remember/recall envelope renames them), and one appends a Python
// compatibility note only when the sandbox interpreter is old enough to need
// it. Both used to be string concatenation around the Go literal.
//
// Expansion happens on every load, not at parse time, so a prompt keeps
// tracking the live flag and the live probe with no restart, exactly as the
// concatenation did.
//
// Deliberately a small closed set. This is a substitution table for facts the
// framework knows about itself, not a template language: a seed that wants to
// compute something is a seed that belongs in Go.
var seedSnippets = map[string]func() string{
	"memory_save_call":    memFindingSavePhrase,
	"sandbox_python_note": sandboxPythonNoteSection,
}

var seedPlaceholderRe = regexp.MustCompile(`\{\{([a-z0-9_]*)\}\}`)

// checkSeedPlaceholders rejects a body that names a snippet we do not have.
// An unknown placeholder left alone would ship "{{sandbox_pyton_note}}" to the
// model as if it were prose.
func checkSeedPlaceholders(body string) error {
	for _, m := range seedPlaceholderRe.FindAllStringSubmatch(body, -1) {
		if _, ok := seedSnippets[m[1]]; !ok {
			return fmt.Errorf("unknown placeholder %s", m[0])
		}
	}
	return nil
}

// expandSeedSnippets resolves every {{name}} in a body. Unknown names cannot
// reach here: checkSeedPlaceholders refused the file at parse time.
func expandSeedSnippets(body string) string {
	if !strings.Contains(body, "{{") {
		return body
	}
	out := seedPlaceholderRe.ReplaceAllStringFunc(body, func(tok string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(tok, "{{"), "}}")
		if fn, ok := seedSnippets[name]; ok {
			return fn()
		}
		return tok
	})
	return out
}

// parseSeedFile turns one seed document into an AgentRecord. Every failure is
// an error naming the file: a seed that does not parse must be loud, because
// the alternative is an agent that silently is not there.
func parseSeedFile(name string, data []byte) (AgentRecord, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return AgentRecord{}, fmt.Errorf("seed %s: %v", name, err)
	}

	var sf seedFile
	if err := decodeFrontmatter(front, &sf); err != nil {
		return AgentRecord{}, fmt.Errorf("seed %s: %v", name, err)
	}

	rec := sf.AgentRecord
	if strings.TrimSpace(rec.ID) == "" {
		return AgentRecord{}, fmt.Errorf("seed %s: no id", name)
	}
	if strings.TrimSpace(rec.Name) == "" {
		return AgentRecord{}, fmt.Errorf("seed %s: no name", name)
	}
	if strings.TrimSpace(body) == "" {
		return AgentRecord{}, fmt.Errorf("seed %s: no prompt body below the frontmatter", name)
	}
	if rec.OrchestratorPrompt != "" {
		return AgentRecord{}, fmt.Errorf("seed %s: put the prompt in the body, not in orchestrator_prompt", name)
	}
	if err := checkSeedPlaceholders(body); err != nil {
		return AgentRecord{}, fmt.Errorf("seed %s: %v", name, err)
	}
	// Placeholders stay unexpanded here. parseSeedFile is the parse; the
	// snippets resolve per load, in copySeedRecord.
	rec.OrchestratorPrompt = body

	// Owner is stamped, never declared: a seed belongs to the framework, and
	// a file that could name its own owner could hand itself to a user.
	rec.Owner = seedOwner
	return rec, nil
}

// fileSeedAgents returns every seed declared under seeds/, sorted by filename
// so the agent list has a stable order.
//
// The documents are read and decoded once. Decoding a record costs a few
// hundred microseconds (AgentRecord is a wide struct, and this runs on paths
// that resolve an agent several times per request), while the snippets that
// have to stay live are re-expanded on every call. Callers get their own copy,
// so a caller that appends to a seed's tool list cannot reach into the cache.
func fileSeedAgents() []AgentRecord {
	seedDocsOnce.Do(func() { seedDocs = loadSeedDocs() })
	out := make([]AgentRecord, len(seedDocs))
	for i, rec := range seedDocs {
		out[i] = copySeedRecord(rec)
	}
	return out
}

var (
	seedDocsOnce sync.Once
	seedDocs     []AgentRecord
)

// copySeedRecord hands out a record that shares nothing mutable with the
// cached one, and resolves its runtime snippets on the way.
//
// The slice fields are copied by name rather than reflectively, and
// TestSeedCopyCoversEverySliceField fails if a seed document ever populates a
// slice this does not name. An aliased slice would be a bug nobody could
// reproduce: one agent's edit changing a different agent's tools.
func copySeedRecord(rec AgentRecord) AgentRecord {
	rec.OrchestratorPrompt = expandSeedSnippets(rec.OrchestratorPrompt)
	rec.AllowedTools = append([]string(nil), rec.AllowedTools...)
	rec.Triggers = append([]string(nil), rec.Triggers...)
	if rec.IntakeForm != nil {
		form := make(IntakeFormSpec, len(rec.IntakeForm))
		copy(form, rec.IntakeForm)
		for i := range form {
			form[i].Options = append([]string(nil), form[i].Options...)
		}
		rec.IntakeForm = form
	}
	return rec
}

// loadSeedDocs reads and parses every seed document. Bodies come back with
// their {{snippet}} placeholders intact; fileSeedAgents expands them per call.
//
// A parse failure is fatal rather than skipped. Dropping a seed quietly would
// present a deployment that is missing an agent as a deployment that never had
// one, and the person who broke the file is the one person who would not see
// it.
func loadSeedDocs() []AgentRecord {
	entries, err := fs.ReadDir(seedFilesFS, "seeds")
	if err != nil {
		Fatal("seed agents: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if e.Name() == libraryReadmeName {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	out := make([]AgentRecord, 0, len(names))
	seen := make(map[string]string, len(names))
	for _, name := range names {
		data, err := seedFilesFS.ReadFile(path.Join("seeds", name))
		if err != nil {
			Fatal("seed agents: %v", err)
		}
		rec, err := parseSeedFile(name, data)
		if err != nil {
			Fatal("%v", err)
		}
		if prior, dup := seen[rec.ID]; dup {
			Fatal("seed agents: %s and %s both declare id %q", prior, name, rec.ID)
		}
		seen[rec.ID] = name
		out = append(out, rec)
	}
	return out
}
