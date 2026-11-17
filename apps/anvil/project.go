// A project is the thing Anvil edits: a directory on disk, plus the rules
// about who may reach it and what may be run inside it.
//
// There are two kinds, and the difference is entirely about WHOSE tree the
// edits land in.
//
//   - root   — a real source tree already on the host, registered by an
//     operator. Edits are edits: the files the server (or the developer)
//     is actually using change underneath them. It is admin-gated because
//     the blast radius is the tree.
//   - clone  — a git checkout Anvil made and owns, under the user's own
//     workspace root. Edits land in a copy nobody else is running, and the
//     work comes back out as a branch or a patch. Any user may have these.
//
// Everything above the base directory is shared: the same tool surface, the
// same edit journal, the same actions. A kind answers one question — where
// is the base directory, and who is allowed to ask for it — and the rest of
// the app never branches on it again.
//
// CONTAINMENT. Every file path an LLM supplies is resolved through
// resolvePath, which refuses absolute paths, refuses `..`, and then resolves
// symlinks and re-checks the ANSWER. That last step is the one that matters:
// a prefix check on the path string is satisfied by `src/link/etc/passwd`
// while the open follows the link straight out of the tree.
//
// Real source trees contain symlinks that legitimately leave the tree — a
// package linked in from a sibling checkout, say — so refusing every outward link would make
// the honest case unusable and teach operators to widen the root instead,
// which is worse. Instead a project declares ExtraRoots: the additional
// absolute paths a link inside it may land in. A link to anywhere else is
// refused by a message that NAMES the resolved target, so the operator can
// see what it wanted and decide, rather than guessing at a denial.
package anvil

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// projectTable holds Project records. The same table name is used in two
// different stores, which is the whole tenancy model: root projects live in
// the app's root DB (deployment-wide, operator-managed), clone projects in
// the caller's own UserDB. A lookup that doesn't say which store it means
// doesn't compile.
const projectTable = "anvil_projects"

// anvilSubdir is where clone projects are checked out, under the owning
// user's workspace root: <workspaces>/<user>/.anvil/<project id>.
const anvilSubdir = ".anvil"

// Project kinds.
const (
	KindRoot  = "root"
	KindClone = "clone"
)

