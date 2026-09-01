// Mapping a registered command into tools.
//
// A registered command is one binary an admin pointed at a folder, called as
// `<cmd> <folder>`. That shape is all gohort knows about it — enough for a
// person to click, and not enough for an agent to use well. What the binary can
// actually DO (subcommands, flags, what its output means, which failures are
// expected) lives in its --help and in trying it, and only a person had ever
// read that.
//
// So: let an agent read it. It probes the binary, and what it produces is not a
// wrapper but TOOLS — typed parameters, a description written for a model, a
// command template. Ordinary gohort tools from then on, scoped and approved and
// repaired by the machinery that already does those things, rather than a
// filestore side channel with its own rules.
//
// WHY MINTING IS SAFE WHERE THE MIDDLE-MAN WAS NOT. A proposed tool is INERT.
// Minting writes a record; nothing can call it until an admin binds it to the
// store in the Tools picker, and that binding is the approval. The agent that
// maps cannot grant itself anything — it can only leave a proposal for a person
// to accept, which is the same deal every other authored tool gets.

package filestore

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// storeToolsTable holds tools minted against a store, keyed "<slug>/<name>".
//
// Global to the store, not per user, because that is what the tool IS: a
// property of the folder and the binary that operates on it, discovered once.
// Per-user would have meant every user who reaches a shared store re-mapping
// the same command, and a binding an admin made resolving to nothing for
// everyone else — which is exactly how the per-user pool behaved when the
// bundle first shipped.
const storeToolsTable = "file_store_tools"

func storeToolKey(slug, name string) string { return slug + "/" + name }

// SaveStoreTool records a minted tool against a store. Inert on arrival: it is
// bound (and thereby approved) separately, by a person.
func SaveStoreTool(db Database, slug string, t TempTool) (TempTool, error) {
	slug = strings.TrimSpace(slug)
	t.Name = RefToolSlug(strings.TrimSpace(t.Name))
	switch {
	case slug == "":
		return t, Error("say which store this tool belongs to")
	case t.Name == "":
		return t, Error("give the tool a name — a short handle like \"unpack_capture\"")
	case strings.TrimSpace(t.Description) == "":
		return t, Error("give the tool a description: it is the only thing another agent reads to decide whether to call it")
	case strings.TrimSpace(t.CommandTemplate) == "":
		return t, Error("give the tool a command template — the command line to run, with {placeholders} for its parameters")
	}
	// Bound-only always. A tool minted for one folder of captures has no
	// business in every chat the user has, and the whole reason the bundle can
	// be narrow is that its members opt out of the global catalog.
	t.BoundOnly = true
	db.Set(storeToolsTable, storeToolKey(slug, t.Name), t)
	return t, nil
}

// StoreTools returns the tools minted against one store, bound or not.
func StoreTools(db Database, slug string) []TempTool {
	var out []TempTool
	if db == nil || slug == "" {
		return out
	}
	prefix := slug + "/"
	for _, k := range db.Keys(storeToolsTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var t TempTool
		if db.Get(storeToolsTable, k, &t) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DeleteStoreTool drops a minted tool. The binding is left alone: a name in a
// store's Toolset that resolves to nothing is skipped, and cleaning the list up
// is the admin's business rather than something a delete should do behind them.
func DeleteStoreTool(db Database, slug, name string) {
	if db != nil {
		db.Unset(storeToolsTable, storeToolKey(slug, RefToolSlug(strings.TrimSpace(name))))
	}
}

// mappingTools are the two an agent gets while mapping: one to LEARN the
// command, one to write down what it learned.
//
// Deliberately two, and deliberately in that order. An agent that could only
// propose would be guessing from a name; an agent that could only probe would
// leave nothing behind.
func (T *FileStoreApp) mappingTools(ctx context.Context, user string, st Store) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "probe_command",
				Description: fmt.Sprintf("Run one of the commands registered against %q with arguments of your choosing, to find out what it can do — start with its help flag. Returns whatever it printed, including on failure, because a usage message is usually printed to a failing exit. This is how you learn the interface you are about to describe; do not guess it.", st.Name),
				Parameters: map[string]ToolParam{
					"command": {Type: "string", Description: "The registered command's handle, from the list you were given."},
					"args":    {Type: "string", Description: "Arguments to pass, space separated — e.g. \"--help\". They are passed as separate argv entries with NO shell, so quoting and metacharacters do nothing."},
				},
				Required: []string{"command"},
				Caps:     []Capability{CapExecute},
			},
			Handler: func(args map[string]any) (string, error) {
				return T.probeCommand(ctx, st, strings.TrimSpace(fmt.Sprint(args["command"])), fmt.Sprint(args["args"]))
			},
		},
		{
			Tool: Tool{
				Name:        "propose_tool",
				Description: fmt.Sprintf("Write down one thing the command can do as a reusable tool for the %q store. Propose SEVERAL narrow tools rather than one that takes a subcommand argument — \"unpack_capture\" and \"verify_capture\" are two tools, not one with a mode. The proposal is inert: nobody can call it until an admin binds it to the store, so propose what you actually verified and say in the description what you did not.", st.Name),
				Parameters: map[string]ToolParam{
					"name":        {Type: "string", Description: "snake_case handle, e.g. \"unpack_capture\"."},
					"description": {Type: "string", Description: "What it does and WHEN to reach for it, written for another agent that has never seen this binary. This is the only thing that will be read before calling it."},
					"command":     {Type: "string", Description: "The command line to run, with {placeholders} matching the parameter names — e.g. \"/opt/bin/cap unpack {folder}\"."},
					"parameters":  {Type: "string", Description: "JSON object of parameters: {\"folder\":{\"type\":\"string\",\"description\":\"...\"}}. Omit for a tool that takes none."},
					"required":    {Type: "string", Description: "Comma-separated names of the parameters that are required."},
				},
				Required: []string{"name", "description", "command"},
				Caps:     []Capability{CapWrite},
			},
			Handler: func(args map[string]any) (string, error) {
				return T.proposeTool(st, args)
			},
		},
	}
}

