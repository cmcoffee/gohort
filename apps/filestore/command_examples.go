// Teaching a mapping by example: an admin runs the command themselves, and
// each run becomes a worked example the mapping conversation learns from.
//
// Builder struggled when an admin described a command with an example line:
// it read the example's values as things it should already have. Seeing the
// command actually used, with what it printed, is the clearer lesson. So the
// command's row has a box pinned to that one binary: the admin types its
// arguments, it runs in the store's folder with no shell (quoting is parsed
// here, nothing is expanded), its output streams back with Cancel, and the
// run is kept on the command for Builder to read (admin_examples).
//
// The admin may run anything with the binary: they registered it. Builder's
// own limits are unchanged; it learns from the runs, it does not repeat them.
package filestore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// CommandExample is one run an admin made to show how the command is used.
type CommandExample struct {
	Args   string    `json:"args"`
	Output string    `json:"output"` // the end of what it printed
	Exit   string    `json:"exit"`   // "0", or how it failed
	By     string    `json:"by"`
	At     time.Time `json:"at"`
}

// examplesKept is how many runs a command keeps, newest last.
const examplesKept = 8

// exampleOutputKept is how much of a run's output an example keeps: the end,
// where the result and any error are.
const exampleOutputKept = 3000

// splitArgs splits an argument line the way a person typing it means it:
// on spaces, with '...' and "..." holding a value that has spaces in it, and
// a backslash escaping the next character. Nothing is expanded or run.
func splitArgs(line string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in, quote, escaped := false, byte(0), false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case escaped:
			cur.WriteByte(c)
			escaped, in = false, true
		case c == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if quote != 0 {
		return nil, Error("a quote is not closed")
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

// handleTryCommand runs a command for its admin, streaming what it prints,
// and keeps the run as an example: POST /filestore/api/map/try?id=<slug/name>
// {"args": "..."}. The answer is the form Test stream: {"progress": line} as
// it goes, then {"ok", "message"|"error"} with the end of the output.
func (T *FileStoreApp) handleTryCommand(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok || !adminOnly(w, r) {
		return
	}
	slug, name, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "/")
	st, ok := LoadStore(T.DB, slug)
	cmd, cok := LoadStoreCommand(T.DB, slug, name)
	if !ok || !cok {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Args string `json:"args"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	argv, err := splitArgs(body.Args)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	dir, err := SubRoot(st.Path, "")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := st.roomFor(0); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)
	send := func(v map[string]any) {
		b, _ := json.Marshal(v)
		w.Write(append(b, '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}
	// The request's context: Cancel in the form closes it, which stops the
	// command. Bounded like any person-run command.
	ctx, cancel := context.WithTimeout(r.Context(), commandTimeout)
	defer cancel()
	defer pathhold.Hold(dir)()
	defer st.wrote()
	c := exec.CommandContext(ctx, cmd.Command, argv...)
	c.Dir = dir
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	var all strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		last := time.Time{}
		for sc.Scan() {
			line := sc.Text()
			all.WriteString(line + "\n")
			if time.Since(last) > 200*time.Millisecond && strings.TrimSpace(line) != "" {
				send(map[string]any{"progress": truncateLine(line, 200)})
				last = time.Now()
			}
		}
	}()
	runErr := c.Run()
	pw.Close()
	<-done

	out := strings.TrimSpace(all.String())
	if len(out) > exampleOutputKept {
		out = "[...]\n" + out[len(out)-exampleOutputKept:]
	}
	exit := "0"
	if runErr != nil {
		exit = runErr.Error()
	}
	if ctx.Err() == context.Canceled {
		send(map[string]any{"ok": false, "error": "stopped; not kept as an example"})
		return
	}
	cmd.Examples = append(cmd.Examples, CommandExample{Args: strings.Join(argv, " "), Output: out, Exit: exit, By: user, At: time.Now()})
	if len(cmd.Examples) > examplesKept {
		cmd.Examples = cmd.Examples[len(cmd.Examples)-examplesKept:]
	}
	T.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	Log("[filestore] %s ran %s/%s as an example (exit %s)", user, cmd.Slug, cmd.Name, exit)
	shown := out
	if shown == "" {
		shown = "(printed nothing)"
	}
	msg := fmt.Sprintf("Exit %s. Kept as example %d for the mapping.\n%s", exit, len(cmd.Examples), shown)
	if runErr != nil {
		send(map[string]any{"ok": false, "error": msg})
		return
	}
	send(map[string]any{"ok": true, "message": msg})
}

func truncateLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// examplesText is the command's examples as the mapping reads them.
func (a StoreCommand) examplesText() string {
	if len(a.Examples) == 0 {
		return "The admin has not run this command as an example yet. They can, from Show it how on the command's row."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The admin ran %s %s to show how it is used, oldest first. Treat the values in them as one instance of the syntax: what changes from run to run becomes a {placeholder}.\n", a.Command, countOf(len(a.Examples), "time", "times"))
	for i, e := range a.Examples {
		fmt.Fprintf(&b, "\n%d. %s %s\n   exit: %s\n", i+1, a.Command, e.Args, e.Exit)
		if e.Output != "" {
			b.WriteString("   printed:\n")
			for _, l := range strings.Split(e.Output, "\n") {
				b.WriteString("     " + l + "\n")
			}
		}
	}
	return b.String()
}

// handleTryArgs fills the box with the last example's line when the row is
// opened, so a second run starts from the first. The form's per-field saves
// land here too and are answered the same way: nothing is stored but runs.
func (T *FileStoreApp) handleTryArgs(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok || !adminOnly(w, r) {
		return
	}
	slug, name, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "/")
	cmd, ok := LoadStoreCommand(T.DB, slug, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	last := ""
	if n := len(cmd.Examples); n > 0 {
		last = cmd.Examples[n-1].Args
	}
	writeJSON(w, map[string]any{"args": last})
}
