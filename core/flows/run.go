package flows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/sandbox"
)

// Bounds on one run: what goes in, what comes back, and how much of what the
// command prints is kept for the admin to read.
const (
	maxInput  = 256 << 20
	maxResult = 64 << 20
	keepLog   = 4000
)

// runs bounds how many flows run at once: each is a process, and a burst of
// uploads should not start a burst of converters.
var runs = make(chan struct{}, 4)

// base is where a run's folder is made. Not the system temp folder: the
// sandbox mounts a fresh, empty /tmp of its own, which would hide a folder
// made there before the command could start in it. core sets it under the
// workspaces folder (SetWorkBase).
var (
	baseMu sync.RWMutex
	base   string
)

// SetWorkBase sets the folder runs are made in.
func SetWorkBase(dir string) {
	baseMu.Lock()
	base = dir
	baseMu.Unlock()
}

// runDir makes a fresh folder for one run.
func runDir() (string, error) {
	baseMu.RLock()
	b := base
	baseMu.RUnlock()
	if b == "" {
		return "", errors.New("gohort's workspaces folder is not set, so a job has nowhere to run")
	}
	if err := os.MkdirAll(b, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(b, "run-")
}

// Result is what a job gave back: one result (Out) or several (Files).
type Result struct {
	Out   []byte
	Files [][]byte
	By    string // what did it: tools, "built in", or an adapted flow's name
	Took  time.Duration
	Log   string // the end of what the commands printed
}

// target is what a flow's result has to be: a Kind for an input, an output
// format for an export. It names the result file when a step does not, and
// checks what came back.
type target interface {
	defaultExt() string
	accept(out []byte) ([]byte, error)
}

// flowRun is one run of a flow.
type flowRun struct {
	by      string
	job     Job
	steps   []string
	in      []byte
	from    string
	params  map[string]string
	timeout time.Duration
	reads   []string
}

// runFlow runs a flow's steps in one folder, each step's result the next
// one's {in}, and checks the last one's result against the job.
func runFlow(ctx context.Context, r flowRun) (Result, error) {
	if r.job.TakesFile() {
		if len(r.in) == 0 {
			return Result{}, errors.New("the file is empty")
		}
		if len(r.in) > maxInput {
			return Result{}, fmt.Errorf("the file is %d MB, over the %d MB a job takes", len(r.in)>>20, maxInput>>20)
		}
	}
	for _, step := range r.steps {
		if p := Program(step); !Installed(p) {
			return Result{}, fmt.Errorf("%s is not installed on this machine", p)
		}
	}
	if err := checkParams(r.job, r.params); err != nil {
		return Result{}, err
	}
	select {
	case runs <- struct{}{}:
		defer func() { <-runs }()
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	dir, err := runDir()
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(dir)
	inName := "in" + extFor(r.from)
	if r.job.TakesFile() {
		if err := os.WriteFile(filepath.Join(dir, inName), r.in, 0o600); err != nil {
			return Result{}, err
		}
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	// The job decides the network, not the context it was called from: an
	// image converted inside a turn that may browse still has none.
	ctx = netgate.WithWorkspaceNetwork(ctx, r.job.Network)
	want := r.job.target()
	start := time.Now()
	var logs []string
	var outName string
	for i, step := range r.steps {
		last := i == len(r.steps)-1
		line, out, err := commandLine(step, inName, i, last, want, r.params)
		if err != nil {
			return Result{}, err
		}
		if last && r.job.Files {
			if err := os.Mkdir(filepath.Join(dir, "outdir"), 0o700); err != nil {
				return Result{}, err
			}
		}
		res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: line, WorkspaceDir: dir, MaxOutput: keepLog * 4,
			RawNetwork: r.job.Network, Reach: r.reads})
		if t := tail(res.Output, keepLog); t != "" {
			logs = append(logs, t)
		}
		prog := Program(step)
		fail := func(format string, args ...any) (Result, error) {
			where := ""
			if len(r.steps) > 1 {
				where = fmt.Sprintf("step %d: ", i+1)
			}
			return Result{By: r.by, Took: time.Since(start), Log: tail(strings.Join(logs, "\n"), keepLog)}, fmt.Errorf(where+format, args...)
		}
		switch {
		case res.TimedOut || errors.Is(ctx.Err(), context.DeadlineExceeded):
			return fail("%s did not finish within %s", prog, r.timeout)
		case ctx.Err() != nil:
			return fail("%v", ctx.Err())
		case res.Err != nil:
			return fail("%s failed: %v%s", prog, res.Err, said(tail(res.Output, keepLog)))
		}
		if last && r.job.Files {
			break
		}
		st, err := os.Stat(filepath.Join(dir, out))
		if err != nil {
			return fail("%s ran but wrote no result%s", prog, said(tail(res.Output, keepLog)))
		}
		if st.Size() > maxResult {
			return fail("%s wrote %d MB, over the %d MB a step may give", prog, st.Size()>>20, maxResult>>20)
		}
		inName, outName = out, out
	}
	took := time.Since(start)
	log := tail(strings.Join(logs, "\n"), keepLog)
	result := Result{By: r.by, Took: took, Log: log}
	if r.job.Files {
		files, err := readOutdir(filepath.Join(dir, "outdir"), want)
		if err != nil {
			return Result{By: r.by, Took: took, Log: log}, fmt.Errorf("what it wrote was not used: %v", err)
		}
		result.Files = files
		return result, nil
	}
	out, err := os.ReadFile(filepath.Join(dir, outName))
	if err != nil {
		return Result{By: r.by, Took: took, Log: log}, err
	}
	if out, err = want.accept(out); err != nil {
		return Result{By: r.by, Took: took, Log: log}, fmt.Errorf("what it wrote was not used: %v", err)
	}
	result.Out = out
	return result, nil
}

// maxFiles bounds how many results a job that gives several may return.
const maxFiles = 200

// readOutdir reads the results a job wrote into {outdir}, in name order,
// each checked against want; there has to be at least one.
func readOutdir(dir string, want target) ([][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, errors.New("it wrote nothing into {outdir}")
	}
	if len(names) > maxFiles {
		return nil, fmt.Errorf("it wrote %d files, over the %d a job may give", len(names), maxFiles)
	}
	var out [][]byte
	total := 0
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		if total += len(b); total > maxResult {
			return nil, fmt.Errorf("it wrote more than the %d MB a job may give", maxResult>>20)
		}
		if b, err = want.accept(b); err != nil {
			return nil, fmt.Errorf("%s: %v", n, err)
		}
		out = append(out, b)
	}
	return out, nil
}

