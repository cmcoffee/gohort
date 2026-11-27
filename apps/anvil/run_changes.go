package anvil

// What a command changed. The file tools record every write with its
// pre-image (edits.go), but a command the agent ran (go mod tidy, a code
// generator, a sed, npm install rewriting a lockfile) changed files nobody
// recorded: no card, no Revert, and Undo on the message put back only the
// file tools' part, leaving the tree half one state and half another.
//
// So the run tool looks at git's status before and after the command and
// records what the command changed, through the same journal the file tools
// use. A pre-image comes from the disk for a file already changed before the
// command (read just before it ran) and from HEAD for one that was clean
// (git said the file was HEAD's content). What git ignores (build output,
// caches, node_modules) is not looked at, which is what keeps this cheap.
//
// What it will not record, and says so rather than recording wrongly: a file
// over the journal's size limit, a binary file, one whose HEAD content may
// not be what was on disk (a filter, such as LFS, or line-ending
// conversion), and a command that changed more than maxRunRecord files. A
// project outside git records nothing, as before.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sandbox"
)

const (
	// maxRunWatch is how many changed files a tree may have before a
	// command and still be watched: each is read to keep its pre-image.
	maxRunWatch = 400
	// maxRunRecord is how many files one command may change and have them
	// recorded one by one.
	maxRunRecord = 100
)

// preImage is a file as it was before the command.
type preImage struct {
	exists  bool
	content string
	size    int64
	mod     time.Time
	// why is set when the file cannot be recorded, and says why.
	why string
}

// runWatch is the tree's state before one command.
type runWatch struct {
	c *toolCtx
	// turn is the context of the tool call that started the command. Once
	// it is over (the command went to the background and its turn ended)
	// what it changed is recorded without cards: that turn's page is gone.
	turn   context.Context
	start  time.Time
	prefix string              // the project's folder within the repository
	before map[string]preImage // files changed before the command, by project path
}

// gitPrefix is where the project's folder sits in its repository, and
// whether it is in one at all. Asked once per turn.
func (c *toolCtx) gitPrefix(ctx context.Context) (string, bool) {
	c.prefixOnce.Do(func() {
		if !gitAvailable() {
			return
		}
		out, err := gitOut(ctx, c.base, "", "rev-parse", "--show-prefix")
		if err != nil {
			return
		}
		c.prefix, c.inRepo = strings.TrimSpace(string(out)), true
	})
	return c.prefix, c.inRepo
}

// watchRun reads the tree's changed files before a command. Nil when there
// is nothing to watch with: no repository, git failing, or a tree with more
// changes than maxRunWatch.
func (c *toolCtx) watchRun(ctx context.Context) *runWatch {
	prefix, ok := c.gitPrefix(ctx)
	if !ok {
		return nil
	}
	w := &runWatch{c: c, start: time.Now(), prefix: prefix, before: map[string]preImage{}}
	dirty, err := w.status(ctx)
	if err != nil || len(dirty) > maxRunWatch {
		return nil
	}
	for rel := range dirty {
		w.before[rel] = c.readPre(rel)
	}
	return w
}

// readPre reads one file as it is now.
func (c *toolCtx) readPre(rel string) preImage {
	abs := filepath.Join(c.base, filepath.FromSlash(rel))
	st, err := os.Lstat(abs)
	if os.IsNotExist(err) {
		return preImage{}
	}
	if err != nil {
		return preImage{exists: true, why: "unreadable"}
	}
	p := preImage{exists: true, size: st.Size(), mod: st.ModTime()}
	switch {
	case !st.Mode().IsRegular():
		p.why = "not a regular file"
	case st.Size() > maxJournalBytes:
		p.why = "larger than " + HumanSize(maxJournalBytes)
	default:
		b, err := os.ReadFile(abs)
		switch {
		case err != nil:
			p.why = "unreadable"
		case isBinary(b):
			p.why = "binary"
		default:
			p.content = string(b)
		}
	}
	return p
}

// status is git's porcelain status under the project's folder: project path
// to its two-letter code.
func (w *runWatch) status(ctx context.Context) (map[string]string, error) {
	out, err := gitOut(ctx, w.c.base, "", "status", "--porcelain=v1", "-z",
		"--untracked-files=all", "--no-renames", "--ignore-submodules=all", "--", ".")
	if err != nil {
		return nil, err
	}
	got := map[string]string{}
	for _, rec := range strings.Split(string(out), "\x00") {
		if len(rec) < 4 || rec[2] != ' ' {
			continue
		}
		rel, ok := strings.CutPrefix(rec[3:], w.prefix)
		if !ok || rel == "" {
			continue
		}
		got[rel] = rec[:2]
	}
	return got, nil
}

// change is one file the command changed.
type change struct {
	rel  string
	pre  preImage
	head bool // the pre-image is HEAD's, still to be read
}

