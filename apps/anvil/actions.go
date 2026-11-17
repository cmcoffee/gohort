// Project actions: the operator's own commands, exposed as buttons.
//
// Anvil edits a tree; it does not know what to DO with a tree. Building,
// testing, linting, restarting the thing the tree produces — all of that is
// specific to the project, and none of it belongs compiled into an app whose
// whole point is being domain-agnostic. So a project carries a list of
// commands its operator wrote, and the page renders them as buttons.
//
// A rebuild-and-restart falls out of this as an instance rather than a
// feature: two actions, where the restart declares the build as its GATE and
// therefore cannot fire on a red build. That invariant is the one thing here
// that is NOT configurable — an operator may choose whether an action is
// gated, but may not ask a gated action to run anyway when its gate fails.
// The alternative is a button that swaps a working binary for one that does
// not compile, which is precisely the failure the gate exists to prevent.
//
// TRUST BOUNDARY. Action commands run on the host, unsandboxed, in the
// project root. That is deliberate and it is why they are OPERATOR-authored:
// a build needs the toolchain, the module cache, and a real environment, and
// a restart is inherently a host-level act. The LLM never writes an action.
// At most it may INVOKE one an operator already wrote, and only when that
// action opts in — the model chooses among the operator's verbs, it does not
// supply them. Commands the model composes itself go through the sandbox
// instead (see the `run` tool in tools.go), which is a different trust level
// and gets different treatment.
package anvil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// defaultActionTimeout bounds an action that never returns. A build that
// hangs should surface as a timeout with its partial output, not as a request
// that never answers.
const defaultActionTimeout = 10 * time.Minute

// maxActionOutput caps what comes back from an action. A verbose build's
// last lines are the ones that matter; the head is what gets trimmed.
const maxActionOutput = 40000

// ProjectAction is one operator-defined command.
type ProjectAction struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Command string `json:"command"`

	// GateID names another action on the same project that must exit 0
	// before this one runs. The gate's output is returned alongside on
	// failure, because "the restart refused" is not useful without the
	// compiler error that caused it.
	GateID string `json:"gate_id,omitempty"`

	// Confirm, when set, is the question the browser asks before firing.
	Confirm string `json:"confirm,omitempty"`

	// TimeoutSec bounds the run. Zero means defaultActionTimeout.
	TimeoutSec int `json:"timeout_sec,omitempty"`

	// Detached runs the command without waiting for it, for the one case
	// where waiting is impossible: an action that stops the server it was
	// invoked through. The reply is written BEFORE the command starts, so
	// the operator sees "restarting" rather than a dead socket.
	//
	// A detached action reports only that it launched. It cannot report
	// success, which is exactly why gating it on a build matters.
	Detached bool `json:"detached,omitempty"`

	// ExposeToAgent lets the coding agent invoke this action as a tool —
	// "run the tests" becoming an actual test run rather than a suggestion
	// that the user run them. Off by default: an operator decides which of
	// their verbs a model may reach, and a detached action is never
	// exposed regardless (see agentInvokableActions).
	ExposeToAgent bool `json:"expose_to_agent,omitempty"`
	// AgentHint describes the action to the model in its own terms ("runs
	// the unit tests and returns failures"). Falls back to the label.
	AgentHint string `json:"agent_hint,omitempty"`
}

// findAction locates an action by ID on a project.
func (p Project) findAction(id string) (ProjectAction, bool) {
	id = strings.TrimSpace(id)
	for _, a := range p.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return ProjectAction{}, false
}

// agentInvokableActions is the subset the coding agent may call.
//
// Detached actions are excluded unconditionally, not by operator choice. A
// detached action's whole shape is "fire something that takes the server
// down and answer before it does" — there is no result for a model to read,
// no way for it to tell success from failure, and no way for a person to
// intervene between the decision and the effect. A model may build and test;
// bouncing the process stays a human's button.
func (p Project) agentInvokableActions() []ProjectAction {
	var out []ProjectAction
	for _, a := range p.Actions {
		if a.ExposeToAgent && !a.Detached {
			out = append(out, a)
		}
	}
	return out
}

// ActionResult is one action run's outcome.
type ActionResult struct {
	ActionID string `json:"action_id"`
	Label    string `json:"label"`
	OK       bool   `json:"ok"`
	Output   string `json:"output"`
	// GateFailed marks the specific refusal worth its own message: the
	// action never ran because its gate did not pass.
	GateFailed bool   `json:"gate_failed,omitempty"`
	GateLabel  string `json:"gate_label,omitempty"`
	Detached   bool   `json:"detached,omitempty"`
}

