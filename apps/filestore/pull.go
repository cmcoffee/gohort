// Pulling files and folders from one store into another.
//
// The flow this serves: files land in a private working store, a command
// prepares what landed there (an extractor writing a prepared/ folder), and a
// second store, the one agents read, reaches across and copies it in. The
// working store is never attached to an agent: the link is set on the
// receiving store by an admin, and that link is the grant. An agent may pull
// an item by name (when an admin lets it), seeing the other store's item
// NAMES so it can, never their contents.
//
// An item is anything at the top of the source store: a folder (copied whole,
// or just its prepared part when the store is set up that way) or a file.
// A copy, not a move: the source keeps its own until its lifetime or
// retention removes it.
//
// Nothing half-copied is ever visible. The copy goes into a hidden staging
// name (.incoming-<item>) and is renamed into place only when every file is
// there. A copy that is stopped, for any reason, removes its staging copy.
// It is refused up front when the destination has no room under its cap or
// the disk's floor, it checks the floor again as it goes, and both sides are
// held (pathhold) so neither store's sweep can take them mid-copy.
package filestore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/diskguard"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// defaultPullSubpath is the part of a folder a pull takes when the admin did not
// say: the extractor's output, not the raw download beside it.
const defaultPullSubpath = "prepared"

// stagingPrefix names the hidden folder a pull copies into before it is
// renamed into place. Folder listings skip dot-folders, so it never reads as
// a case.
const stagingPrefix = ".incoming-"

// pullSubpath is the part of each folder this store's pull takes, "" for the
// whole folder.
func (s Store) pullSubpath() string {
	p := strings.Trim(strings.TrimSpace(s.PullSubpath), "/")
	switch p {
	case "":
		return defaultPullSubpath
	case ".":
		return ""
	}
	return p
}

// pullSource is the store this one pulls from, when a link is set and the
// store still exists.
func (s Store) pullSource(db Database) (Store, bool) {
	slug := strings.TrimSpace(s.PullFrom)
	if slug == "" || slug == s.Slug {
		return Store{}, false
	}
	return LoadStore(db, slug)
}

// validatePull refuses a pull link that cannot work, at save time.
func validatePull(db Database, s Store) error {
	if strings.TrimSpace(s.PullFrom) == "" {
		return nil
	}
	if s.PullFrom == s.Slug {
		return Error("a store cannot pull from itself")
	}
	if _, ok := LoadStore(db, s.PullFrom); !ok {
		return Error("the store to pull from does not exist: " + s.PullFrom)
	}
	sub := strings.Trim(strings.TrimSpace(s.PullSubpath), "/")
	if sub != "" && sub != "." {
		if filepath.IsAbs(sub) || strings.Contains(sub, "..") || filepath.Clean(sub) != sub {
			return Error("the part of each folder to copy must be a plain folder name inside it, like prepared: " + s.PullSubpath)
		}
	}
	return nil
}

// pullJob is one item (a file or a folder) being copied from src into dest.
type pullJob struct {
	dest, src Store
	item      string
	replace   bool
	user      string
	// db, when set, is where a landed pull's evidence index is recorded
	// (noteChanged). Nil in a test that only copies.
	db Database

	// What there is to copy. Atomic: the copy sets them as it measures, and
	// the progress line reads them from the page's request at the same time.
	totalFiles, totalBytes atomic.Int64
	doneFiles, doneByte    atomic.Int64
}

func (p *pullJob) key() string { return p.dest.Slug + "/" + p.item }

// progress is the job's line for a person: "120 of 1,204 files, 310 MB of
// 3.2 GB".
func (p *pullJob) progress() string {
	if p.totalFiles.Load() == 0 {
		return "measuring " + p.item
	}
	return fmt.Sprintf("%s of %s files, %s of %s", commaN(p.doneFiles.Load()), commaN(p.totalFiles.Load()),
		HumanSize(p.doneByte.Load()), HumanSize(p.totalBytes.Load()))
}

func commaN(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// source is what the job copies, and whether it is a folder: the folder (or
// its prepared part), or a file at the top of the source store.
func (p *pullJob) source() (string, bool, error) {
	name := strings.TrimSpace(p.item)
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", false, fmt.Errorf("name a file or folder at the top of %s, one name with no path: %q", p.src.Name, p.item)
	}
	if dir, err := SubRoot(p.src.Path, name); err == nil {
		sub := p.dest.pullSubpath()
		if sub == "" {
			return dir, true, nil
		}
		part, err := SubRoot(dir, sub)
		if err != nil {
			return "", false, fmt.Errorf("%s/%s has no %s/ folder yet: it has not been prepared (run its extractor first)", p.src.Name, name, sub)
		}
		return part, true, nil
	}
	root, err := SubRoot(p.src.Path, "")
	if err != nil {
		return "", false, err
	}
	full := filepath.Join(root, name)
	// Lstat: a link is not followed, here or inside a folder; it could name
	// anything on the disk.
	if info, err := os.Lstat(full); err == nil && info.Mode().IsRegular() {
		return full, false, nil
	}
	return "", false, fmt.Errorf("%s has no file or folder called %q", p.src.Name, name)
}

