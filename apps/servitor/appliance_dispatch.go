// Running a minted tool: four gates, in the order that fails cheapest first.
//
//	approved?   the owner said yes to THIS command, once, having read it
//	renders?    every required value present, every enum satisfied
//	unchanged?  the rendered command is no riskier than what was approved
//	then run
//
// The ordering is not cosmetic. Approval and rendering cost nothing and need no
// connection, so a tool nobody approved never opens an SSH session, and a
// missing argument is reported before anything is dialled.
//
// WHAT THIS PATH DOES NOT DO is let a model contribute structure. It receives a
// tool NAME and a map of values; the command comes from the stored template.
// There is deliberately no branch here that accepts a command string, because
// the moment one exists everything upstream — the mint-time classification, the
// owner's reading of the template, the enum — becomes advisory.
package servitor

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// ApplianceDispatch is one call of one minted tool.
type ApplianceDispatch struct {
	Appliance Appliance
	ToolName  string
	Args      map[string]any
	// AgentID is who is calling, for the log and for the acting-agent stamp.
	// It no longer decides permission here: a minted tool is gated by its own
	// approval, not by whose turn is running.
	AgentID string
	UserID  string
}

// DispatchApplianceTool runs a minted tool and returns its output.
//
// Every refusal is worded for the agent that will read it: what was refused,
// why, and what would change the answer. A refusal the model cannot act on gets
// relayed to a person as "it didn't work".
func DispatchApplianceTool(ctx context.Context, udb Database, d ApplianceDispatch) (string, error) {
	tool, ok := LoadApplianceTool(udb, d.Appliance.ID, d.ToolName)
	if !ok {
		return "", fmt.Errorf("no tool named %q exists for %s — ask for the capability first, and the owner approves it before it can run",
			d.ToolName, applianceLabel(d.Appliance.Name, d.Appliance.ID))
	}
	if !tool.Approved {
		return "", fmt.Errorf("%q exists for %s but the owner has not approved it yet. Nothing ran and nothing is queued — tell the person it is waiting on their approval, and do not look for another way to do it",
			tool.Name, applianceLabel(d.Appliance.Name, d.Appliance.ID))
	}
	// Path-scoped parameters are checked HERE, when the tool runs, and
	// replaced by the absolute path they resolved to.
	//
	// Quoting is not containment: renderApplianceCommand single-quotes a
	// value so it can never contribute syntax, which does nothing about
	// "../../var/lib/something" being a well-formed argument pointing
	// somewhere else. An enum is the usual answer and cannot express a
	// set that changes, which is exactly the case here — the folders
	// under a drop directory. See core/path_scope.go.
	// The CALLING agent is passed as well as the user: a scoped root that
	// is also an attachable source has to be linked to this agent, not
	// merely reachable by its owner. See core.ResolvePathScope.
	args, err := resolveScopedArgs(d.UserID, d.AgentID, tool, d.Args)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool.Name, err)
	}
	cmd, err := renderApplianceCommand(tool, args)
	if err != nil {
		// An argument problem, not a permission one. Said plainly so the model
		// fixes the call rather than concluding it lacks access.
		return "", fmt.Errorf("%s: %w", tool.Name, err)
	}

	// THE APPROVAL IS THE GATE. No category check here, deliberately.
	//
	// It used to ask autoRunAllowed as well, and that was double-gating that
	// defeated the design: with no category ticked — the configuration this
	// whole feature recommends — an approved capability was refused anyway, so
	// the only way to use one was to grant a broad risk category permitting far
	// more than the command that had actually been read and approved. The safe
	// setup did not work, and the fix for it was the unsafe setup.
	//
	// Approving a minted tool is a stronger act than granting a category: it is
	// a decision about ONE frozen command, made while looking at it. Requiring a
	// standing permission on top asks for a weaker, broader consent to unlock a
	// narrower, stronger one.
	//
	// Risk categories keep their job — free-form commands, from the console or
	// any path that is not a minted tool. That is the gate in web.go, untouched.
	//
	// Still classified, and still compared against the mint-time category: a
	// render that comes out HIGHER than what was approved means the arguments
	// pushed it somewhere the owner did not agree to, and that is refused
	// however permissive the grants are.
	cat := tool.Risk
	if rendered, _ := classify_command_scoped(cmd, ""); riskRank(rendered) > riskRank(cat) {
		Log("[servitor] %q rendered as %s but was approved as %s — refusing", tool.Name, rendered, cat)
		return "", fmt.Errorf("%q was approved as a %s command, but with these values it reads as %s. Nothing ran. The arguments changed what the command does beyond what was approved — use different values, or have the owner approve a capability that covers this",
			tool.Name, string(cat), string(rendered))
	}

	conn, err := acquireConn(d.UserID, d.Appliance)
	if err != nil {
		return "", fmt.Errorf("could not reach %s: %w", applianceLabel(d.Appliance.Name, d.Appliance.ID), err)
	}
	// The rendered command is logged, not the argument map: this is the string
	// that actually ran, and it is the only record that answers "what did the
	// agent do on that box".
	Log("[servitor] agent=%q running %q on %s: %s", d.AgentID, tool.Name, d.Appliance.ID, cmd)
	return execOverSSH(ctx, conn, cmd)
}

// riskRank orders categories so "the higher of two" has a meaning. Unknown
// names rank ABOVE everything known: a category this build does not recognize
// is one that arrived from somewhere newer, and treating it as harmless is the
// wrong way to be wrong.
func riskRank(c RiskCategory) int {
	if c == RiskNone {
		return 0
	}
	for i, known := range AllRiskCategories {
		if known == c {
			return i + 1
		}
	}
	return len(AllRiskCategories) + 1
}