// finish compares the tree after the command with before it, records each
// file the command changed, and returns what to tell the agent about them.
func (w *runWatch) finish(ctx context.Context) string {
	c := w.c
	after, err := w.status(ctx)
	if err != nil {
		return ""
	}
	paths := map[string]bool{}
	for rel := range w.before {
		paths[rel] = true
	}
	for rel := range after {
		paths[rel] = true
	}
	var changes []change
	for rel := range paths {
		if c.wroteSince(rel, w.start) {
			continue // a file tool wrote it while the command ran, and recorded it
		}
		if pre, ok := w.before[rel]; ok {
			if pre.why != "" {
				if now := c.readPre(rel); now.exists == pre.exists && now.size == pre.size && now.mod.Equal(pre.mod) {
					continue
				}
			} else if now := c.readPre(rel); now.why == "" && now.exists == pre.exists && now.content == pre.content {
				continue
			}
			changes = append(changes, change{rel: rel, pre: pre})
			continue
		}
		if after[rel] == "??" {
			// New to git's status. Made by the command, unless it was
			// there before and only just stopped being ignored.
			// The margin is for filesystems that keep coarse times.
			if st, err := os.Lstat(filepath.Join(c.base, filepath.FromSlash(rel))); err == nil && st.ModTime().Before(w.start.Add(-2*time.Second)) {
				continue
			}
			changes = append(changes, change{rel: rel})
			continue
		}
		changes = append(changes, change{rel: rel, head: true, pre: preImage{exists: true}})
	}
	if len(changes) == 0 {
		return ""
	}
	// Two commands ending at once must not both claim one file: the check
	// above and the recording below happen under one lock per project.
	unlock := lockProjectRecording(c.project.ID)
	defer unlock()
	sort.Slice(changes, func(i, j int) bool { return changes[i].rel < changes[j].rel })
	if len(changes) > maxRunRecord {
		return fmt.Sprintf("\n\nThis command changed %d files in the project, too many to record one by one, so they cannot be reverted from Anvil. git status shows them.", len(changes))
	}
	w.readHead(ctx, changes)

	var recorded, skipped []string
	for _, ch := range changes {
		now := c.readPre(ch.rel)
		why := ch.pre.why
		if why == "" {
			why = now.why
		}
		if why != "" {
			skipped = append(skipped, ch.rel+" ("+why+")")
			continue
		}
		if !ch.pre.exists && !now.exists || ch.pre.exists == now.exists && ch.pre.content == now.content {
			continue
		}
		if c.wroteSince(ch.rel, w.start) {
			continue // recorded meanwhile by another command
		}
		e := journalWrite(c.udb, c.project.ID, c.session(), ch.rel, ch.pre.content, ch.pre.exists, now.content, !now.exists)
		if w.turn != nil && w.turn.Err() != nil {
			c.noteWrite(e.Path)
		} else {
			c.publish(e)
		}
		switch e.Kind {
		case EditCreate:
			recorded = append(recorded, fmt.Sprintf("%s (new, +%d)", ch.rel, e.Added))
		case EditDelete:
			recorded = append(recorded, ch.rel+" (deleted)")
		default:
			recorded = append(recorded, fmt.Sprintf("%s (+%d -%d)", ch.rel, e.Added, e.Removed))
		}
	}
	var b strings.Builder
	if len(recorded) > 0 {
		b.WriteString("\n\nThis command changed files in the project, each recorded so it can be reverted: " + strings.Join(recorded, ", ") + ".")
	}
	if len(skipped) > 0 {
		b.WriteString("\n\nIt also changed files Anvil could not record, which cannot be reverted from here: " + strings.Join(skipped, ", ") + ".")
	}
	return b.String()
}

