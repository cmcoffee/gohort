// Store actions: admin-registered commands that operate on ONE folder of
// a store.
//
// Decrypting is the first one, and the reason this is a list rather than
// a `decrypt_command` field. What arrives in a drop folder needs
// different things done to it in different deployments — decrypt, redact
// before anyone reads it, unpack a proprietary container, run an
// extractor that turns a binary dump into text, build an index. Every one
// of those is "run a registered binary against this folder, then the
// files are ready", and a field named after the first instance would have
// made the second one a second field, a second endpoint, and a second UI.
//
// The shape is deliberately small:
//
//	<cmd> <folder>            one-phase: run it, files are ready
//	<cmd> <folder>            two-phase, first call: prints a challenge
//	<cmd> <folder> <input>    two-phase, second call: the response
//
// Two phases exist because of the case that prompted this: the answer to
// the challenge is looked up out-of-band by a person, so nothing here can
// obtain it and nothing here should try. An action that needs no input
// just declares one phase and runs.
//
// WHY THIS RUNS COMMANDS WHEN THE PACKAGE DOC SAYS COMMANDS ARE
// SERVITOR'S. That rule is about MODEL-invoked commands: minting a
// template from an intent, classifying its risk, holding it for owner
// approval. None of it applies here — the binary is registered by an
// admin in the same breath as the directory it operates on, and the
// caller is a person clicking a button.
//
// It is also stricter than the path it declines to reuse: there is NO
// SHELL. The command, the folder and the input are exec'd as separate
// arguments, so quoting, word-splitting and metacharacters have nowhere
// to happen. The folder is resolved by SubRoot; the input is one argv
// element, whatever is in it.

package filestore

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// actionsTable holds registered actions, keyed "<store-slug>/<name>".
const actionsTable = "file_store_actions"

// actionTimeout bounds one run. Generous: these unpack real captures,
// and killing one halfway leaves a folder half-processed, which is worse
// than waiting.
const actionTimeout = 30 * time.Minute

// StoreAction is one registered command.
type StoreAction struct {
	Slug string `json:"slug"` // which store it belongs to
	Name string `json:"name"` // stable handle, snake_case: "decrypt"
	// Label is what the button says. Named for what it DOES to the
	// folder ("Decrypt bundle"), because that is what the person
	// clicking it is deciding.
	Label   string `json:"label,omitempty"`
	Command string `json:"command"` // absolute path; no shell
	// TwoPhase: run bare first and show the output as a challenge, then
	// run again with what the person supplies. Off = one call, done.
	TwoPhase bool `json:"two_phase,omitempty"`
	// InputLabel is what the box asks for on the second phase ("Response
	// key"). Only meaningful when TwoPhase.
	InputLabel string `json:"input_label,omitempty"`
	Help       string `json:"help,omitempty"`
}

func actionKey(slug, name string) string { return slug + "/" + name }

// SaveStoreAction validates and stores one action.
func SaveStoreAction(db Database, a StoreAction) (StoreAction, error) {
	a.Slug = strings.TrimSpace(a.Slug)
	a.Name = RefToolSlug(strings.TrimSpace(a.Name))
	a.Command = strings.TrimSpace(a.Command)
	switch {
	case a.Slug == "":
		return a, Error("say which store this action belongs to")
	case a.Name == "":
		return a, Error("give the action a name — a short handle like \"decrypt\"")
	case a.Command == "":
		return a, Error("give the absolute path of the command to run")
	case !strings.HasPrefix(a.Command, "/"):
		return a, Error("the command must be an absolute path: a relative one resolves against whatever directory the server happens to be in")
	}
	if _, ok := LoadStore(db, a.Slug); !ok {
		return a, Error("there is no file store called " + a.Slug)
	}
	if strings.TrimSpace(a.Label) == "" {
		a.Label = strings.ToUpper(a.Name[:1]) + strings.ReplaceAll(a.Name[1:], "_", " ")
	}
	db.Set(actionsTable, actionKey(a.Slug, a.Name), a)
	return a, nil
}

