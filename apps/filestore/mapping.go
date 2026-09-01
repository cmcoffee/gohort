// The mapping conversation: working out what a registered command can do, and
// writing it down as a toolbox on that command's own row.
//
// Step 4 of docs/local-command-tools.md. A registered command is one binary an
// admin pointed at a folder, called as `<cmd> <folder>`. That shape is all
// gohort knows about it — enough for a person to click, not enough for an agent
// to use well. What it can actually DO lives in its --help and in trying it, and
// only a person had ever read that.
//
// A conversation rather than a button, because the first --help rarely says
// everything and what a flag MEANS is a judgement an admin should be able to
// push back on before it becomes a description other agents trust.
//
// WHY THE AGENT NEEDS NO AUTHORITY. What it produces is inert. Mapping writes a
// toolbox; nothing can call it until a person attaches it to a folder, and that
// attachment is the approval. Probing runs only the ONE command this
// conversation was opened on — an admin chose that binary, and the agent is
// choosing flags for it.
//
// And it lands where it came from. The toolbox carries FromStore/FromCommand, so
// the command's row shows it the moment the conversation ends. The arrangement
// this replaces (v0.6.537, reverted) wrote loose tools into the global tool list
// where they read as orphaned, waiting to be found and bound from a different
// expander.

package filestore

import (
	"context"
	"encoding/json"
	"fmt"
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
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	st, ok := LoadStore(T.DB, slug)
	if !ok || user == "" {
		http.NotFound(w, r)
		return
	}
	cmd, ok := LoadStoreCommand(T.DB, slug, RefToolSlug(strings.TrimSpace(r.URL.Query().Get("command"))))
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
	orch.PublicHandleSendWithAppTools(w, r, agent, T.mappingTools(r.Context(), st, cmd))
}

// mappingTools are the two an agent gets: one to LEARN the command, one to write
// down what it learned. Deliberately two, and in that order — an agent that
// could only propose would be guessing from a name, and one that could only
// probe would leave nothing behind.
func (T *FileStoreApp) mappingTools(ctx context.Context, st Store, cmd StoreCommand) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name: "probe_command",
				Description: fmt.Sprintf(
					"Run %s (%s), the command you are mapping, with arguments of your choosing — start with its help flag. Returns whatever it printed, INCLUDING when it exits non-zero, because a usage message is usually printed on a failing exit and that is the most useful thing a probe gets back. This is how you learn the interface you are about to describe; do not guess it.",
					cmd.Label, cmd.Command),
				Parameters: map[string]ToolParam{
					"args": {Type: "string", Description: "Arguments to pass, space separated — e.g. \"--help\". Passed as separate argv entries with NO shell, so quoting and metacharacters do nothing."},
				},
				Caps: []Capability{CapExecute},
			},
			Handler: func(args map[string]any) (string, error) {
				return T.probeCommand(ctx, cmd, stringArg(args, "args"))
			},
		},
		{
			Tool: Tool{
				Name: "propose_toolbox",
				Description: fmt.Sprintf(
					"Write down what %s can do, as ONE toolbox with an action per thing it does. Propose narrow actions rather than one that takes a mode argument — unpack and verify are two actions, not one with a switch. The toolbox is inert: nobody can call it until an admin attaches it to a folder, so propose what you actually verified and say in each description what you did not. Calling this again REPLACES the previous proposal for this command, which is how you correct one.",
					cmd.Label),
				Parameters: map[string]ToolParam{
					"name":        {Type: "string", Description: "snake_case handle for the toolbox, e.g. \"capture_tools\". Name it for the BINARY, not for this folder — it will be attached to every folder of the same kind."},
					"description": {Type: "string", Description: "What this binary is for, in a sentence. Read before the toolbox is opened."},
					"actions": {Type: "string", Description: "JSON array of actions: [{\"name\":\"unpack\",\"description\":\"...\",\"command_template\":\"/opt/bin/cap unpack {folder}\",\"params\":{\"folder\":{\"type\":\"string\",\"description\":\"...\"}},\"required\":[\"folder\"]}]. " +
						"Every {placeholder} in a command_template must be declared in that action's params."},
				},
				Required: []string{"name", "description", "actions"},
				Caps:     []Capability{CapWrite},
			},
			Handler: func(args map[string]any) (string, error) {
				return T.proposeToolbox(st, cmd, args)
			},
		},
	}
}

// probeCommand runs the command being mapped with the agent's arguments.
//
// Constrained to that ONE binary: an admin registered it against this folder,
// and the agent is choosing flags, never an executable. That is the whole reason
// this can exist without the approval machinery a free-form shell tool needs.
func (T *FileStoreApp) probeCommand(ctx context.Context, cmd StoreCommand, argLine string) (string, error) {
	out, err := runRegisteredCommand(ctx, cmd.Command, strings.Fields(argLine)...)
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

// proposeToolbox writes the mapped toolbox against the command it came from.
func (T *FileStoreApp) proposeToolbox(st Store, cmd StoreCommand, args map[string]any) (string, error) {
	var acts []TempToolAction
	raw := strings.TrimSpace(stringArg(args, "actions"))
	if raw == "" {
		return "", Error("actions is required — a toolbox with none does nothing")
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
					" — declare it, or take it out of the command")
			}
		}
	}
	saved, err := SaveToolbox(T.DB, StoreToolbox{
		Tool: TempTool{
			Name:        stringArg(args, "name"),
			Description: strings.TrimSpace(stringArg(args, "description")),
			Actions:     acts,
		},
		FromStore:   st.Slug,
		FromCommand: cmd.Name,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"Mapped %s into the toolbox %q (%s). It shows on this command's row now, and it cannot be called yet: an admin attaches it to a folder under Toolboxes, and that attachment is the approval. Call this again to correct it.",
		cmd.Label, saved.Tool.Name, countOf(len(saved.Tool.Actions), "action", "actions")), nil
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
