// The mapping conversation: working out what a registered command can do, and
// writing it down on that command's own row.
//
// A registered command is one binary an admin pointed at a folder, called as
// `<cmd> <folder>`. That shape is all gohort knows about it — enough for a
// person to click, not enough for an agent to use well. What it can actually DO
// lives in its --help and in trying it, and only a person had ever read that.
//
// A conversation rather than a button, because the first --help rarely says
// everything and what a flag MEANS is a judgement an admin should be able to
// push back on before it becomes a description other agents trust.
//
// WHY THE AGENT NEEDS NO AUTHORITY. What it produces is inert. Mapping writes
// onto the command and nothing else; no agent can call any of it until an admin
// turns Agents on for that command, on the same row, and that switch is the
// approval. Probing runs only the ONE command this conversation was opened on —
// an admin chose that binary, and the agent is choosing flags for it.
//
// And it lands where it came from, because there is nowhere else for it to
// land. See command_tools.go: a mapping is fields on the command record, not a
// noun of its own.

package filestore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// mapAgentID is the agent that maps. Builder, because this IS authoring — it
// already knows what a tool description worth reading looks like, and giving the
// job to a second agent would mean maintaining a second opinion about that.
const mapAgentID = "builder"

// handleMapChat is the conversation, scoped to one command.
func (T *FileStoreApp) handleMapChat(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	user := AuthCurrentUser(r)
	// One id, not a slug and a name. The panel is mounted inside a store row,
	// and a {slug}/{name} pair there is answered by the STORE — so the command
	// arrived as the folder's own display name and never resolved. The row
	// already carries the composite the command is stored under; that is what
	// it sends. See the Map action in admin.go.
	slug, name, _ := strings.Cut(strings.TrimSpace(r.URL.Query().Get("id")), "/")
	st, ok := LoadStore(T.DB, slug)
	if !ok || user == "" {
		http.NotFound(w, r)
		return
	}
	cmd, ok := LoadStoreCommand(T.DB, slug, RefToolSlug(name))
	if !ok {
		http.Error(w, "that command is not registered against this folder", http.StatusNotFound)
		return
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "the agent runtime is not available", http.StatusServiceUnavailable)
		return
	}
	agent, ok := orch.LookupAppAgent(user, mapAgentID)
	if !ok {
		http.Error(w, "no agent is available to map commands", http.StatusServiceUnavailable)
		return
	}
	// Filed under this command, so its conversations can be listed and
	// reopened from its row (handleMapSessions).
	stampMapContext(r, mapContext(cmd))
	orch.PublicHandleSendWithAppTools(w, r, agent, T.mappingTools(r.Context(), st, cmd))
}

// mapContext is what a command's mapping conversations are filed under.
func mapContext(cmd StoreCommand) string { return "filestore-map:" + commandKey(cmd.Slug, cmd.Name) }

// mapAgentFor resolves the command named by the id query value and Builder,
// for the mapping conversation's other endpoints. Writes the error itself.
func (T *FileStoreApp) mapAgentFor(w http.ResponseWriter, r *http.Request, id string) (StoreCommand, *orchestrate.OrchestrateApp, orchestrate.AgentRecord, bool) {
	var none orchestrate.AgentRecord
	if !adminOnly(w, r) {
		return StoreCommand{}, nil, none, false
	}
	slug, name, _ := strings.Cut(strings.TrimSpace(id), "/")
	cmd, ok := LoadStoreCommand(T.DB, slug, RefToolSlug(name))
	if !ok {
		http.Error(w, "that command is not registered against this folder", http.StatusNotFound)
		return StoreCommand{}, nil, none, false
	}
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "the agent runtime is not available", http.StatusServiceUnavailable)
		return StoreCommand{}, nil, none, false
	}
	agent, ok := orch.LookupAppAgent(AuthCurrentUser(r), mapAgentID)
	if !ok {
		http.Error(w, "no agent is available to map commands", http.StatusServiceUnavailable)
		return StoreCommand{}, nil, none, false
	}
	return cmd, orch, agent, true
}

// handleMapSessions lists a command's mapping conversations, and loads or
// deletes one, so an admin can go back to a mapping they started earlier.
//
//	GET    /filestore/api/map/chat/sessions?id=<slug/name>      this command's conversations
//	GET    /filestore/api/map/chat/sessions/<sid>?cmd=<slug/name>  one, to reopen
//	DELETE /filestore/api/map/chat/sessions/<sid>?cmd=<slug/name>  drop one
func (T *FileStoreApp) handleMapSessions(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	sid := strings.Trim(strings.TrimPrefix(r.URL.Path[strings.Index(r.URL.Path, "/api/map/chat/sessions")+len("/api/map/chat/sessions"):], "/"), " ")
	if sid == "" {
		cmd, orch, agent, ok := T.mapAgentFor(w, r, r.URL.Query().Get("id"))
		if !ok {
			return
		}
		orch.PublicHandleSessionListOnly(w, r, agent.ID, mapContext(cmd))
		return
	}
	_, orch, agent, ok := T.mapAgentFor(w, r, r.URL.Query().Get("cmd"))
	if !ok {
		return
	}
	orch.PublicHandleSessionOne(w, r, agent.ID, sid)
}