// run copies the item. It returns a one-line summary for a person or an
// agent, or why it did not.
func (p *pullJob) run(ctx context.Context) (string, error) {
	from, isDir, err := p.source()
	if err != nil {
		return "", err
	}
	destRoot, err := SubRoot(p.dest.Path, "")
	if err != nil {
		return "", err
	}
	if !isDir {
		return p.runFile(ctx, from, destRoot)
	}
	final := filepath.Join(destRoot, p.item)
	staging := filepath.Join(destRoot, stagingPrefix+p.item)
	defer pathhold.HoldAll(from, final, staging)()

	// Measured first: the destination's room is checked against the real
	// size, and the progress line has a total to count toward.
	var files, size int64
	_ = filepath.WalkDir(from, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, ierr := d.Info(); ierr == nil {
				files++
				size += info.Size()
			}
		}
		return ctx.Err()
	})
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p.totalFiles.Store(files)
	p.totalBytes.Store(size)
	if err := p.dest.roomFor(size); err != nil {
		return "", err
	}

	os.RemoveAll(staging) // what a crashed earlier pull left
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", fmt.Errorf("cannot make the staging folder in %s: %w", p.dest.Name, err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(staging)
		}
		p.dest.wrote()
	}()
	skipped := 0
	err = filepath.WalkDir(from, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		to := filepath.Join(staging, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type().IsRegular():
			return p.copyFile(ctx, path, to)
		default:
			// A symlink is not followed: it could point anywhere on the
			// disk, and copying its target would carry out of Intake
			// whatever it named.
			skipped++
			return nil
		}
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "", Error("stopped; nothing was kept in " + p.dest.Name)
		}
		return "", fmt.Errorf("copying %s into %s stopped, and nothing was kept: %w", p.item, p.dest.Name, err)
	}
	_, statErr := os.Stat(final)
	exists := statErr == nil
	merged := ""
	switch {
	case exists && !p.replace:
		// The folder is already here, perhaps from an earlier pull of the
		// same folder: add to it. New files arrive, a file of the same path is
		// updated, and nothing already here is removed, so a file the
		// source has since expired stays.
		added, updated, err := mergeInto(staging, final)
		if err != nil {
			return "", fmt.Errorf("adding to %s in %s stopped partway (%d added, %d updated so far): %w", p.item, p.dest.Name, added, updated, err)
		}
		os.RemoveAll(staging)
		merged = fmt.Sprintf(" Added to the copy already there: %d new, %d updated, nothing removed.", added, updated)
	case exists:
		old := filepath.Join(destRoot, ".replaced-"+p.item)
		os.RemoveAll(old)
		if err := os.Rename(final, old); err != nil {
			return "", fmt.Errorf("could not move the old %s aside: %w", p.item, err)
		}
		defer os.RemoveAll(old)
		fallthrough
	default:
		if err := os.Rename(staging, final); err != nil {
			return "", fmt.Errorf("could not move the copy into place: %w", err)
		}
	}
	ok = true
	what := p.item
	if sub := p.dest.pullSubpath(); sub != "" {
		what += " (" + sub + "/)"
	}
	note := ""
	if skipped > 0 {
		note = fmt.Sprintf(" %d link(s) inside it were not followed.", skipped)
	}
	Log("[filestore.pull] %s copied %s from %s into %s: %s files, %s", p.user, what, p.src.Name, p.dest.Name, commaN(files), HumanSize(size))
	return fmt.Sprintf("Copied %s from %s into %s: %s files, %s. It is at %s/%s.%s%s",
		what, p.src.Name, p.dest.Name, commaN(files), HumanSize(size), p.dest.Name, p.item, merged, note), nil
}

// mergeInto moves every file under staging to the same place under final,
// replacing a file already at that path and leaving everything else in final
// alone. Each file arrives whole: a rename on the same disk.
func mergeInto(staging, final string) (added, updated int, err error) {
	err = filepath.WalkDir(staging, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		rel, _ := filepath.Rel(staging, path)
		to := filepath.Join(final, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		if info, serr := os.Lstat(to); serr == nil {
			if info.IsDir() {
				return fmt.Errorf("%s is a folder in the copy already there and a file in the new one", rel)
			}
			updated++
		} else {
			added++
		}
		return os.Rename(path, to)
	})
	return added, updated, err
}

