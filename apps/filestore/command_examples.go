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
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/pathhold"
)

// CommandExample is one run an admin made to show how the command is used.
type CommandExample struct {
	ID     string `json:"id"` // stable handle, for editing or removing it
	Args   string `json:"args"`
	Output string `json:"output"`         // the end of what it printed
	Exit   string `json:"exit"`           // "0", or how it failed
	Note   string `json:"note,omitempty"` // what the admin said this run shows
	// Use groups runs that are the steps of one use of the command, in order
	// (a run that prints a challenge, then one that passes the response).
	// Runs of the same Use are its steps; a new use starts a new number.
	Use int       `json:"use,omitempty"`
	By  string    `json:"by"`
	At  time.Time `json:"at"`
}

// examplesKept is how many runs a command keeps, newest last: room for a few
// uses of a few steps each.
const examplesKept = 20

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
		Note string `json:"note"`
		Step string `json:"step"` // "next": the next step of the last use; else a new use
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
	use := 1
	if n := len(cmd.Examples); n > 0 {
		use = cmd.Examples[n-1].Use + 1
		if body.Step == "next" {
			use = cmd.Examples[n-1].Use
		}
	}
	cmd.Examples = append(cmd.Examples, CommandExample{ID: newExampleID(), Args: strings.Join(argv, " "), Output: out, Exit: exit,
		Note: strings.TrimSpace(body.Note), Use: use, By: user, At: time.Now()})
	if len(cmd.Examples) > examplesKept {
		cmd.Examples = cmd.Examples[len(cmd.Examples)-examplesKept:]
	}
	T.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	Log("[filestore] %s ran %s/%s as an example (exit %s)", user, cmd.Slug, cmd.Name, exit)
	shown := out
	if shown == "" {
		shown = "(printed nothing)"
	}
	steps := 0
	for _, e := range cmd.Examples {
		if e.Use == use {
			steps++
		}
	}
	msg := fmt.Sprintf("Exit %s. Kept for the mapping as use %d, step %d.\n%s", exit, use, steps, shown)
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

// examplesText is the command's examples as the mapping reads them: each use
// with its steps in order, and what each step printed.
func (a StoreCommand) examplesText() string {
	if len(a.Examples) == 0 {
		return "The admin has not run this command as an example yet. They can, from Show it how on the command's row."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The admin ran %s to show how it is used, grouped into USES: a use with more than one step was done in that order (for example a run that prints a challenge, then one passing a person's response). Treat the values as one instance of the syntax: what would differ from one use to the next becomes a {placeholder}. Map a use with several steps as one action per step: a step that ends waiting on a person (a challenge, a code) returns what they must answer, and the next step takes their answer; an agent cannot answer a challenge itself and asks the person between them. The admin's note on a step, when there is one, says what it shows. Where a step, a value or an output is unclear, ASK the admin in this conversation rather than guess.\n", a.Command)
	use, step := -1, 0
	for _, e := range a.Examples {
		if e.Use != use {
			use, step = e.Use, 0
			fmt.Fprintf(&b, "\nUse %d:\n", e.Use)
		}
		step++
		fmt.Fprintf(&b, "  step %d: %s %s\n    exit: %s\n", step, a.Command, e.Args, e.Exit)
		if e.Note != "" {
			fmt.Fprintf(&b, "    admin's note: %s\n", e.Note)
		}
		if e.Output != "" {
			b.WriteString("    printed:\n")
			for _, l := range strings.Split(e.Output, "\n") {
				b.WriteString("      " + l + "\n")
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

func newExampleID() string { return strings.ReplaceAll(UUIDv4(), "-", "")[:12] }

// handleExamples is the admin's list of recorded runs, to review and correct
// before Builder learns from them: a run done by mistake would otherwise teach
// it the wrong thing.
//
//	GET    /filestore/api/map/examples?id=<slug/name>            the runs, each use's steps in order
//	GET    /filestore/api/map/examples?id=...&eid=<run>          one run, for its edit form
//	POST   /filestore/api/map/examples?id=...&eid=<run>          {note, use} change one
//	DELETE /filestore/api/map/examples?id=...&eid=<run>          remove one
func (T *FileStoreApp) handleExamples(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok || !adminOnly(w, r) {
		return
	}
	slug, name, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "/")
	cmd, ok := LoadStoreCommand(T.DB, slug, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Runs kept before they had handles get one now.
	changed := false
	for i := range cmd.Examples {
		if cmd.Examples[i].ID == "" {
			cmd.Examples[i].ID = newExampleID()
			changed = true
		}
	}
	if changed {
		T.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
	}
	eid := strings.TrimSpace(r.URL.Query().Get("eid"))
	idx := -1
	for i, e := range cmd.Examples {
		if eid != "" && e.ID == eid {
			idx = i
		}
	}
	switch r.Method {
	case http.MethodGet:
		if eid != "" {
			if idx < 0 {
				http.NotFound(w, r)
				return
			}
			e := cmd.Examples[idx]
			writeJSON(w, map[string]any{"note": e.Note, "use": e.Use})
			return
		}
		rows := []map[string]any{}
		use, step := -1, 0
		for _, e := range cmd.Examples {
			if e.Use != use {
				use, step = e.Use, 0
			}
			step++
			exit := "ok"
			if e.Exit != "0" {
				exit = e.Exit
			}
			rows = append(rows, map[string]any{
				"eid": e.ID, "where": fmt.Sprintf("use %d, step %d", e.Use, step),
				"args": e.Args, "exit": exit, "note": e.Note,
				"output": lastLines(e.Output, 3),
			})
		}
		writeJSON(w, rows)
	case http.MethodPost, http.MethodPatch:
		if idx < 0 {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if v, ok := body["note"]; ok && v != nil {
			cmd.Examples[idx].Note = strings.TrimSpace(fmt.Sprint(v))
		}
		if v, ok := body["use"]; ok && v != nil {
			if n, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(v))); err == nil && n > 0 {
				cmd.Examples[idx].Use = n
				sortExamplesByUse(cmd.Examples)
			}
		}
		T.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
		writeJSON(w, map[string]any{"ok": true})
	case http.MethodDelete:
		if idx < 0 {
			http.NotFound(w, r)
			return
		}
		cmd.Examples = append(cmd.Examples[:idx], cmd.Examples[idx+1:]...)
		T.DB.Set(commandsTable, commandKey(cmd.Slug, cmd.Name), cmd)
		Log("[filestore] %s removed an example run of %s/%s", user, cmd.Slug, cmd.Name)
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// sortExamplesByUse keeps each use's steps together, in the order they were
// run, after a run is moved into another use.
func sortExamplesByUse(ex []CommandExample) {
	sort.SliceStable(ex, func(i, j int) bool { return ex[i].Use < ex[j].Use })
}

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = append([]string{"…"}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}