// ApplianceToolDefs turns a system's APPROVED tools into definitions an agent
// can be offered. Unapproved ones are absent rather than visible-and-refusing:
// a tool in the catalog is a promise it can be called.
//
// Each handler closes over the appliance and stamps the acting agent, so the
// grant lookup downstream cannot be fooled by anything the model passes.
// sess carries the turn's cancelable context so a stopped turn stops the
// command it sent to the machine, instead of the dispatch running on
// detached time with nobody left to read the result. Nil-safe.
func ApplianceToolDefs(sess *ToolSession, udb Database, userID, agentID string, appliance Appliance) []AgentToolDef {
	var out []AgentToolDef
	// One listing per root per catalog build, not per tool: several tools
	// on the same machine usually scope to the same store, and this runs
	// on every turn.
	folders := map[string][]string{}
	for _, t := range ListApplianceTools(udb, appliance.ID) {
		if !t.Approved {
			continue
		}
		tool, app := t, appliance
		out = append(out, AgentToolDef{
			Tool: Tool{
				Name:        tool.Name,
				Description: applianceToolDescription(tool, app),
				Parameters:  describeScopedParams(userID, tool.Params, folders),
				Required:    tool.Required,
				Caps:        []Capability{CapWrite},
			},
			Handler: func(args map[string]any) (string, error) {
				return DispatchApplianceTool(WithActingAgent(sess.Context(), agentID), udb,
					ApplianceDispatch{Appliance: app, ToolName: tool.Name, Args: args, AgentID: agentID, UserID: userID})
			},
		})
	}
	return out
}

// maxListedFolders caps how many names go into one parameter
// description. A drop directory with a folder per ticket runs to
// hundreds, and a tool description is prompt on every turn.
const maxListedFolders = 40

// describeScopedParams returns a copy of params with each path-scoped
// one's description carrying the folder names that are valid RIGHT NOW.
//
// Without this the constraint is invisible to the model: the parameter
// says "which bundle to parse", the scope is enforced somewhere it
// cannot see, and the first call is a guess that gets refused. The names
// were always available (core.PathScopeChoices) and nothing asked for
// them.
//
// A COPY, because tool.Params is the stored record and this text is a
// per-turn snapshot of a directory — writing it back would persist one
// moment's listing into the frozen tool.
//
// The snapshot is honest about being one: a folder that appears after
// the catalog was built still resolves, because the check runs when the
// tool runs. Saying so matters — otherwise a model that has been handed
// a list treats it as exhaustive and refuses to try the folder somebody
// just named.
func describeScopedParams(userID string, params map[string]ToolParam, memo map[string][]string) map[string]ToolParam {
	var out map[string]ToolParam
	for name, p := range params {
		ref := strings.TrimSpace(p.PathScope)
		if ref == "" {
			continue
		}
		if out == nil {
			out = make(map[string]ToolParam, len(params))
			for k, v := range params {
				out[k] = v
			}
		}
		choices, cached := memo[ref]
		if !cached {
			choices = PathScopeChoices(userID, ref)
			memo[ref] = choices
		}
		p.Description = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p.Description), ".")+". ") +
			scopeHint(choices)
		out[name] = p
	}
	if out == nil {
		return params
	}
	return out
}

// scopeHint is the sentence appended to a scoped parameter.
func scopeHint(choices []string) string {
	if len(choices) == 0 {
		// Not "nothing is valid" — the honest reading is that the folder
		// this is scoped to has nothing in it yet, or cannot be read. A
		// model told "no valid values" concludes the tool is broken and
		// stops; told what is actually true, it can say so.
		return "Name one folder in the store this is limited to. Nothing is in it right now, " +
			"so nothing will resolve until a folder appears — say that rather than guessing a name."
	}
	shown := choices
	extra := 0
	if len(shown) > maxListedFolders {
		extra = len(shown) - maxListedFolders
		shown = shown[:maxListedFolders]
	}
	hint := "Name ONE folder, exactly as listed: " + strings.Join(shown, ", ") + "."
	if extra > 0 {
		hint += " (" + strconv.Itoa(extra) + " more not listed — the store's own list tool has all of them.)"
	}
	return hint + " Read when this turn started; a folder added since still works. A path is not accepted here — a name is."
}

// applianceToolDescription says what it does and WHERE, because an agent that
// can reach two boxes has two tools whose names may differ only by suffix, and
// running the right command on the wrong machine is the mistake this prevents.
func applianceToolDescription(t ApplianceTool, a Appliance) string {
	desc := strings.TrimSpace(t.Description)
	if desc == "" {
		desc = "Run a prepared command"
	}
	return fmt.Sprintf("%s — on %s. Prepared and approved in advance; you supply values only, never the command.",
		strings.TrimSuffix(desc, "."), applianceLabel(a.Name, a.ID))
}

// resolveScopedArgs replaces every path-scoped argument with the absolute
// path it resolves to, refusing anything that lands outside its root.
//
// Returns a COPY: the caller's map is what gets logged and echoed back,
// and rewriting it in place would make the record disagree with what the
// model actually asked for.
func resolveScopedArgs(user, agentID string, tool ApplianceTool, args map[string]any) (map[string]any, error) {
	// core.ResolveScopedArgs is this function, lifted (v0.6.240) once a
	// second dispatch path needed it. It also returns the paths it
	// resolved, which an appliance does not use: an appliance runs the
	// command on the TARGET, so there is no local sandbox to bind them
	// into.
	out, _, err := ResolveScopedArgs(user, agentID, tool.Params, args)
	return out, err
}