// handleMapCancel stops the mapping conversation's turn in flight.
func (T *FileStoreApp) handleMapCancel(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	_, orch, agent, ok := T.mapAgentFor(w, r, r.URL.Query().Get("id"))
	if !ok {
		return
	}
	orch.PublicHandleCancel(w, r, agent)
}

// stampMapContext writes the command's context into the send body as
// app_context, so the session the turn creates is filed under the command.
// Server-side, because the first send creates the session and no page could
// name it yet. Best-effort: a body that will not parse is sent unchanged.
func stampMapContext(r *http.Request, ctx string) {
	if r.Body == nil {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	r.Body.Close()
	restore := func() { r.Body = io.NopCloser(bytes.NewReader(raw)) }
	if err != nil {
		restore()
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body == nil {
		restore()
		return
	}
	body["app_context"] = ctx
	stamped, err := json.Marshal(body)
	if err != nil {
		restore()
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(stamped))
	r.ContentLength = int64(len(stamped))
}

// mappingTools are the two an agent gets: one to LEARN the command, one to write
// down what it learned. Deliberately two, and in that order — an agent that
// could only propose would be guessing from a name, and one that could only
// probe would leave nothing behind.
func (T *FileStoreApp) mappingTools(ctx context.Context, st Store, cmd StoreCommand) []AgentToolDef {
	// How the mapped lines will run, said up front in both tools. Builder
	// explores freely: this conversation is the admin's, and what it writes
	// is inert until the admin approves it.
	// Examples are SYNTAX, not things to find. An admin explaining a command
	// writes a real-looking line ("download --src=/Diag/dump.zip
	// --dst=CASE-1"), and the mapping read those values as things it should
	// already have: it went looking for the file, or probed with it.
	examples := " When the admin shows an example command line with real-looking values (a path, a file name, an id), it illustrates the SYNTAX: the parts that would differ from one call to the next become {placeholders}, and those example values do not exist here and are not yours to check, find or probe with."
	later := " The {placeholders} in a command_template are filled in later, by an agent at the moment it calls the tool: their values do not exist yet, so do not look for them, invent them or probe with them."
	scope := " An agent will run each action you map exactly as its command_template is written, filling only the {placeholders}." + later + " Each line you map becomes one of the command's allowed uses, and the admin approves that list. Keep options fixed in the line rather than making a placeholder of one."
	if lines := cmd.allowedLines(); len(lines) > 0 {
		scope += " The admin has written the uses they want: " + strings.Join(lines, " | ") + ". Map those, as written; map anything beyond them only when the admin asks."
	} else if len(cmd.Examples) > 0 {
		scope += " The admin's runs (admin_examples) are the uses they want: map those, keeping each run's arguments in order, with only the values that would differ as {placeholders}."
	}
	scope += examples
	if strings.TrimSpace(cmd.Guide) != "" {
		scope += " The admin wrote a guide to how this command is used (admin_examples shows it first): what it says about the steps, the values and the output outranks your reading of the runs."
	}
	if n := len(cmd.Examples); n > 0 {
		scope += fmt.Sprintf(" The admin has run it %s as examples of how it is used: read them with admin_examples before anything else, they are the best evidence you have.", countOf(n, "time", "times"))
	}
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "admin_examples",
				Description: "The runs the admin made of " + cmd.Label + " to show how it is used: each command line, how it exited, and the end of what it printed. Learn the syntax from them; their values are one instance of it." + examples,
				Caps:        []Capability{CapRead},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				if fresh, ok := LoadStoreCommand(T.DB, cmd.Slug, cmd.Name); ok {
					return fresh.examplesText(), nil
				}
				return cmd.examplesText(), nil
			},
		},
		{
			Tool: Tool{
				Name: "probe_command",
				Description: fmt.Sprintf(
					"Run %s (%s), the command you are mapping, with arguments of your choosing: start with its help flag. Returns whatever it printed, INCLUDING when it exits non-zero, because a usage message is usually printed on a failing exit and that is the most useful thing a probe gets back. This is how you learn the interface you are about to describe; do not guess it. A probe runs for real, as the server's account, in the store's top folder: read help and make calls that only look, and ask the admin before one that would change, move, send or delete anything.",
					cmd.Label, cmd.Command) + scope,
				Parameters: map[string]ToolParam{
					"args": {Type: "string", Description: "Arguments to pass, space separated: e.g. \"--help\". Passed as separate argv entries with NO shell, so quoting and metacharacters do nothing."},
				},
				Caps: []Capability{CapExecute},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return T.probeCommand(ctx, st.Path, cmd, stringArg(args, "args"))
			},
		},
		{
			Tool: Tool{
				Name: "propose_tools",
				Description: fmt.Sprintf(
					"Write down what %s can do, as one action per thing it does. Propose narrow actions rather than one that takes a mode argument: unpack and verify are two actions, not one with a switch. What you write is inert: no agent can call it until an admin turns Agents on for this command, so propose what you actually verified and say in each description what you did not. Calling this again REPLACES what you proposed before, and switches Agents off until an admin approves again.",
					cmd.Label) + scope,
				Parameters: map[string]ToolParam{
					"description": {Type: "string", Description: "What this binary is for, in a sentence. It is what an agent reads before using it."},
					"actions": {Type: "string", Description: "JSON array of actions: [{\"name\":\"unpack\",\"description\":\"...\",\"command_template\":\"/opt/bin/cap unpack {folder}\",\"params\":{\"folder\":{\"type\":\"string\",\"description\":\"...\"}},\"required\":[\"folder\"]}]. " +
						"Every {placeholder} in a command_template must be declared in that action's params. " +
						"A parameter that takes a FILE or FOLDER from this store must declare \"path_scope\":\"files\", that is what turns its name into a real path inside this store when the tool runs; without it the command receives the bare name and finds nothing. " +
						"If the binary must RUN INSIDE the folder rather than take it as an argument (it resolves its inputs relative to the working directory), also add \"work_dir\":\"folder\" naming that parameter, and leave the folder out of the command line."},
				},
				Required: []string{"description", "actions"},
				Caps:     []Capability{CapWrite},
			},
			Handler: func(ctx context.Context, args map[string]any) (string, error) {
				return T.proposeTools(st, cmd, args)
			},
		},
	}
}

