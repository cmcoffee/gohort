// Managed python dependency provisioning for sandboxed scripts.
//
// The sandbox binds /usr read-only and (for script/pipe invocations)
// runs --unshare-net, so an LLM-authored python script can NEVER pip
// install anything from inside: site-packages is read-only and there's
// no network. Any third-party library a generator needs (openpyxl,
// python-docx, python-pptx, pandas, ...) has to be installed HOST-side,
// by the gohort process, into a directory the sandbox then binds RO.
//
// This mirrors the gohort-helper mechanism in sandbox_hook.go exactly:
// a host dir living OUTSIDE any workspace, bind-mounted read-only into
// every sandbox with PYTHONPATH pointing at it. The difference is only
// what populates it — EnsureGohortLibDir writes an embedded __init__.py,
// EnsurePyDeps shells out to `pip install --target`.
//
// Trust posture: fetching packages from PyPI is a privileged host
// operation (network + write). The sandbox only ever consumes the
// frozen result read-only. Callers gate WHICH packages may be installed
// (an allowlist, a manifest declared by a skill/tool/export-format);
// this file just does the install idempotently and reports the result.

package deps

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

// SandboxPyDepsMountPath is the in-sandbox path where the managed
// python-deps dir is bind-mounted (read-only). Scripts get this path in
// PYTHONPATH so `import openpyxl` resolves against the managed install
// rather than the (empty) system site-packages.
const SandboxPyDepsMountPath = "/opt/gohort-pydeps"

// pyDepsInstalledMarker is the filename (inside the deps dir) that
// records which pip specs have already been installed, one per line.
// Keyed on the exact spec string the caller passed, so "python-docx"
// and "python-docx==1.1.2" are tracked as distinct entries — a version
// bump reinstalls rather than being silently skipped.
const pyDepsInstalledMarker = ".installed"

var (
	// pyDepsDirMu guards only the cached dir path — held for microseconds.
	// This is the lock EnsurePyDepsDir() takes, and that function runs on
	// every sandbox launch (bwrapArgv/bwrapScriptArgv), so it must never
	// be held across anything slow.
	pyDepsDirMu   sync.Mutex
	pyDepsDirPath string
	// pyInstallMu serializes pip installs and the install-marker read/write.
	// Deliberately SEPARATE from pyDepsDirMu so a cold pip run (up to
	// pyInstallLimit) can't block the dir-resolution every sandboxed shell
	// command needs — otherwise a first-time provisioning would stall every
	// other sandbox launch process-wide for minutes.
	pyInstallMu    sync.Mutex
	pyInstallLimit = 5 * time.Minute // generous: cold pip + wheel build
)

// EnsurePyDepsDir returns the host-side directory that holds the managed
// python packages, creating it if needed. It is a sibling of
// WorkspacesDir named "_gohort_pydeps" (the "_" prefix keeps it out of
// the valid-user-id namespace, same trick EnsureGohortLibDir uses), and
// is the target of `pip install --target` — so packages land directly
// under it (e.g. <dir>/openpyxl/) and PYTHONPATH points here.
//
// Returns "" when WorkspacesDir is unset or the mkdir fails; callers
// then skip the bind and the script's import fails loudly, which is the
// right shape.
func EnsurePyDepsDir() string {
	pyDepsDirMu.Lock()
	defer pyDepsDirMu.Unlock()
	return ensurePyDepsDirLocked()
}

func ensurePyDepsDirLocked() string {
	if pyDepsDirPath != "" {
		return pyDepsDirPath
	}
	base := workspacesDir()
	if base == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(base), "_gohort_pydeps")
	if err := os.MkdirAll(dir, 0755); err != nil {
		nfo.Debug("[pydeps] failed to mkdir %s: %v", dir, err)
		return ""
	}
	pyDepsDirPath = dir
	return dir
}