// Project is one editable tree.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // KindRoot | KindClone

	// Root — kind=root only: the absolute host path of the tree.
	Root string `json:"root,omitempty"`
	// ExtraRoots — kind=root only: additional absolute paths a symlink
	// inside Root is allowed to resolve into. See the package comment.
	ExtraRoots Lines `json:"extra_roots,omitempty"`
	// AllowedUsers — kind=root only: non-admin usernames granted access.
	// Empty means admins only, which is the safe default for a real tree.
	AllowedUsers Lines `json:"allowed_users,omitempty"`

	// RepoURL / Branch / Token — kind=clone only. Token is write-only from
	// the UI's perspective: it is blanked on every read that leaves the
	// server (see redacted).
	RepoURL string `json:"repo_url,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Token   string `json:"token,omitempty"`
	// FullHistory clones the whole history instead of the default shallow
	// single commit. Off by default: a first clone is the slowest thing here
	// and the usual job needs no history at all. Turn it on when `git log` or
	// `git blame` are the point — blame in particular is not merely limited
	// on a shallow clone, it is wrong.
	FullHistory bool `json:"full_history,omitempty"`
	// HasToken tells a browser that a token IS stored without telling it
	// which one, so the edit form can offer "leave blank to keep" instead
	// of implying none is set. Derived on read; never persisted.
	HasToken bool `json:"has_token,omitempty"`

	// Instructions are the operator's own notes for this project, injected
	// into the agent's prompt alongside the project's own CLAUDE.md.
	//
	// Two sources rather than one because they answer different questions.
	// A CLAUDE.md is the REPOSITORY's rules, versioned with it and true for
	// anyone who checks it out. This field is what is true about working on
	// it HERE — a staging URL, which of two build commands this box can
	// actually run, a warning that one package is mid-migration. Putting that
	// in the repo would be wrong; leaving it unsaid means repeating it every
	// session.
	Instructions string `json:"instructions,omitempty"`

	// Reasoning selects how hard the model works on this project.
	//
	//	"" / "standard" — the worker, at the deployment's usual depth.
	//	"quick"         — the worker with a reduced thinking budget.
	//	"lead"          — the reasoning turn escalates to the lead model.
	//
	// Per project because the answer genuinely differs between them: a
	// refactor spanning a real source tree is worth the lead, and renaming a
	// constant in a scratch checkout is not worth waiting for it.
	//
	// There is deliberately no "deeper than usual" option. A per-agent think
	// budget can only LOWER the deployment's ceiling, never raise it, so an
	// option promising more thought than the admin configured would be a
	// control that silently does nothing — and this app has shipped one of
	// those already.
	Reasoning string `json:"reasoning,omitempty"`

	// Dependencies are other projects linked in as readable source. See
	// dependency.go for why they are links to PROJECTS rather than paths.
	Dependencies []Dependency `json:"dependencies,omitempty"`

	// Actions are the operator-defined commands this project exposes —
	// build, test, rebuild+restart. See actions.go.
	Actions []ProjectAction `json:"actions,omitempty"`

	// CheckCommand is the project's check: its build and tests, run in the
	// sandbox after the agent's edits and before its turn may end (check.go).
	// Empty: no check. CheckManual runs it only when the agent asks.
	CheckCommand string `json:"check_command,omitempty"`
	CheckManual  bool   `json:"check_manual,omitempty"`

	// Owner is the username whose store holds a clone project. Empty for
	// root projects, which belong to the deployment rather than a person.
	Owner     string    `json:"owner,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// Source is "bundle" for a checkout made from an uploaded git bundle
	// (bundle.go), which has no repository address; empty otherwise.
	// BundleName is the file it came from. Unpack is "unpacking" or
	// "failed" until the checkout is made, with UnpackError saying why.
	// All four are the server's: a save never takes them from the browser.
	Source      string `json:"source,omitempty"`
	BundleName  string `json:"bundle_name,omitempty"`
	Unpack      string `json:"unpack,omitempty"`
	UnpackError string `json:"unpack_error,omitempty"`

	// AppSlug is the person's own app a checkout with Source "app" holds as
	// its folder (app_project.go). InstallOnSave installs it as each turn
	// ends with changes, for an app nobody else uses; PreviewReal previews
	// it on the person's real records instead of a copy.
	AppSlug       string `json:"app_slug,omitempty"`
	InstallOnSave bool   `json:"install_on_save,omitempty"`
	PreviewReal   bool   `json:"preview_real,omitempty"`
	// RunWithoutAsking lets the assistant's commands run without asking
	// first, on a checkout Anvil made (runsWithoutAsking): there a command
	// runs in the sandbox, without network, against the owner's own copy, and
	// asking before each one was most of a coding turn's interruptions. A
	// directory on this host keeps asking.
	RunWithoutAsking bool `json:"run_without_asking,omitempty"`

	// Collections are the knowledge collections the assistant searches on
	// this project (knowledge.go): reference it should work from, such as an
	// API's docs or the team's standards. Each turn searches only those the
	// person asking may read.
	Collections []string `json:"collections,omitempty"`

	// Prompts are saved prompts, used as slash commands in the message box
	// (prompts.go).
	Prompts []SavedPrompt `json:"prompts,omitempty"`

	// Where Publish last pushed a repository started here or brought as a
	// bundle (publish.go): the address, branch and token, kept for the next
	// push. Only Publish sets them; the token, like Token, never leaves the
	// server, and HasPushToken says one is kept.
	PushURL      string    `json:"push_url,omitempty"`
	PushBranch   string    `json:"push_branch,omitempty"`
	PushToken    string    `json:"push_token,omitempty"`
	PushedAt     time.Time `json:"pushed_at,omitempty"`
	HasPushToken bool      `json:"has_push_token,omitempty"`
}