// probeCommand runs the command being mapped with the agent's arguments.
//
// Constrained to that ONE binary: an admin registered it against this folder,
// and the agent is choosing flags, never an executable. That is the whole reason
// this can exist without the approval machinery a free-form shell tool needs.
func (T *FileStoreApp) probeCommand(ctx context.Context, root string, cmd StoreCommand, argLine string) (string, error) {
	// The store root, so the probe sees what the real run will see. An agent
	// choosing flags against a binary that resolves anything relatively would
	// otherwise be reading one directory here and writing about another.
	out, err := runRegisteredCommand(ctx, root, cmd.Command, strings.Fields(argLine)...)
	out = strings.TrimSpace(out)
	if err != nil {
		// The output comes back WITH the failure, not instead of it. Handing an
		// agent "exit status 2" and dropping the usage message it printed would
		// make this tool useless for the one job it exists for.
		return fmt.Sprintf("(exited with an error: %v)\n%s", err, out), nil
	}
	if out == "" {
		return "(ran, printed nothing)", nil
	}
	return out, nil
}

// proposeTools writes what mapping worked out onto the command it came from.
func (T *FileStoreApp) proposeTools(st Store, cmd StoreCommand, args map[string]any) (string, error) {
	var acts []TempToolAction
	raw := strings.TrimSpace(stringArg(args, "actions"))
	if raw == "" {
		return "", Error("actions is required: a mapping with none does nothing")
	}
	if err := json.Unmarshal([]byte(raw), &acts); err != nil {
		return "", Error("actions must be a JSON array of {name, description, command_template, params}: " + err.Error())
	}
	for _, a := range acts {
		// A placeholder with no parameter behind it produces an action that
		// fails the first time it is called, and nobody finds out until then.
		for _, ph := range templatePlaceholders(a.CommandTemplate) {
			if _, ok := a.Params[ph]; !ok {
				return "", Error("action " + a.Name + " uses {" + ph + "} but declares no parameter called " + ph +
					", declare it, or take it out of the command")
			}
		}
		// Folder pinning and the work_dir check live in SaveCommandTools,
		// below — at the write, so a second caller cannot skip them.
	}
	saved, err := SaveCommandTools(T.DB, st.Slug, cmd.Name, stringArg(args, "description"), acts)
	if err != nil {
		return "", err
	}
	// Never live on save: SaveCommandTools switches Agents off, so a re-map
	// of an approved command is approved again before agents get it.
	live := "and it cannot be called yet: an admin turns Agents on for this command, on this row, and that switch is the approval"
	return fmt.Sprintf("Mapped %s: %s as %s, %s. Call this again to correct it.",
		cmd.Label, countOf(len(saved.Tools), "action", "actions"), saved.ToolName(), live), nil
}

// templatePlaceholders lists the {name} placeholders in a command template.
func templatePlaceholders(tpl string) []string {
	var out []string
	for {
		i := strings.IndexByte(tpl, '{')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(tpl[i:], '}')
		if j < 0 {
			return out
		}
		if name := strings.TrimSpace(tpl[i+1 : i+j]); name != "" {
			out = append(out, name)
		}
		tpl = tpl[i+j+1:]
	}
}

// cachedOrch memoizes the orchestrate app. Looked up rather than held as a field
// because neither app should depend on the other's registration order — the same
// seam guides uses.
var cachedOrch *orchestrate.OrchestrateApp

func findOrchestrate() *orchestrate.OrchestrateApp {
	if cachedOrch != nil {
		return cachedOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedOrch = o
	return o
}