// runAction runs one action in the project root, honoring its gate.
//
// A gate is resolved and run FIRST, and its failure is terminal — this is the
// build-gate invariant, and it lives here rather than in the caller so that
// every path to an action (button, agent tool, future scheduler) gets it
// without having to remember.
func (p Project) runAction(ctx context.Context, base string, a ProjectAction) ActionResult {
	res := ActionResult{ActionID: a.ID, Label: a.Label}

	if a.GateID != "" {
		gate, ok := p.findAction(a.GateID)
		if !ok {
			res.Output = fmt.Sprintf("This action is gated on an action that no longer exists (%s). Fix the project's actions before running it.", a.GateID)
			res.GateFailed = true
			return res
		}
		if gate.ID == a.ID {
			res.Output = "This action is gated on itself, which can never pass. Fix the project's actions."
			res.GateFailed = true
			return res
		}
		// A gate is run for its exit status, so a gate that is itself
		// detached (and therefore has no exit status) cannot gate anything.
		if gate.Detached {
			res.Output = fmt.Sprintf("The gate %q is detached, so it has no exit status to gate on. A gate must be a command that finishes.", gate.Label)
			res.GateFailed = true
			return res
		}
		out, err := runShellIn(ctx, base, gate.Command, gate.timeout())
		if err != nil {
			res.GateFailed = true
			res.GateLabel = gate.Label
			res.Output = fmt.Sprintf("%s failed, so %s did not run.\n\n%s\n%s", gate.Label, a.Label, err, out)
			return res
		}
	}

	if a.Detached {
		if err := startShellIn(base, a.Command); err != nil {
			res.Output = fmt.Sprintf("could not launch: %s", err)
			return res
		}
		res.OK = true
		res.Detached = true
		res.Output = fmt.Sprintf("%s launched. It runs detached, so there is no result to report here.", a.Label)
		return res
	}

	out, err := runShellIn(ctx, base, a.Command, a.timeout())
	res.Output = out
	if err != nil {
		res.Output = strings.TrimSpace(out + "\n" + err.Error())
		return res
	}
	res.OK = true
	if strings.TrimSpace(res.Output) == "" {
		res.Output = fmt.Sprintf("%s finished with no output.", a.Label)
	}
	return res
}

func (a ProjectAction) timeout() time.Duration {
	if a.TimeoutSec > 0 {
		return time.Duration(a.TimeoutSec) * time.Second
	}
	return defaultActionTimeout
}

// runShellIn runs command in dir and returns combined output. A non-zero
// exit is an error whose message names the status, because "exit status 2"
// alongside the compiler's own output is what makes a failed build readable.
func runShellIn(ctx context.Context, dir, command string, timeout time.Duration) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("no command configured")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := trimOutput(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("timed out after %s", timeout)
	}
	return text, err
}

// startShellIn launches a command without waiting. Used only by detached
// actions; the process is deliberately not tracked, since the common case is
// that it replaces the server that started it.
func startShellIn(dir, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("no command configured")
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap it so a non-replacing detached command doesn't linger as a
	// zombie for the life of the server.
	go func() { _ = cmd.Wait() }()
	return nil
}

// trimOutput keeps the TAIL of oversized output. The end of a build log is
// where the error is; the beginning is where the file list is.
func trimOutput(s string) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= maxActionOutput {
		return s
	}
	cut := s[len(s)-maxActionOutput:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 {
		cut = cut[i+1:]
	}
	return "...(earlier output trimmed)...\n" + cut
}

// actionsAllowed reports whether this caller may fire actions on a project.
//
// Actions are admin-only, full stop, on any project: the commands run
// unsandboxed on the host as the gohort process, so being allowed to open a
// tree is not the same permission as being allowed to run its build. A clone
// project's actions used to belong to the user whose checkout it was, which
// gave every user granted Anvil a shell on the server. An admin still has to
// be the project's owner (or it is a root project) to run a clone's actions.
func actionsAllowed(p Project, user string, isAdmin bool) bool {
	if !isAdmin {
		return false
	}
	if p.Kind == KindRoot {
		return true
	}
	return strings.EqualFold(p.Owner, user)
}

// validateActions normalizes an incoming action list and rejects the shapes
// that cannot work, so a broken gate is caught when it is saved rather than
// when someone presses the button.
func validateActions(in []ProjectAction) ([]ProjectAction, error) {
	seen := map[string]bool{}
	out := make([]ProjectAction, 0, len(in))
	for _, a := range in {
		a.Label = strings.TrimSpace(a.Label)
		a.Command = strings.TrimSpace(a.Command)
		if a.Label == "" || a.Command == "" {
			continue // an empty row is a half-filled form, not an error
		}
		if a.ID == "" {
			a.ID = UUIDv4()
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("duplicate action id %q", a.ID)
		}
		seen[a.ID] = true
		out = append(out, a)
	}
	for _, a := range out {
		if a.GateID == "" {
			continue
		}
		if !seen[a.GateID] {
			return nil, fmt.Errorf("action %q is gated on an action that isn't in this project", a.Label)
		}
		if a.GateID == a.ID {
			return nil, fmt.Errorf("action %q is gated on itself", a.Label)
		}
	}
	return out, nil
}