// floorCheckEvery is how much a pull copies between looks at the disk's floor.
const floorCheckEvery = 256 << 20

// runFile copies one file from the top of the source store into the
// destination's top, staged and renamed into place like a folder. A file
// already there by that name is replaced: it is the same file, newer.
func (p *pullJob) runFile(ctx context.Context, from, destRoot string) (string, error) {
	final := filepath.Join(destRoot, p.item)
	staging := filepath.Join(destRoot, stagingPrefix+p.item)
	defer pathhold.HoldAll(from, final, staging)()
	if info, err := os.Lstat(final); err == nil && info.IsDir() {
		return "", fmt.Errorf("%s already has a folder called %s, so the file of that name cannot be copied there", p.dest.Name, p.item)
	}
	info, err := os.Stat(from)
	if err != nil {
		return "", err
	}
	p.totalFiles.Store(1)
	p.totalBytes.Store(info.Size())
	if err := p.dest.roomFor(info.Size()); err != nil {
		return "", err
	}
	os.Remove(staging)
	defer p.dest.wrote()
	if err := p.copyFile(ctx, from, staging); err != nil {
		os.Remove(staging)
		if errors.Is(err, context.Canceled) {
			return "", Error("stopped; nothing was kept in " + p.dest.Name)
		}
		return "", fmt.Errorf("copying %s into %s stopped, and nothing was kept: %w", p.item, p.dest.Name, err)
	}
	if err := os.Rename(staging, final); err != nil {
		os.Remove(staging)
		return "", fmt.Errorf("could not move the copy into place: %w", err)
	}
	Log("[filestore.pull] %s copied file %s from %s into %s: %s", p.user, p.item, p.src.Name, p.dest.Name, HumanSize(info.Size()))
	return fmt.Sprintf("Copied the file %s from %s into %s: %s. It is at %s/%s.", p.item, p.src.Name, p.dest.Name, HumanSize(info.Size()), p.dest.Name, p.item), nil
}

// copyFile copies one file in chunks, counting as it goes so a large file
// still moves the progress line, and stopping with the context.
func (p *pullJob) copyFile(ctx context.Context, from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	var sinceCheck int64
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			p.doneByte.Add(int64(n))
			if sinceCheck += int64(n); sinceCheck >= floorCheckEvery {
				sinceCheck = 0
				if err := diskguard.Check(to, 0); err != nil {
					out.Close()
					return err
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	p.doneFiles.Add(1)
	return out.Close()
}

// --- the running pulls ----------------------------------------------------

// pullRun is a pull in progress or recently ended, as the page and the agent
// tool see it.
type pullRun struct {
	job     *pullJob
	cancel  context.CancelFunc
	done    chan struct{}
	result  string
	err     error
	ended   time.Time
	started time.Time
}

// pullRegistry is the pulls this server is running, one per destination item,
// and the last outcome of each for a while after.
type pullRegistry struct {
	mu   sync.Mutex
	runs map[string]*pullRun
}

var pulls = &pullRegistry{runs: map[string]*pullRun{}}

// pullOutcomeKept is how long a finished pull's outcome stays to be read, so
// a page arriving after the end says how it went.
const pullOutcomeKept = 30 * time.Minute

// start runs job in the background, unless that item is already being
// pulled into that store, in which case the running one is returned.
func (r *pullRegistry) start(job *pullJob) (*pullRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.runs[job.key()]; ok && cur.ended.IsZero() {
		return cur, false
	}
	// Not the request's context: the copy outlives the click that started it.
	ctx, cancel := context.WithCancel(context.Background())
	run := &pullRun{job: job, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	r.runs[job.key()] = run
	go func() {
		defer close(run.done)
		defer cancel()
		res, err := job.run(ctx)
		r.mu.Lock()
		run.result, run.err, run.ended = res, err, time.Now()
		r.mu.Unlock()
		if err == nil && job.db != nil {
			noteChanged(job.db, job.dest, job.item)
		}
	}()
	return run, true
}

func (r *pullRegistry) get(key string) (*pullRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.runs[key]
	if ok && !run.ended.IsZero() && time.Since(run.ended) > pullOutcomeKept {
		delete(r.runs, key)
		return nil, false
	}
	return run, ok
}

// status is the run's {"progress"} while it goes or {"outcome"} once ended.
func (run *pullRun) status() map[string]any {
	pulls.mu.Lock()
	ended, res, err := run.ended, run.result, run.err
	pulls.mu.Unlock()
	if ended.IsZero() {
		return map[string]any{"progress": run.job.progress() + " - " + shortSince(run.started)}
	}
	if err != nil {
		return map[string]any{"outcome": "Failed: " + err.Error()}
	}
	return map[string]any{"outcome": res}
}

func shortSince(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