// redacted returns a copy safe to hand to a browser: the clone token is a
// credential, and a list endpoint is not a place to spend one.
func (p Project) redacted() Project {
	if p.Token != "" {
		p.Token = ""
		p.HasToken = true
	}
	if p.PushToken != "" {
		p.PushToken = ""
		p.HasPushToken = true
	}
	return p
}

// baseDir returns the absolute directory this project's files live in.
// For a root project that is the registered tree; for a clone it is the
// checkout under the owner's workspace, created if absent.
func (p Project) baseDir() (string, error) {
	switch p.Kind {
	case KindRoot:
		root := strings.TrimSpace(p.Root)
		if root == "" {
			return "", fmt.Errorf("project %q has no root path configured", p.Name)
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fmt.Errorf("resolve root %q: %w", root, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return "", fmt.Errorf("root %q is not reachable: %w", abs, err)
		}
		if !st.IsDir() {
			return "", fmt.Errorf("root %q is not a directory", abs)
		}
		return abs, nil
	case KindClone:
		if p.Owner == "" {
			return "", fmt.Errorf("clone project %q has no owner", p.Name)
		}
		base := WorkspacesDir()
		if base == "" {
			return "", fmt.Errorf("workspaces dir not configured, and clone projects need one")
		}
		if strings.ContainsAny(p.ID, `/\`) || strings.Contains(p.ID, "..") {
			return "", fmt.Errorf("invalid project id %q", p.ID)
		}
		if strings.ContainsAny(p.Owner, `/\`) || strings.Contains(p.Owner, "..") {
			return "", fmt.Errorf("invalid owner %q", p.Owner)
		}
		return filepath.Join(base, p.Owner, anvilSubdir, p.ID), nil
	default:
		return "", fmt.Errorf("project %q has unknown kind %q", p.Name, p.Kind)
	}
}

// allowedRoots is the set of absolute directories a resolved path may live
// in: the base directory — in BOTH the form the operator typed and the form it
// actually resolves to — plus any declared ExtraRoots.
//
// Both forms of the base, because the two are routinely different and neither
// is wrong. A root registered as /srv/src/project resolves to
// /data/src/project when /srv/src is a link to /data/src. Comparing a resolved
// FILE path against an unresolved ROOT then matches nothing at all, and every
// read in an ordinary checkout is refused as an escape attempt. That was the
// bug: the loop below already resolved the extra roots, for exactly this
// reason, and the base itself never got the same treatment.
func (p Project) allowedRoots(base string) []string {
	out := []string{filepath.Clean(base)}
	if real, err := filepath.EvalSymlinks(base); err == nil {
		if cleaned := filepath.Clean(real); cleaned != out[0] {
			out = append(out, cleaned)
		}
	}
	if p.Kind != KindRoot {
		return out
	}
	for _, extra := range p.ExtraRoots {
		extra = strings.TrimSpace(extra)
		if extra == "" {
			continue
		}
		if abs, err := filepath.Abs(extra); err == nil {
			// Resolve the declared root too, for the same reason.
			if real, err := filepath.EvalSymlinks(abs); err == nil {
				abs = real
			}
			out = append(out, filepath.Clean(abs))
		}
	}
	return out
}

// followsOwnSymlinks reports whether a symlink INSIDE the project may lead out
// of it without being declared first.
//
// The two kinds differ because the trust does. A root project is a directory
// an admin registered on this host: its symlinks are part of how they arranged
// their own tree, and every other tool they use — their editor, their shell,
// their compiler — follows them without asking.
// Refusing them buys nothing an admin wants and costs them a configuration
// step to get back to normal, which is the shape of a guard people learn to
// switch off.
//
// A clone project is the opposite: the content came from a remote and a git
// repository can carry a symlink pointing anywhere it likes. Following one
// there is how a hostile repository reads /etc/passwd through a checkout, so
// containment stays strict and ExtraRoots stays the way to widen it.
//
// What is refused in BOTH cases is the part an LLM actually gets wrong: an
// absolute path, or one climbing out with "..". Those are lexical, checked
// before anything touches the disk, and no kind of project relaxes them.
func (p Project) followsOwnSymlinks() bool { return p.Kind == KindRoot }

// withinAny reports whether abs is one of roots or lives under one.
func withinAny(abs string, roots []string) bool {
	abs = filepath.Clean(abs)
	for _, root := range roots {
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolvePath turns a project-relative path from an LLM into an absolute one
// proved to be somewhere this project is allowed to reach.
//
// The path need not exist — a create lands on a name that isn't there yet. In
// that case the DEEPEST existing ancestor is resolved and checked instead, so
// a new file inside a symlinked directory is judged by where that directory
// actually is rather than by the name it was reached through.
func (p Project) resolvePath(base, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return filepath.Clean(base), nil
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative to the project root, got absolute %q", rel)
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			return "", fmt.Errorf("path may not contain \"..\": %q", rel)
		}
		// The repository's own store, config included: a setting written
		// there runs a program on the next git command (git.go). Case-folded,
		// as macOS's filesystem is.
		if strings.EqualFold(seg, ".git") {
			return "", fmt.Errorf("path may not be inside .git: %q (use the git tool to read history)", rel)
		}
	}
	joined := filepath.Clean(filepath.Join(base, rel))
	roots := p.allowedRoots(base)

	// Resolve the deepest part of the path that exists, then judge THAT.
	// Walking up rather than failing lets a not-yet-created file be checked
	// against its real parent directory.
	probe := joined
	var trailing []string
	for {
		real, err := filepath.EvalSymlinks(probe)
		if err == nil {
			resolved := filepath.Join(append([]string{real}, trailing...)...)
			if withinAny(resolved, roots) {
				return resolved, nil
			}
			// Landed outside. For a tree the operator registered themselves,
			// that just means the tree contains a link — follow it, the same
			// way their editor and their compiler do. The lexical checks above
			// already guaranteed the path did not climb out on its own.
			if p.followsOwnSymlinks() {
				return resolved, nil
			}
			// No "ask an admin to widen it" here, deliberately. Extra roots
			// are a root-project setting and are cleared on save for a
			// checkout — they have to be, or a user could name any path on
			// the host in their own project and read it. Offering a remedy
			// that cannot be granted would just send someone looking for a
			// setting that is not there.
			return "", fmt.Errorf(
				"%q points to %q, outside this checkout. A repository can carry a symlink to anywhere, so Anvil does not follow one out of a checkout it made. Read the file from a project that covers it instead",
				rel, resolved)
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %q: %w", rel, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("resolve %q: no existing ancestor directory", rel)
		}
		trailing = append([]string{filepath.Base(probe)}, trailing...)
		probe = parent
	}
}

// --- storage -----------------------------------------------------------------

// saveProject writes p into the correct store for its kind.
func (T *Anvil) saveProject(udb Database, p Project) {
	if p.Kind == KindRoot {
		T.DB.Set(projectTable, p.ID, p)
		return
	}
	udb.Set(projectTable, p.ID, p)
}

// deleteProject removes the record (not the files — see handleProjectDelete).
func (T *Anvil) deleteProject(udb Database, p Project) {
	if p.Kind == KindRoot {
		T.DB.Unset(projectTable, p.ID)
		return
	}
	udb.Unset(projectTable, p.ID)
}

// listProjects returns every project this caller may open: their own clones,
// plus the root projects they are allowed to reach. Sorted by name so the
// picker doesn't reshuffle between loads.
func (T *Anvil) listProjects(udb Database, user string, isAdmin bool) []Project {
	out := []Project{}
	for _, id := range udb.Keys(projectTable) {
		var p Project
		if !udb.Get(projectTable, id, &p) {
			continue
		}
		p.Kind = KindClone // a record in a user store is a clone by construction
		p.Owner = user
		out = append(out, p)
	}
	for _, id := range T.DB.Keys(projectTable) {
		var p Project
		if !T.DB.Get(projectTable, id, &p) {
			continue
		}
		p.Kind = KindRoot
		if !p.reachableBy(user, isAdmin) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == KindRoot // real trees first — they're the operator's
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// reachableBy answers whether user may open this root project. Admins always
// may; anyone else must be named. An unlisted user gets the same answer as a
// nonexistent project, so a listing discloses nothing.
func (p Project) reachableBy(user string, isAdmin bool) bool {
	if p.Kind != KindRoot {
		return true // clones are only ever read out of their owner's own store
	}
	if isAdmin {
		return true
	}
	for _, u := range p.AllowedUsers {
		if strings.EqualFold(strings.TrimSpace(u), user) {
			return true
		}
	}
	return false
}

// findProject resolves one project by id for this caller, checking their own
// store first and then the deployment's. Reports found=false both for a
// missing project and for one this caller may not reach.
func (T *Anvil) findProject(udb Database, user string, isAdmin bool, id string) (Project, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Project{}, false
	}
	var p Project
	if udb.Get(projectTable, id, &p) {
		p.Kind = KindClone
		p.Owner = user
		return p, true
	}
	if T.DB.Get(projectTable, id, &p) {
		p.Kind = KindRoot
		if !p.reachableBy(user, isAdmin) {
			return Project{}, false
		}
		return p, true
	}
	return Project{}, false
}

// Reasoning modes.
const (
	ReasoningStandard = "standard"
	ReasoningQuick    = "quick"
	ReasoningLead     = "lead"
)

// quickThinkBudget is the reduced budget "quick" runs under. Below the
// deployment default rather than above it, because a per-agent budget can only
// lower the admin ceiling — see Project.Reasoning.
const quickThinkBudget = 512

// applyReasoning stamps this project's reasoning choice onto the agent record
// for one turn.
//
// LeadModel is OR'd rather than assigned: an agent already configured to
// reason on the lead keeps doing so, so a project that simply has not asked
// for it cannot quietly downgrade a decision made elsewhere. The privacy gates
// still outrank both — a ForcePrivate agent or a private turn stays local
// whatever is set here, because that is a question about where the
// conversation may GO, not about how well it should think.
func (p Project) applyReasoning(leadModel bool, thinkBudget int) (bool, int) {
	switch p.Reasoning {
	case ReasoningLead:
		return true, thinkBudget
	case ReasoningQuick:
		// Only lower it. A project asking to be quick must not raise a budget
		// somebody deliberately set lower.
		if thinkBudget == 0 || thinkBudget > quickThinkBudget {
			thinkBudget = quickThinkBudget
		}
		return leadModel, thinkBudget
	default:
		return leadModel, thinkBudget
	}
}

// Lines is a list of strings that a form may send as one newline-separated
// textarea value.
//
// It exists because the two shapes disagreed and the disagreement was fatal
// rather than lossy. A textarea posts a STRING; the field is a LIST; and
// encoding/json refuses the whole document on a type mismatch — so filling in
// Extra roots or Also-allow did not save those fields badly, it made the
// entire project submission fail with "bad request" and no indication which
// field was responsible. Both of them had therefore never worked.
//
// Accepting either shape here rather than adding a translation step in the
// handler, because the handler is not the only decoder: stored records, the
// API, and any future importer all read the same struct, and a fix that lives
// in one caller is a fix the next caller does not get.
type Lines []string

// UnmarshalJSON accepts a JSON array, or a string split on newlines.
func (l *Lines) UnmarshalJSON(b []byte) error {
	var asList []string
	if err := json.Unmarshal(b, &asList); err == nil {
		*l = trimmedNonEmpty(asList)
		return nil
	}
	var asText string
	if err := json.Unmarshal(b, &asText); err != nil {
		return fmt.Errorf("expected a list or newline-separated text, got %s", string(b))
	}
	// Commas too: a single-line textarea holding "a, b" is what people type
	// when the box is one row tall, and refusing it teaches nothing.
	fields := strings.FieldsFunc(asText, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	*l = trimmedNonEmpty(fields)
	return nil
}

// MarshalJSON emits a plain array, so the stored shape and the API stay a
// list — the textarea spelling is an INPUT convenience, not a storage format.
func (l Lines) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]string(l))
}

func trimmedNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
