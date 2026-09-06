package temptool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// ----------------------------------------------------------------------
// list_temp_tools
// ----------------------------------------------------------------------

type ListTempToolsTool struct{}

func (t *ListTempToolsTool) Name() string { return "list_temp_tools" }

func (t *ListTempToolsTool) Caps() []Capability { return []Capability{CapExecute} }

func (t *ListTempToolsTool) Desc() string {
	return "List the temp tools currently defined for this session. Returns each tool's name, description, parameters, and command template — useful for reviewing what you've built before deciding whether to add another."
}

func (t *ListTempToolsTool) Params() map[string]ToolParam { return map[string]ToolParam{} }

func (t *ListTempToolsTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("list_temp_tools requires a session")
}

func (t *ListTempToolsTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("list_temp_tools requires a session")
	}
	// Categorize: each in-session tool is either loaded-from-persistent,
	// pending-approval, or session-only. Look up persistence state by
	// name once.
	persistentByName := make(map[string]bool)
	pendingByName := make(map[string]bool)
	if sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			persistentByName[p.Tool.Name] = true
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			pendingByName[p.Tool.Name] = true
		}
	}

	tools := sess.CopyTempTools()
	if len(tools) == 0 && len(pendingByName) == 0 {
		return "No temp tools defined in this session.", nil
	}
	var b strings.Builder
	for i, t := range tools {
		var tag string
		switch {
		case persistentByName[t.Name]:
			tag = " [persistent]"
		case pendingByName[t.Name]:
			tag = " [pending approval]"
		default:
			tag = " [session-only]"
		}
		fmt.Fprintf(&b, "%d. %s%s — %s\n", i+1, t.Name, tag, t.Description)
		fmt.Fprintf(&b, "   command: %s\n", t.CommandTemplate)
		if len(t.Params) > 0 {
			b.WriteString("   params: ")
			first := true
			for k, p := range t.Params {
				if !first {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s (%s)", k, p.Type)
				first = false
			}
			b.WriteString("\n")
		}
	}
	// Also list pending tools that aren't currently in the session
	// (e.g. requested in a prior session, still waiting on approval).
	var orphanPending []string
	inSession := make(map[string]bool, len(tools))
	for _, t := range tools {
		inSession[t.Name] = true
	}
	for name := range pendingByName {
		if !inSession[name] {
			orphanPending = append(orphanPending, name)
		}
	}
	if len(orphanPending) > 0 {
		b.WriteString("\nPending approval (not yet visible to the LLM until approved): ")
		b.WriteString(strings.Join(orphanPending, ", "))
		b.WriteString("\n")
	}
	return b.String(), nil
}

// ----------------------------------------------------------------------
// delete_temp_tool
// ----------------------------------------------------------------------

type DeleteTempToolTool struct{}

func (t *DeleteTempToolTool) Name() string { return "delete_temp_tool" }

func (t *DeleteTempToolTool) Caps() []Capability { return []Capability{CapExecute} }

func (t *DeleteTempToolTool) Desc() string {
	return "Remove a temp tool from this session. Use when a tool you defined is no longer needed or you want to redefine it (delete then create_temp_tool again with the new shape)."
}

func (t *DeleteTempToolTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"name": {Type: "string", Description: "Name of the temp tool to remove."},
	}
}

func (t *DeleteTempToolTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("delete_temp_tool requires a session")
}

