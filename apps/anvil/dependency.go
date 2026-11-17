// Reaching a project's dependencies.
//
// A codebase is rarely self-contained. gohort is written against snugforge;
// asking "why does HumanSize round that way" from inside gohort used to be
// unanswerable, because a project could see nothing but its own tree — both
// spellings of a sibling path are refused before anything touches the disk,
// and ExtraRoots does not help, since it only widens where a symlink INSIDE
// the tree may land.
//
// A dependency is therefore a LINK TO ANOTHER PROJECT, not a path.
//
// That is a security decision before it is an ergonomic one. The obvious
// design — name a directory, like an editor's "add folder to workspace" — is
// a read-anything-on-this-host control the moment a non-admin can set it on
// their own checkout, and closing that hole by making it admin-only removes
// most of the point. Linking a project you can ALREADY open grants nothing you
// did not already have, so the check that makes it safe is one that already
// exists and runs on every request: resolve the link the same way the picker
// resolves a project, and a link you may not open simply is not there.
//
// Addressing keeps the relative-path invariant intact. A dependency claims a
// PREFIX — "snugforge/nfo/misc.go" — so no tool ever accepts an absolute path
// or a "..", and the first path segment is what selects the tree.
package anvil

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Dependency links one project into another as readable source.
type Dependency struct {
	// ProjectID is the linked project. Resolved per request against the
	// CALLER, so a link to something they may not open resolves to nothing
	// rather than to a back door.
	ProjectID string `json:"project_id"`
	// Prefix is the first path segment that selects this dependency.
	// Defaults to a slug of the linked project's name.
	Prefix string `json:"prefix,omitempty"`
	// Writable opts this link into edits. Off by default: reading a
	// dependency to understand it is the common case, and changing one is a
	// deliberate act that should not happen because a model was reasoning
	// about a call chain and kept going.
	Writable bool `json:"writable,omitempty"`
}

// resolvedDep is a dependency that the caller can actually open, with its tree
// located. Built per turn; never stored.
type resolvedDep struct {
	Dependency
	Name string // the linked project's own name, for prompts and messages
	Kind string // the linked project's kind — its containment rules, not ours
	Base string // absolute path to its tree
}

var nonPrefixChar = regexp.MustCompile(`[^a-z0-9_.-]+`)

// slugPrefix derives an addressing prefix from a project name.
func slugPrefix(name string) string {
	s := nonPrefixChar.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	return strings.Trim(s, "_")
}

// resolveDependencies turns a project's links into trees this caller may read.
//
// A link the caller cannot open is DROPPED, silently as far as the model is
// concerned — it never appears in the prompt, so nothing refers to it and
// nothing has to explain its absence. Silence is right here: the alternative
// tells one user about another's project by describing what they may not see.
func (T *Anvil) resolveDependencies(udb Database, user string, isAdmin bool, p Project) []resolvedDep {
	var out []resolvedDep
	seen := map[string]bool{}
	for _, d := range p.Dependencies {
		if strings.TrimSpace(d.ProjectID) == "" || d.ProjectID == p.ID {
			// A project linking itself would shadow its own root with a
			// prefix, which is confusing rather than dangerous — but there is
			// no reading of it that helps.
			continue
		}
		dep, ok := T.findProject(udb, user, isAdmin, d.ProjectID)
		if !ok {
			continue
		}
		base, err := dep.baseDir()
		if err != nil {
			continue
		}
		prefix := slugPrefix(d.Prefix)
		if prefix == "" {
			prefix = slugPrefix(dep.Name)
		}
		if prefix == "" || seen[prefix] {
			// Two links claiming one prefix would make addressing ambiguous,
			// and the loser would be whichever came second in a list nobody
			// thinks of as ordered. Drop it rather than guess.
			continue
		}
		seen[prefix] = true
		// A dependency is only writable when the LINK says so AND the caller
		// could write to that project directly. The link cannot grant more
		// than the project itself would.
		writable := d.Writable && writeAllowed(dep, user, isAdmin)
		out = append(out, resolvedDep{
			Dependency: Dependency{ProjectID: d.ProjectID, Prefix: prefix, Writable: writable},
			Name:       dep.Name,
			Kind:       dep.Kind,
			Base:       base,
		})
	}
	return out
}

// target is the tree one path resolves in.
type target struct {
	abs      string // the proved absolute path
	rel      string // the path as given, prefix included
	writable bool
	dep      *resolvedDep // nil when the path is in the main project
}

// resolve maps a path an LLM supplied onto a tree.
//
// The first segment selects: if it names a dependency prefix, the rest is
// resolved inside that project; otherwise the whole path is the main
// project's. Containment is enforced by the OWNING project's own rules, so a
// checkout linked into a directory keeps the checkout's stricter symlink
// handling rather than inheriting the host's.
func (c *toolCtx) resolve(rel string) (target, error) {
	rel = strings.TrimSpace(rel)
	head, rest, _ := strings.Cut(strings.TrimPrefix(path.Clean(strings.TrimSpace(rel)), "./"), "/")
	for i := range c.deps {
		d := &c.deps[i]
		if head != d.Prefix {
			continue
		}
		// A bare prefix means the dependency's root.
		abs, err := c.depProject(d).resolvePath(d.Base, rest)
		if err != nil {
			return target{}, fmt.Errorf("%s: %w", d.Prefix, err)
		}
		return target{abs: abs, rel: rel, writable: d.Writable, dep: d}, nil
	}
	abs, err := c.project.resolvePath(c.base, rel)
	if err != nil {
		return target{}, err
	}
	return target{abs: abs, rel: rel, writable: !c.readOnly}, nil
}

// depProject rebuilds enough of the linked project for its containment rules.
// Kind is what those rules turn on, and it is carried on the resolved link.
func (c *toolCtx) depProject(d *resolvedDep) Project {
	return Project{ID: d.ProjectID, Name: d.Name, Kind: d.Kind, Root: d.Base}
}

// requireWritable refuses an edit the link does not permit, in terms that say
// what would change the answer.
func (t target) requireWritable() error {
	if t.writable {
		return nil
	}
	if t.dep != nil {
		return fmt.Errorf("%s is a linked dependency opened read-only, so it cannot be changed from here. "+
			"Enable edits on that link in the project's settings, or open %s as its own project and change it there",
			t.dep.Prefix, t.dep.Name)
	}
	return fmt.Errorf("this project is open read-only")
}

// depSummary describes the linked trees for the agent's prompt. Without it the
// prefixes are unguessable and the dependencies may as well not be linked.
func depSummary(deps []resolvedDep) string {
	if len(deps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Linked dependencies\n\n" +
		"Other projects readable from here. Address them by their prefix: the first path segment picks the tree.\n\n")
	for _, d := range deps {
		mode := "read-only"
		if d.Writable {
			mode = "editable"
		}
		fmt.Fprintf(&b, "- %s/: %s (%s). e.g. read_file(\"%s/README.md\"), search_code(pattern, path_glob=\"%s/**\")\n",
			d.Prefix, d.Name, mode, d.Prefix, d.Prefix)
	}
	return b.String()
}