// readHead fills in the pre-image of each change whose file was HEAD's
// content before the command. One whose HEAD content may differ from what
// was on disk (a filter or line-ending conversion applies) is marked as not
// recordable instead.
func (w *runWatch) readHead(ctx context.Context, changes []change) {
	var want []*change
	for i := range changes {
		if !changes[i].head {
			continue
		}
		if strings.ContainsAny(changes[i].rel, "\n\r") {
			changes[i].pre.why = "unusual name"
			continue
		}
		want = append(want, &changes[i])
	}
	if len(want) == 0 {
		return
	}
	fail := func(why string) {
		for _, ch := range want {
			ch.pre.why = why
		}
	}
	if out, err := gitOut(ctx, w.c.base, "", "config", "--get", "core.autocrlf"); err == nil {
		if v := strings.TrimSpace(string(out)); v == "true" || v == "input" {
			fail("line endings converted by git")
			return
		}
	}
	args := []string{"check-attr", "-z", "filter", "text", "eol", "--"}
	for _, ch := range want {
		args = append(args, ch.rel)
	}
	out, err := gitOut(ctx, w.c.base, "", args...)
	if err != nil {
		fail("git could not say how it stores it")
		return
	}
	converted := map[string]bool{}
	f := strings.Split(string(out), "\x00")
	for i := 0; i+2 < len(f); i += 3 {
		if f[i+2] != "unspecified" {
			converted[f[i]] = true
		}
	}
	var stdin strings.Builder
	var read []*change
	for _, ch := range want {
		if converted[ch.rel] {
			ch.pre.why = "converted by a git filter or attribute"
			continue
		}
		stdin.WriteString("HEAD:./" + ch.rel + "\n")
		read = append(read, ch)
	}
	if len(read) == 0 {
		return
	}
	blobs, err := gitOut(ctx, w.c.base, stdin.String(), "cat-file", "--batch")
	if err != nil {
		for _, ch := range read {
			ch.pre.why = "its earlier content could not be read"
		}
		return
	}
	for i, ch := range read {
		content, rest, ok := nextBlob(blobs)
		if !ok {
			// The output is out of step from here on.
			for _, later := range read[i:] {
				later.pre.why = "its earlier content could not be read"
			}
			return
		}
		blobs = rest
		switch {
		case len(content) > maxJournalBytes:
			ch.pre.why = "larger than " + HumanSize(maxJournalBytes)
		case isBinary(content):
			ch.pre.why = "binary"
		default:
			ch.pre.content = string(content)
		}
	}
}

// nextBlob reads one object from git cat-file --batch output: its content
// and the output after it. Not ok for a missing object or output that is not
// in the batch format.
func nextBlob(out []byte) (content, rest []byte, ok bool) {
	nl := bytes.IndexByte(out, '\n')
	if nl < 0 {
		return nil, nil, false
	}
	head := strings.Fields(string(out[:nl]))
	if len(head) != 3 || head[1] != "blob" {
		return nil, nil, false
	}
	size, err := strconv.Atoi(head[2])
	if err != nil || size < 0 || nl+1+size+1 > len(out) || out[nl+1+size] != '\n' {
		return nil, nil, false
	}
	return out[nl+1 : nl+1+size], out[nl+1+size+1:], true
}

// gitOut runs git in dir and returns what it printed on stdout alone, byte
// for byte, with stdin given to it. Where the run tool can write the
// repository it runs in the same sandbox, as runGitContext does.
func gitOut(ctx context.Context, dir, stdin string, args ...string) ([]byte, error) {
	if gitNeedsNetwork(args) {
		return nil, fmt.Errorf("git %s reaches a remote", args[0])
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append(append([]string(nil), gitSafety...), args...)
	if !gitWritableByRun(dir) {
		cmd := exec.CommandContext(ctx, "git", full...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), gitQuietEnv...)
		cmd.Stdin = strings.NewReader(stdin)
		return cmd.Output()
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var cmd strings.Builder
	cmd.WriteString("printf '%s' " + shellQuote(stdin) + " | git")
	for _, a := range full {
		cmd.WriteString(" " + shellQuote(a))
	}
	cmd.WriteString(" 2>/dev/null")
	env := map[string]string{}
	for _, kv := range gitQuietEnv {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: cmd.String(), WorkspaceDir: abs, Env: env})
	return []byte(res.Output), res.Err
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// What was written in each project and when, by file tools and by commands
// recording, kept across turns: a command that went to the background ends
// after its turn, and a write a later turn made meanwhile is that turn's,
// not the command's. Pruned as it grows.
var (
	writesMu sync.Mutex
	writes   = map[string]time.Time{} // project ID + "\x00" + path
	// recordMu holds one lock per project for a command's recording.
	recordMu    sync.Mutex
	recordLocks = map[string]*sync.Mutex{}
)

// noteWrite records that rel was written now, so a command running at the
// same time does not record the write as its own.
func (c *toolCtx) noteWrite(rel string) {
	writesMu.Lock()
	defer writesMu.Unlock()
	if len(writes) > 10000 {
		for k, at := range writes {
			if time.Since(at) > time.Hour {
				delete(writes, k)
			}
		}
	}
	writes[c.project.ID+"\x00"+rel] = time.Now()
}

// wroteSince reports whether rel was written at or after t.
func (c *toolCtx) wroteSince(rel string, t time.Time) bool {
	writesMu.Lock()
	defer writesMu.Unlock()
	at, ok := writes[c.project.ID+"\x00"+rel]
	return ok && !at.Before(t)
}

// lockProjectRecording takes the project's recording lock and returns its
// release.
func lockProjectRecording(projectID string) func() {
	recordMu.Lock()
	m := recordLocks[projectID]
	if m == nil {
		m = &sync.Mutex{}
		recordLocks[projectID] = m
	}
	recordMu.Unlock()
	m.Lock()
	return m.Unlock
}
