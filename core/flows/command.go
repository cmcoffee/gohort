package flows

// A command for a caller other than a job: a service connector whose
// provider is a local program (core/service's command transport). It runs
// exactly as a flow's steps do: each word quoted so no shell reads it, in the
// sandbox, in a folder of its own, with no network unless asked, stopped at
// its time limit, its result bounded. What differs is how values arrive:
// named files and text in place of a job's {in} and parameters.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/sandbox"
)

// Command is one run of a program for a service.
type Command struct {
	// Steps are the command lines, run in order; {in} in a step is the
	// result of the step before, {out} (or {out.png}) the file it writes,
	// and with no {out}, what it prints is its result.
	Steps []string
	// Files are {name}s that stand for a file, written into the run's
	// folder before the first step.
	Files map[string]CommandFile
	// Values are {name}s that stand for text.
	Values map[string]string
	// Network lets the program reach the network (a downloader); off, it
	// has none.
	Network bool
	Timeout time.Duration
	// OutExt names the result file when a step does not (".png").
	OutExt string
}

// CommandFile is a file a command reads.
type CommandFile struct {
	Data []byte
	Ext  string // ".png"; the file is named for its {name} with it
}

var nameTokenRE = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)

// RunCommand runs c and returns its result, or why not with the end of what
// it printed.
func RunCommand(ctx context.Context, c Command) (Result, error) {
	if len(c.Steps) == 0 {
		return Result{}, errors.New("a command needs at least one step")
	}
	for _, step := range c.Steps {
		if p := Program(step); !Installed(p) {
			return Result{}, fmt.Errorf("%s is not installed on this machine", p)
		}
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
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
	files := map[string]string{}
	for name, f := range c.Files {
		if len(f.Data) > maxInput {
			return Result{}, fmt.Errorf("{%s} is %d MB, over the %d MB a command takes", name, len(f.Data)>>20, maxInput>>20)
		}
		fn := "in-" + name + f.Ext
		if err := os.WriteFile(filepath.Join(dir, fn), f.Data, 0o600); err != nil {
			return Result{}, err
		}
		files[name] = fn
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx = netgate.WithWorkspaceNetwork(ctx, c.Network)
	ext := c.OutExt
	if ext == "" {
		ext = ".out"
	}
	start := time.Now()
	var logs []string
	in, outName := "", ""
	for i, step := range c.Steps {
		line, out, err := c.line(step, in, i, i == len(c.Steps)-1, ext, files)
		if err != nil {
			return Result{}, err
		}
		res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: line, WorkspaceDir: dir, MaxOutput: keepLog * 4, RawNetwork: c.Network})
		if t := tail(res.Output, keepLog); t != "" {
			logs = append(logs, t)
		}
		prog := Program(step)
		fail := func(format string, args ...any) (Result, error) {
			where := ""
			if len(c.Steps) > 1 {
				where = fmt.Sprintf("step %d: ", i+1)
			}
			return Result{Took: time.Since(start), Log: tail(strings.Join(logs, "\n"), keepLog)}, fmt.Errorf(where+format, args...)
		}
		switch {
		case res.TimedOut || errors.Is(ctx.Err(), context.DeadlineExceeded):
			return fail("%s did not finish within %s", prog, timeout)
		case ctx.Err() != nil:
			return fail("%v", ctx.Err())
		case res.Err != nil:
			return fail("%s failed: %v%s", prog, res.Err, said(tail(res.Output, keepLog)))
		}
		st, err := os.Stat(filepath.Join(dir, out))
		if err != nil {
			return fail("%s ran but wrote no result%s", prog, said(tail(res.Output, keepLog)))
		}
		if st.Size() > maxResult {
			return fail("%s wrote %d MB, over the %d MB a step may give", prog, st.Size()>>20, maxResult>>20)
		}
		in, outName = out, out
	}
	out, err := os.ReadFile(filepath.Join(dir, outName))
	log := tail(strings.Join(logs, "\n"), keepLog)
	if err != nil {
		return Result{Took: time.Since(start), Log: log}, err
	}
	return Result{Out: out, Took: time.Since(start), Log: log}, nil
}

// line is step i as the sandbox's shell receives it, as a flow's are
// (commandLine): every word quoted. A text value that is a whole word may
// not start with "-": the program would read it as an option, and a prompt
// is no place for one.
func (c Command) line(step, in string, i int, last bool, ext string, files map[string]string) (string, string, error) {
	words, err := splitArgs(step)
	if err != nil {
		return "", "", err
	}
	out := fmt.Sprintf("step%d%s", i+1, ext)
	if last {
		out = "out" + ext
	}
	if m := outExtRE.FindStringSubmatch(step); m != nil {
		out = strings.TrimSuffix(out, filepath.Ext(out)) + m[1]
	}
	hasOut := strings.Contains(step, "{out")
	quoted := make([]string, len(words))
	for k, w := range words {
		if m := nameTokenRE.FindStringSubmatch(w); m != nil && m[0] == w {
			if v, ok := c.Values[m[1]]; ok && strings.HasPrefix(strings.TrimSpace(v), "-") {
				return "", "", fmt.Errorf("{%s} starts with \"-\", which %s would read as an option", m[1], Program(step))
			}
		}
		w = strings.ReplaceAll(w, "{in}", in)
		w = outAnyRE.ReplaceAllString(w, out)
		w = nameTokenRE.ReplaceAllStringFunc(w, func(tok string) string {
			name := tok[1 : len(tok)-1]
			if fn, ok := files[name]; ok {
				return fn
			}
			if v, ok := c.Values[name]; ok {
				return v
			}
			return tok
		})
		quoted[k] = quote(w)
	}
	line := "exec " + strings.Join(quoted, " ")
	if !hasOut {
		line += " > " + quote(out)
	}
	return line, out, nil
}
