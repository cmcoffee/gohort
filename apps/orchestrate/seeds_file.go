package orchestrate

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Seed agents live in seeds/*.md, one file per agent, rather than as
// AgentRecord literals in Go. A seed is mostly a long persona prompt with a
// short header of settings, which is a document with a config block on top,
// not code — and every edit to one used to be a recompile plus an escaping
// exercise (a persona that wants a fenced code block cannot live in a Go raw
// string at all: see the backtick rule).
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

const (
	seedFrontmatterFence = "---"
	seedReadmeName       = "README.md"
)

// parseSeedFile turns one seed document into an AgentRecord. Every failure is
// an error naming the file: a seed that does not parse must be loud, because
// the alternative is an agent that silently is not there.
func parseSeedFile(name string, data []byte) (AgentRecord, error) {
	front, body, err := splitSeedFrontmatter(data)
	if err != nil {
		return AgentRecord{}, fmt.Errorf("seed %s: %v", name, err)
	}

	dec := json.NewDecoder(bytes.NewReader(front))
	// Strict: a misspelled key ("alowed_tools") would otherwise be dropped
	// without a word, and the agent would come up missing a setting its
	// file plainly asks for.
	dec.DisallowUnknownFields()
	var sf seedFile
	if err := dec.Decode(&sf); err != nil {
		return AgentRecord{}, fmt.Errorf("seed %s: frontmatter: %v", name, err)
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
	rec.OrchestratorPrompt = body

	// Owner is stamped, never declared: a seed belongs to the framework, and
	// a file that could name its own owner could hand itself to a user.
	rec.Owner = seedOwner
	return rec, nil
}

// splitSeedFrontmatter separates the leading fenced JSON block from the body.
func splitSeedFrontmatter(data []byte) ([]byte, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, seedFrontmatterFence+"\n") {
		return nil, "", fmt.Errorf("file must start with a %q frontmatter fence", seedFrontmatterFence)
	}
	rest := text[len(seedFrontmatterFence)+1:]
	end := strings.Index(rest, "\n"+seedFrontmatterFence+"\n")
	if end < 0 {
		return nil, "", fmt.Errorf("frontmatter is never closed by a %q line", seedFrontmatterFence)
	}
	front := rest[:end+1]
	body := rest[end+len(seedFrontmatterFence)+2:]
	return []byte(front), strings.TrimRight(body, "\n"), nil
}

// fileSeedAgents returns every seed declared under seeds/, sorted by filename
// so the agent list has a stable order.
//
// A parse failure is fatal rather than skipped. Dropping a seed quietly would
// present a deployment that is missing an agent as a deployment that never had
// one, and the person who broke the file is the one person who would not see
// it.
func fileSeedAgents() []AgentRecord {
	entries, err := fs.ReadDir(seedFilesFS, "seeds")
	if err != nil {
		Fatal("seed agents: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		// The one reserved name: seeds/README.md documents the format for
		// whoever opens the directory next. Every OTHER .md here is a seed
		// and must parse as one.
		if e.Name() == seedReadmeName {
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