func (t *DeleteTempToolTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("delete_temp_tool requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	// Capture the tool's on-disk script filenames BEFORE removing
	// the record. We check all three pools (session / persistent /
	// pending) for the tool by name; the first match wins. The
	// captured names get unlinked after the registry removal so the
	// workspace doesn't accumulate orphan scripts when tools get
	// deleted.
	var scriptFiles []string
	collectScriptFiles := func(tt *TempTool) {
		if tt == nil {
			return
		}
		if tt.CanonicalScriptName != "" {
			scriptFiles = append(scriptFiles, tt.CanonicalScriptName)
		}
		if tt.ScriptName != "" && tt.ScriptName != tt.CanonicalScriptName {
			// Legacy tools may have only ScriptName populated (no
			// canonical) — try unlinking that too.
			scriptFiles = append(scriptFiles, tt.ScriptName)
		}
	}
	// Check the session pool first.
	for _, tt := range sess.TempTools {
		if tt != nil && tt.Name == name {
			collectScriptFiles(tt)
			break
		}
	}
	// Then the persistent pool.
	if len(scriptFiles) == 0 && sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			if p.Tool.Name == name {
				collectScriptFiles(&p.Tool)
				break
			}
		}
	}
	// Pending pool last.
	if len(scriptFiles) == 0 && sess.DB != nil && sess.Username != "" {
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			if p.Tool.Name == name {
				collectScriptFiles(&p.Tool)
				break
			}
		}
	}

	removed := sess.RemoveTempTool(name)

	// Also clear from the per-chat-session pool so the deletion sticks
	// across messages within this same chat. Independent of the
	// persistent (admin-approved) pool — a tool can live in either,
	// both, or neither.
	if sess.DB != nil && sess.ChatSessionID != "" {
		RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
	}

	// Also clear from the persistent pool if applicable. The LLM may
	// be deleting a tool it loaded from persistence at session start,
	// expecting the deletion to stick across sessions.
	persistRemoved := false
	if sess.DB != nil && sess.Username != "" {
		if err := DeletePersistentTempTool(sess.DB, sess.Username, name); err == nil {
			persistRemoved = true
		}
	}

	// Also clear from the pending-approval queue. If the LLM created
	// a tool with persist=true earlier in this session and now wants
	// it gone (typo, supersession by a v2, or a flat-out abandonment
	// of the original idea), the deletion should yank it from the
	// admin's review queue too — otherwise the user sees a tool the
	// LLM no longer wants and approving it just adds dead weight.
	pendingRemoved := false
	if sess.DB != nil && sess.Username != "" {
		if err := RejectPendingTempTool(sess.DB, sess.Username, name); err == nil {
			pendingRemoved = true
		}
	}

	parts := []string{}
	if removed {
		parts = append(parts, "this session")
	}
	if persistRemoved {
		parts = append(parts, "your persistent pool")
	}
	if pendingRemoved {
		parts = append(parts, "the pending-approval queue")
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("no temp tool named %q in this session, your persistent pool, or the pending-approval queue", name)
	}

	// Clean up on-disk script files captured before the registry
	// removal. Best-effort: an unlink failure logs but doesn't fail
	// the delete (the tool's already gone from the catalog; a stale
	// script file is cruft, not a correctness issue). Idempotent
	// for missing files.
	if sess.WorkspaceDir != "" {
		seen := map[string]bool{}
		for _, f := range scriptFiles {
			if seen[f] {
				continue
			}
			seen[f] = true
			abs := filepath.Join(sess.WorkspaceDir, f)
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				Debug("[temptool] script cleanup: failed to unlink %s: %v", abs, err)
			} else if err == nil {
				Debug("[temptool] script cleanup: removed %s for tool %q", abs, name)
			}
		}
	}

	var phrase string
	switch len(parts) {
	case 1:
		phrase = parts[0]
	case 2:
		phrase = parts[0] + " and " + parts[1]
	default:
		phrase = strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
	return fmt.Sprintf("Removed temp tool %q from %s.", name, phrase), nil
}

// ----------------------------------------------------------------------
// Conversion + dispatch
// ----------------------------------------------------------------------

// persistUnapprovedTool gives a freshly authored, UNAPPROVED tool a durable
// home: the agent that asked for it, marked Trial because nobody has vouched
// for it yet. (An approved tool takes the persist=true path into the pending
// pool instead; this is the everything-else case.)
//
// It used to write a per-CHAT-SESSION copy. That scope answered "who can use
// this tool?" with "whichever chat window you were in" — not a boundary anyone
// reasons about. Deleting the conversation deleted the tool, nothing outside
// that conversation could see it existed, and reconciling it against the real
// scopes needed a shadow pass on every read. Committing to the agent keeps the
// property that mattered (an agent always loads its own kit, so the tool is
// callable immediately, before any approval) and drops the rest.
//
// Hosts with no agent of their own — no AgentID, or no attach seam wired — keep
// the session pool as a fallback so they still have somewhere to put it.
func persistUnapprovedTool(sess *ToolSession, tool *TempTool) {
	if sess == nil || sess.DB == nil || tool == nil {
		return
	}
	// Authoring focus wins over the running agent: an authoring turn builds FOR
	// another agent, and committing to the runner would pile every tool Builder
	// ever wrote onto Builder itself.
	target := sess.AgentID
	if sess.AuthoringAgentFn != nil {
		if focus := strings.TrimSpace(sess.AuthoringAgentFn()); focus != "" {
			target = focus
		}
	}
	// Builder does NOT agent-bundle: its tools belong in the user-wide pool
	// (finalizeAuthoredTool runs right after create and pools them, so every one
	// of the user's agents loads the single canonical copy and an edit propagates
	// to all of them). Bundling to Builder's own record here stranded the tool on
	// one agent AND returned before saving the session draft finalizeAuthoredTool
	// needs — so the pool step no-op'd and the tool was stuck agent-bundled. That
	// was invisible to the create-scope unit test, which leaves AgentID empty and
	// so never hit this branch. Fall through to the session-draft save; the pool
	// re-home is finalizeAuthoredTool's job. Non-Builder agents still bundle onto
	// their own record — they can't write the shared pool.
	if target != "" && AttachToolToAgent != nil && !sess.CanScopeGlobal {
		t := *tool
		t.Trial = true
		t.TrialSince = time.Now()
		if err := AttachToolToAgent(sess.DB, sess.Username, target, t); err == nil {
			Debug("[temptool] %q committed to agent %s as a trial tool", tool.Name, target)
			return
		} else {
			Debug("[temptool] attach %q to agent %s failed, falling back to session scope: %v", tool.Name, target, err)
		}
	}
	if sess.ChatSessionID != "" {
		if err := SaveSessionTempTool(sess.DB, sess.ChatSessionID, *tool); err != nil {
			Debug("[temptool] session-scoped save failed for %s/%s: %v", sess.ChatSessionID, tool.Name, err)
		}
	}
}