// commandLine is step i as the sandbox's shell receives it: every word
// quoted, so the shell only starts the program and reads nothing in it. A
// ";" or "$(...)" in the line is a character in an argument, not an
// instruction. {in} is the file it reads (the flow's input, or the step
// before's result), {out} the file it writes, named for the job's result on
// the last step (or {out.wav}, as the step names it), {outdir} the folder
// the last step writes several results into, and a job's values ({count})
// what the call passed. With no {out}, what the step prints is its result:
// gohort's own redirection, after the quoted words, sends it there.
func commandLine(step, in string, i int, last bool, want target, params map[string]string) (line, out string, err error) {
	words, err := splitArgs(step)
	if err != nil {
		return "", "", err
	}
	out = fmt.Sprintf("step%d%s", i+1, want.defaultExt())
	if last {
		out = "out" + want.defaultExt()
	}
	if m := outExtRE.FindStringSubmatch(step); m != nil {
		out = strings.TrimSuffix(out, filepath.Ext(out)) + m[1]
	}
	hasOut := strings.Contains(step, "{out")
	quoted := make([]string, len(words))
	for k, w := range words {
		w = strings.ReplaceAll(w, "{in}", in)
		w = strings.ReplaceAll(w, "{outdir}", "outdir")
		w = outAnyRE.ReplaceAllString(w, out)
		for name, v := range params {
			w = strings.ReplaceAll(w, "{"+name+"}", v)
		}
		quoted[k] = quote(w)
	}
	line = "exec " + strings.Join(quoted, " ")
	if !hasOut {
		line += " > " + quote(out)
	}
	return line, out, nil
}

var (
	outExtRE = regexp.MustCompile(`\{out(\.[a-z0-9]{1,6})\}`)
	outAnyRE = regexp.MustCompile(`\{out(\.[a-z0-9]{1,6})?\}`)
)

func quote(w string) string { return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'" }

// probeFlags are the arguments Probe may pass: the ones that ask a program
// what it is, and nothing that asks it to do anything.
var probeFlags = []string{"--help", "-h", "-help", "help", "--version", "-version", "-V", "--usage", ""}

var programRE = regexp.MustCompile(`^(/[A-Za-z0-9._+/-]+|[A-Za-z0-9._+-]+)$`)

// Probe runs program with one of probeFlags, in the sandbox a conversion
// runs in (no network, an empty folder of its own), so someone setting one
// up reads its usage as the conversion will see it: a program installed
// somewhere the sandbox cannot see shows up here as not found. The output
// comes back with a failing exit, since usage is often printed on one.
func Probe(ctx context.Context, program, flag string) (string, error) {
	program = strings.TrimSpace(program)
	if !programRE.MatchString(program) {
		return "", fmt.Errorf("%q is not a program name or path", program)
	}
	if !contains(probeFlags, flag) {
		return "", fmt.Errorf("a probe asks a program what it is: use one of %s", strings.Join(probeFlags[:len(probeFlags)-1], " "))
	}
	path, err := exec.LookPath(program)
	if err != nil {
		return "", fmt.Errorf("%s is not installed on this machine", program)
	}
	dir, err := runDir()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	line := "exec " + quote(program)
	if flag != "" {
		line += " " + quote(flag)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res := sandbox.RunSandboxedShellIn(netgate.WithWorkspaceNetwork(ctx, false), sandbox.ShellRun{
		Command: line, WorkspaceDir: dir, MaxOutput: keepLog * 4,
	})
	out := tail(res.Output, keepLog*2)
	head := "Found at " + path + "."
	switch {
	case res.TimedOut || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return head + " It did not finish within 15s.\n" + out, nil
	case res.Err != nil:
		return fmt.Sprintf("%s It exited with an error (%v), output below.\n%s", head, res.Err, out), nil
	case out == "":
		return head + " It printed nothing.", nil
	}
	return head + "\n" + out, nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

// said appends what a command printed to an error, briefly.
func said(log string) string {
	if log == "" {
		return ""
	}
	return ": " + tail(log, 300)
}