// ListStoreActions returns every registered action, sorted for a stable
// table.
func ListStoreActions(db Database) []StoreAction {
	if db == nil {
		return nil
	}
	var out []StoreAction
	for _, k := range db.Keys(actionsTable) {
		var a StoreAction
		if db.Get(actionsTable, k, &a) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// StoreActionsFor returns the actions registered against one store.
func StoreActionsFor(db Database, slug string) []StoreAction {
	var out []StoreAction
	for _, a := range ListStoreActions(db) {
		if a.Slug == slug {
			out = append(out, a)
		}
	}
	return out
}

// LoadStoreAction resolves one by store and name.
func LoadStoreAction(db Database, slug, name string) (StoreAction, bool) {
	var a StoreAction
	ok := db != nil && db.Get(actionsTable, actionKey(slug, strings.TrimSpace(name)), &a)
	return a, ok
}

// DeleteStoreAction removes one.
func DeleteStoreAction(db Database, slug, name string) {
	if db != nil {
		db.Unset(actionsTable, actionKey(slug, name))
	}
}

// runAction executes one phase and returns its combined output.
//
// Combined stdout+stderr because the output belongs to a tool this code
// does not own, on a stream it should not assume: capturing only stdout
// is how a challenge printed to stderr becomes "it printed nothing".
func runAction(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text, Error("the command did not finish within " + actionTimeout.String() +
			" and was stopped. The folder may be half-processed — check it before retrying.")
	}
	return text, err
}

// handleAction runs a registered action against one folder.
//
//	POST /filestore/api/action?slug=<store>&within=<folder>&action=<name>
//	     {}              → one-phase: {"output": "..."} ; two-phase: {"challenge": "..."}
//	     {"input":"..."} → two-phase second call: {"output": "..."}
//
// Output is passed through VERBATIM in both directions. Nothing here
// parses it: the format belongs to the registered binary, and a parser
// guessing at it is how the first real bundle breaks.
func (T *FileStoreApp) handleAction(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st, found := LoadStore(T.DB, strings.TrimSpace(r.URL.Query().Get("slug")))
	if !found || !st.AllowsUser(user) {
		http.Error(w, "no such file store you can reach", http.StatusNotFound)
		return
	}
	// An action REWRITES the folder, so it takes the write grant rather
	// than the read one — the same gate as upload, for the same reason.
	if !st.UploadsAllowedFor(user, RequestIsAdmin(r)) {
		http.Error(w, "you may read "+st.Name+" but not write to it, and an action rewrites the folder.", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("action"))
	act, ok := LoadStoreAction(T.DB, st.Slug, name)
	if !ok {
		avail := "none are registered"
		if list := StoreActionsFor(T.DB, st.Slug); len(list) > 0 {
			var names []string
			for _, a := range list {
				names = append(names, a.Name)
			}
			avail = "registered here: " + strings.Join(names, ", ")
		}
		http.Error(w, "no action called "+strconv.Quote(name)+" on "+st.Name+" ("+avail+
			"). An admin registers actions under Admin → Files.", http.StatusNotFound)
		return
	}
	dir, err := SubRoot(st.Path, r.URL.Query().Get("within"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		Input string `json:"input"`
	}
	// An empty body is the first phase, so a decode failure is only fatal
	// when there was something to decode.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	input := strings.TrimSpace(body.Input)

	args := []string{dir}
	// NEVER logged: on a two-phase action the input is the secret half of
	// an out-of-band exchange, and a log line is the one place it would
	// outlive the request.
	if input != "" {
		args = append(args, input)
	}
	out, err := runAction(r.Context(), act.Command, args...)
	if err != nil {
		Log("[filestore.action] %s: %s on %s/%s failed: %v", user, act.Name, st.Name, dir, err)
		// The tool's own words first: "exit status 1" tells a person
		// nothing and the line above it usually tells them everything.
		http.Error(w, chFirstNonEmpty(out, err.Error()), http.StatusBadGateway)
		return
	}
	Log("[filestore.action] %s ran %s on %s/%s", user, act.Name, st.Name, dir)
	// A two-phase action with nothing supplied yet returns a CHALLENGE,
	// which the caller shows to a person. Same bytes either way; the key
	// name is what tells the UI whether to ask for something.
	if act.TwoPhase && input == "" {
		writeJSON(w, map[string]any{
			"challenge":   out,
			"input_label": chFirstNonEmpty(act.InputLabel, "Response"),
			"action":      act.Name,
		})
		return
	}
	writeJSON(w, map[string]any{"output": out, "action": act.Name})
}

// chFirstNonEmpty returns the first non-blank string.
func chFirstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return "the command failed and said nothing"
}