// EnsurePyDeps installs the given pip specs into the managed deps dir,
// skipping any already recorded in the install marker. Idempotent and
// safe to call on every use of a generator: once a spec is installed it
// costs a marker read, not a pip run.
//
// Runs HOST-side (never sandboxed) so pip has the network and write
// access it needs. Blocks until pip finishes or pyInstallLimit elapses.
// Returns nil when every requested spec is present (already or freshly
// installed); a non-nil error carries pip's combined output so the
// caller can surface why provisioning failed.
//
// Callers are responsible for governance — validate specs against an
// allowlist before calling. This function trusts its inputs.
//
// ctx bounds the pip run: pass the request/turn context so a cancelled
// request aborts a cold install rather than letting it run to the
// pyInstallLimit. Nil is accepted (treated as context.Background).
func EnsurePyDeps(ctx context.Context, specs ...string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	specs = dedupeNonEmpty(specs)
	if len(specs) == 0 {
		return nil
	}

	// Resolve the dir under its own fast lock, then serialize the install
	// under pyInstallMu — never hold pyDepsDirMu across the pip run.
	dir := EnsurePyDepsDir()
	if dir == "" {
		return errors.New("pydeps: WorkspacesDir unset — cannot provision python packages")
	}

	pyInstallMu.Lock()
	defer pyInstallMu.Unlock()

	installed := readInstalledMarker(dir)
	var missing []string
	for _, s := range specs {
		if !installed[s] {
			missing = append(missing, s)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	if _, err := exec.LookPath("python3"); err != nil {
		return errors.New("pydeps: python3 not found on PATH — cannot provision packages")
	}

	ctx, cancel := context.WithTimeout(ctx, pyInstallLimit)
	defer cancel()

	// --upgrade is required for the version-pinning contract: pip install
	// --target skips a package whose dir already exists UNLESS --upgrade
	// is set. Without it, provisioning "openpyxl==X" after a bare
	// "openpyxl" is already present would exit 0 without replacing the
	// bytes, and the marker would then record the pinned spec as
	// satisfied while the old version stays on disk. --upgrade makes the
	// on-disk install actually match the requested spec.
	args := append([]string{
		"-m", "pip", "install",
		"--target", dir,
		"--upgrade",
		"--no-input",
		"--disable-pip-version-check",
	}, missing...)
	nfo.Debug("[pydeps] installing %v into %s", missing, dir)
	cmd := exec.CommandContext(ctx, "python3", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		trimmed := strings.TrimSpace(out.String())
		if ctx.Err() == context.DeadlineExceeded {
			return errors.New("pydeps: pip install " + strings.Join(missing, " ") + " timed out after " + pyInstallLimit.String())
		}
		return errors.New("pydeps: pip install " + strings.Join(missing, " ") + " failed: " + tailLines(trimmed, 12))
	}

	appendInstalledMarker(dir, missing)
	nfo.Log("[pydeps] provisioned python packages: %s", strings.Join(missing, ", "))
	return nil
}

// PyDepsAvailable reports whether the managed deps dir currently records
// every given spec as installed, WITHOUT triggering an install. Cheap
// pre-check for callers that want to branch (e.g. show "provisioning…"
// only when something is actually missing).
func PyDepsAvailable(specs ...string) bool {
	specs = dedupeNonEmpty(specs)
	if len(specs) == 0 {
		return true
	}
	dir := EnsurePyDepsDir()
	if dir == "" {
		return false
	}
	pyInstallMu.Lock()
	defer pyInstallMu.Unlock()
	installed := readInstalledMarker(dir)
	for _, s := range specs {
		if !installed[s] {
			return false
		}
	}
	return true
}

// readInstalledMarker loads the set of already-installed specs. Missing
// marker file = empty set (nothing installed yet), not an error.
func readInstalledMarker(dir string) map[string]bool {
	set := map[string]bool{}
	f, err := os.Open(filepath.Join(dir, pyDepsInstalledMarker))
	if err != nil {
		return set
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			set[line] = true
		}
	}
	return set
}

// appendInstalledMarker records freshly-installed specs. Best-effort:
// a write failure only means the next call re-runs pip (which is
// idempotent), so it's logged at Debug rather than surfaced.
func appendInstalledMarker(dir string, specs []string) {
	f, err := os.OpenFile(filepath.Join(dir, pyDepsInstalledMarker),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		nfo.Debug("[pydeps] could not update install marker: %v", err)
		return
	}
	defer f.Close()
	for _, s := range specs {
		f.WriteString(s + "\n")
	}
}

// PrependPythonPath returns a PYTHONPATH value with the given mount
// paths prepended to any existing value, deduped and order-preserving.
// A mount already present in existing isn't added twice, so repeated
// calls stay stable.
func PrependPythonPath(existing string, mounts ...string) string {
	var parts []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		parts = append(parts, p)
	}
	for _, m := range mounts {
		add(m)
	}
	for _, p := range strings.Split(existing, ":") {
		add(strings.TrimSpace(p))
	}
	return strings.Join(parts, ":")
}

// dedupeNonEmpty trims, drops empties, and removes duplicates while
// preserving first-seen order.
func dedupeNonEmpty(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// tailLines returns at most the last n lines of s, for compact error
// surfacing (pip dumps a lot; the actionable line is near the end).
func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return "...\n" + strings.Join(lines[len(lines)-n:], "\n")
}