// probeCommand runs a registered command with the agent's arguments.
//
// Constrained to what an admin already registered: the binary is one of the
// store's own commands, never a path the agent names. That is the whole reason
// this can exist without the approval machinery a free-form shell tool needs —
// the executable was chosen by a person, and the agent is only choosing flags.
func (T *FileStoreApp) probeCommand(ctx context.Context, st Store, name, argLine string) (string, error) {
	cmd, ok := LoadStoreCommand(T.DB, st.Slug, RefToolSlug(strings.TrimSpace(name)))
	if !ok {
		avail := []string{}
		for _, c := range StoreCommandsFor(T.DB, st.Slug) {
			avail = append(avail, c.Name)
		}
		if len(avail) == 0 {
			return "", Error("no commands are registered against " + st.Name + " — there is nothing here to map")
		}
		return "", Error("no command called " + name + "; registered: " + strings.Join(avail, ", "))
	}
	out, err := runRegisteredCommand(ctx, cmd.Command, strings.Fields(argLine)...)
	out = strings.TrimSpace(out)
	if err != nil {
		// Returned as a RESULT, not an error: a usage message is usually printed
		// on a failing exit, and that is the single most useful thing a probe
		// gets back. Handing the agent "exit status 2" and dropping the text
		// would make the tool useless for the job it exists for.
		return fmt.Sprintf("(exited with an error: %v)\n%s", err, out), nil
	}
	if out == "" {
		return "(ran, printed nothing)", nil
	}
	return out, nil
}

// proposeTool writes one mapped tool against the store.
func (T *FileStoreApp) proposeTool(st Store, args map[string]any) (string, error) {
	t := TempTool{
		Name:            fmt.Sprint(args["name"]),
		Description:     strings.TrimSpace(fmt.Sprint(args["description"])),
		CommandTemplate: strings.TrimSpace(fmt.Sprint(args["command"])),
	}
	if raw := strings.TrimSpace(fmt.Sprint(args["parameters"])); raw != "" && raw != "<nil>" {
		if err := json.Unmarshal([]byte(raw), &t.Params); err != nil {
			return "", Error("parameters must be a JSON object of {name: {type, description}}: " + err.Error())
		}
	}
	if raw := strings.TrimSpace(fmt.Sprint(args["required"])); raw != "" && raw != "<nil>" {
		for _, n := range strings.Split(raw, ",") {
			if n = strings.TrimSpace(n); n != "" {
				t.Required = append(t.Required, n)
			}
		}
	}
	// Every placeholder has to be a parameter, or the tool is unusable the first
	// time it is called and nobody finds out until then.
	for _, ph := range templatePlaceholders(t.CommandTemplate) {
		if _, ok := t.Params[ph]; !ok {
			return "", Error("the command template uses {" + ph + "} but no parameter called " + ph + " is declared — declare it, or take it out of the template")
		}
	}
	saved, err := SaveStoreTool(T.DB, st.Slug, t)
	if err != nil {
		return "", err
	}
	return "Proposed " + saved.Name + " for " + st.Name + ". It cannot be called yet: an admin binds it to the store under Tools, and that binding is the approval. Keep going if there is more this command can do.", nil
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

// handleMapChat is the back-and-forth. An admin opens it on a store and works
// through its commands with the agent — probe, read, propose, correct — because
// mapping a binary is not a thing to one-shot: the first --help rarely says
// everything, and what a flag MEANS is a judgement a person should be able to
// push back on before it becomes a tool description other agents trust.
func (T *FileStoreApp) handleMapChat(w http.ResponseWriter, r *http.Request) {
	user := AuthCurrentUser(r)
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	st, ok := LoadStore(T.DB, slug)
	if !ok || user == "" {
		http.NotFound(w, r)
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
	orch.PublicHandleSendWithAppTools(w, r, agent, T.mappingTools(r.Context(), user, st))
}

// mapAgentID is the agent that does the mapping. Builder, because this IS
// authoring — it already knows how to write a tool description worth reading and
// what a good parameter looks like, and giving the job to a second agent would
// mean maintaining a second opinion about that.
const mapAgentID = "builder"

// cachedOrch memoizes the orchestrate app. Looked up rather than held as a
// field because neither app should depend on the other's registration order —
// the same seam guides uses.
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
