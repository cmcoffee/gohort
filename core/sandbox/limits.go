// What a confined command may CONSUME, as opposed to what it may reach.
//
// The rest of this package answers "what can this command touch" and answers it
// well. It has never answered "how much can it take", and the gap was total:
// there was no rlimit, no cgroup bound, and no quota anywhere in the tree.
// --unshare-cgroup-try creates a cgroup and sets nothing inside it. An
// LLM-authored `while :; do :; done`, a runaway pip install, or a
// `yes > file` filled the host's disk or pinned a core, inside a sandbox that
// was working exactly as designed. Confinement is about blast RADIUS; this is
// about blast DURATION, and they are different questions.
//
// WHY ulimit RATHER THAN cgroups OR prlimit. The limit has to apply under three
// backends on two platforms, and the one thing all of them already have in
// common is that they end in a POSIX shell. `ulimit` is a shell builtin, so it
// needs nothing installed (prlimit is util-linux, absent on macOS), needs no
// delegated cgroup subtree (which an unprivileged daemon does not have), and is
// inherited by every child the command spawns. It is also one-way: an
// unprivileged process can lower a limit and can never raise it again, so the
// LLM cannot undo this by putting its own `ulimit` in front of the command.
//
// WHAT IS ON BY DEFAULT, AND WHY SO LITTLE. gohort has legitimate workloads
// that consume real resources — tools/video/transcode.go runs ffmpeg through
// RunSandboxedShell with a five-minute wall clock, which on a many-core host is
// a lot of CPU-seconds and a large output file. A default that broke transcode
// would be discovered as "video tools stopped working" weeks later, by someone
// with no reason to suspect a sandbox setting. So the defaults bound the
// runaway cases that no legitimate workload needs (a single enormous file, an
// unbounded fd table) and leave CPU, address space and process count OFF until
// an operator asks, because each of those has a real workload behind it.
//
// The knobs matter more than the defaults here: an operator running untrusted
// agents on a shared box can turn all five on, and until this file existed they
// could not.
package sandbox

import (
	"os"
	"strconv"
	"strings"
)

// Limits is the resource ceiling for one sandboxed command. Zero means
// unlimited for every field, which is also what an unset env var parses to, so
// "not configured" and "explicitly unlimited" are the same state and neither
// needs a pointer to express.
type Limits struct {
	// FileSizeMB caps a single file the command writes (RLIMIT_FSIZE).
	// Per-file, not per-workspace: it stops `yes > f` and `dd if=/dev/zero`,
	// which is the common runaway, and does not pretend to be a disk quota.
	FileSizeMB int
	// OpenFiles caps the fd table (RLIMIT_NOFILE).
	OpenFiles int
	// CPUSeconds caps CPU time, not wall time (RLIMIT_CPU). SIGXCPU at the
	// soft limit, SIGKILL at the hard one.
	//
	// Off by default because every one-shot path already has a wall clock
	// (90s for temp tools and workspace runs, 5min for transcode) and CPU
	// seconds on a many-core host are not comparable to it. It is the one
	// limit worth turning on for a deployment that uses persistent shells,
	// which is the only shape with no wall clock at all: an idle psql
	// session spends no CPU, so a generous cap costs it nothing and still
	// kills a spin loop that would otherwise run until the process dies.
	CPUSeconds int
	// MemoryMB caps the address space (RLIMIT_AS).
	//
	// Off by default because RLIMIT_AS bounds VIRTUAL memory, and runtimes
	// that reserve large address ranges without committing them — the Go
	// runtime, the JVM, numpy — die under a limit that looks generous
	// against their actual RSS. An operator who sets this should expect to
	// tune it. Not enforced on macOS at all: Darwin ignores RLIMIT_AS.
	MemoryMB int
	// MaxProcs caps processes (RLIMIT_NPROC), the fork-bomb guard.
	//
	// Off by default and the most dangerous of the five, because NPROC is
	// counted per-UID rather than per-process-tree: the sandboxed command
	// runs as the same user as the daemon, so the daemon's own threads
	// count against this number, and a value set too low makes the DAEMON
	// unable to fork rather than the command. Set it well above the
	// daemon's steady-state or leave it off.
	MaxProcs int
}

// Any reports whether anything is actually capped, so callers can skip the
// prefix entirely rather than emit a no-op that shows up in every log of a
// command that has no limits.
func (l Limits) Any() bool {
	return l.FileSizeMB > 0 || l.OpenFiles > 0 || l.CPUSeconds > 0 || l.MemoryMB > 0 || l.MaxProcs > 0
}

// Default limits. See the file comment for why CPU, memory and process count
// are absent: each has a real gohort workload behind it.
const (
	// defaultFileSizeMB is far above any legitimate single output —
	// transcode's largest realistic result is a fraction of it — and far
	// below the point where filling a disk is quick.
	defaultFileSizeMB = 8192
	// defaultOpenFiles is generous for a shell pipeline and well under the
	// typical 1024/4096 soft default, so a descriptor leak stops before it
	// reaches anything else on the host.
	defaultOpenFiles = 512
)

// resourceLimits reads the deployment's ceiling.
//
// Read per call rather than resolved once like activeSandbox, because unlike
// the backend these are not a property of the host: an operator tightening a
// limit should not have to restart the daemon, and a test should not have to
// reach around a sync.Once to vary one.
//
//	GOHORT_SANDBOX_MAX_FILE_MB    single file the command may write (default 8192)
//	GOHORT_SANDBOX_MAX_OPEN_FILES fd table size                     (default 512)
//	GOHORT_SANDBOX_MAX_CPU_SEC    CPU seconds                       (default off)
//	GOHORT_SANDBOX_MAX_MEM_MB     address space                     (default off)
//	GOHORT_SANDBOX_MAX_PROCS      processes, per-UID                (default off)
//
// Each accepts "0", "none" or "unlimited" to switch that limit off, so a
// deployment that needs to write a file larger than the default has a way to
// say so that does not involve turning off the other four.
func resourceLimits() Limits {
	return Limits{
		FileSizeMB: limitEnv("GOHORT_SANDBOX_MAX_FILE_MB", defaultFileSizeMB),
		OpenFiles:  limitEnv("GOHORT_SANDBOX_MAX_OPEN_FILES", defaultOpenFiles),
		CPUSeconds: limitEnv("GOHORT_SANDBOX_MAX_CPU_SEC", 0),
		MemoryMB:   limitEnv("GOHORT_SANDBOX_MAX_MEM_MB", 0),
		MaxProcs:   limitEnv("GOHORT_SANDBOX_MAX_PROCS", 0),
	}
}

// limitEnv parses one limit, falling back to def when unset.
//
// An unparseable value takes the DEFAULT rather than unlimited. This is the
// same rule bypassPolicy applies to a typo in a security switch: a mistake must
// not resolve to the permissive answer, and "MAX_MEM_MB=1gb" is a mistake.
func limitEnv(name string, def int) int {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if raw == "" {
		return def
	}
	switch raw {
	case "0", "none", "unlimited", "off":
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// shellPrefix renders the limits as `ulimit` calls to run before the command.
//
// Trailing separator included so the caller concatenates rather than deciding
// how to join. stderr is discarded per call: a shell that rejects one option
// must not print a diagnostic into output the LLM reads as the tool's result,
// and the other four should still apply.
//
// ON THE UNIT OF -f. POSIX specifies 512-byte blocks; bash uses 1024-byte
// blocks unless POSIXLY_CORRECT. So the effective cap is the nominal MB under
// bash and half that under dash. Both are the safe direction for a ceiling this
// far above any real output, and the alternative — probing the shell at
// startup — buys precision that a runaway-guard does not need.
func (l Limits) shellPrefix() string {
	if !l.Any() {
		return ""
	}
	var b strings.Builder
	add := func(flag string, val int) {
		if val <= 0 {
			return
		}
		b.WriteString("ulimit ")
		b.WriteString(flag)
		b.WriteString(" ")
		b.WriteString(strconv.Itoa(val))
		b.WriteString(" 2>/dev/null; ")
	}
	// Core dumps off, unconditionally, whenever anything else is capped.
	//
	// Not a knob, because nobody wants these: the signals a limit raises —
	// SIGXFSZ from -f, SIGXCPU from -t — are dump-by-default, so the mechanism
	// that just refused to let the command write 8MB would answer by writing a
	// core file of its own into the same workspace. Nothing in gohort reads it
	// and the LLM sees only the signal.
	//
	// Note the shell still PRINTS "(core dumped)" when it reports the death.
	// That wording comes from the wait status and is emitted whether or not a
	// dump was actually written; with RLIMIT_CORE at 0 none is. Verified by
	// asking `ulimit -c` inside a real bwrap sandbox rather than by reading
	// the message, which says the opposite of what is true.
	b.WriteString("ulimit -c 0 2>/dev/null; ")
	add("-f", l.FileSizeMB*1024) // -f is in blocks; see above
	add("-n", l.OpenFiles)
	add("-t", l.CPUSeconds)
	add("-v", l.MemoryMB*1024) // -v is in KiB
	add("-u", l.MaxProcs)
	return b.String()
}

// apply prefixes a shell command with its limits.
//
// The command is appended UNCHANGED and without `exec`. Both matter: the caller
// may pass a compound command ("build && test"), and `exec build && test` would
// replace the shell with build and never run test. The cost is one extra shell
// process per run, which is the correct trade against silently changing what a
// command means.
func (l Limits) apply(command string) string {
	p := l.shellPrefix()
	if p == "" {
		return command
	}
	return p + command
}

// Summary renders the ceiling as one line for the admin panel.
//
// Every field is named even when it is off, because the reader's question is
// "what is capped here", and a summary that lists only what is set answers a
// different one: it cannot be distinguished from a summary of a deployment
// where those knobs do not exist.
func (l Limits) Summary() string {
	part := func(label string, val int, unit string) string {
		if val <= 0 {
			return label + " unlimited"
		}
		return label + " " + strconv.Itoa(val) + unit
	}
	return strings.Join([]string{
		part("file", l.FileSizeMB, "MB"),
		part("fds", l.OpenFiles, ""),
		part("cpu", l.CPUSeconds, "s"),
		part("mem", l.MemoryMB, "MB"),
		part("procs", l.MaxProcs, ""),
	}, ", ")
}
