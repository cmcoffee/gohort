package temptool

import (
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// listGrouped reuses the existing ListTempToolsTool logic via a
// shim. We can't call its RunWithSession directly without an instance,
// so reproduce the formatting inline.
func listGrouped(args map[string]any, sess *ToolSession) (string, error) {
	persistentByName := map[string]bool{}
	persistentDescByName := map[string]string{}
	persistentModeByName := map[string]string{}
	pendingByName := map[string]bool{}
	pendingDescByName := map[string]string{}
	pendingModeByName := map[string]string{}
	var orphanedPool []OrphanedTempTool
	if sess.DB != nil && sess.Username != "" {
		orphanedPool = LoadOrphanedTempTools(sess.DB, sess.Username)
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			persistentByName[p.Tool.Name] = true
			persistentDescByName[p.Tool.Name] = p.Tool.Description
			persistentModeByName[p.Tool.Name] = p.Tool.Mode
		}
		for _, p := range LoadPendingTempTools(sess.DB, sess.Username) {
			pendingByName[p.Tool.Name] = true
			pendingDescByName[p.Tool.Name] = p.Tool.Description
			pendingModeByName[p.Tool.Name] = p.Tool.Mode
		}
	}
	// Deployment-wide shared tools live in their OWNER's pool, so none of
	// the maps above contain one you don't own. Without this, such a tool
	// listed as "[session-only]" — the exact opposite of the truth (it is
	// permanent and global), and with no hint that someone else owns it.
	sharedOwners := SharedToolOwners(sess.DB)
	tools := sess.CopyTempTools()
	if len(tools) == 0 && len(pendingByName) == 0 && len(persistentByName) == 0 && len(orphanedPool) == 0 {
		return "No temp tools defined in this session.", nil
	}
	var b strings.Builder
	inSession := make(map[string]bool, len(tools))
	for i, t := range tools {
		inSession[t.Name] = true
		var tag string
		switch {
		// Agent-bundled wins the tag: a tool can be BOTH bundled and in
		// the persistent pool, and "bundled" is the fact that explains
		// why deleting the session copy doesn't stick (the record
		// reloads it each turn). Say so, and say how to remove it.
		case sess.BundledToolNames[t.Name]:
			tag = " [agent-bundled — attached to this agent's record; delete removes it from the record too]"
		case persistentByName[t.Name]:
			tag = " [persistent]"
		case pendingByName[t.Name]:
			tag = " [pending approval]"
		case sharedOwners[t.Name] != "" && sharedOwners[t.Name] != sess.Username:
			tag = " [shared deployment-wide — owned by " + sharedOwners[t.Name] + "; read-only to you, copy under a new name to change it]"
		case sharedOwners[t.Name] == sess.Username:
			tag = " [persistent, shared deployment-wide — you own it; edits affect every user]"
		default:
			tag = " [session-only]"
		}
		fmt.Fprintf(&b, "%d. %s%s [%s] — %s\n", i+1, t.Name, tag, modeLabel(t.Mode), t.Description)
	}
	// Orphan pending: tools queued for approval that aren't currently in
	// sess.TempTools (e.g. requested in a prior session, still waiting).
	// Surface them as a footer so the LLM understands why the catalog
	// doesn't have a tool it remembers requesting. Approved-and-loaded
	// orphans (persistent but not in this session) shouldn't happen in
	// normal flow but list them too for completeness.
	var orphanPending []string
	for name := range pendingByName {
		if !inSession[name] {
			orphanPending = append(orphanPending, name)
		}
	}
	if len(orphanPending) > 0 {
		b.WriteString("\nPending approval (queued but not yet usable in this session — admin must approve):\n")
		for _, name := range orphanPending {
			mode := pendingModeByName[name]
			if mode == "" {
				mode = "shell"
			}
			fmt.Fprintf(&b, "  - %s [%s] — %s\n", name, modeLabel(mode), pendingDescByName[name])
		}
	}
	// Approved-but-not-loaded: tools in the user's persistent pool that
	// aren't in THIS session's executable catalog. The common case is
	// Builder, which deliberately doesn't load user-authored persistent
	// tools ("authors fresh") — so without this footer an APPROVED tool
	// the user is asking about (e.g. "the moltbook toolbox") is invisible
	// to Builder, which then insists the only such tool is a pending
	// draft of a different name. Surface them read-only so the model can
	// SEE (and tool_def get / delete) them, and knows they already exist.
	var orphanPersistent []string
	for name := range persistentByName {
		if !inSession[name] && !pendingByName[name] {
			orphanPersistent = append(orphanPersistent, name)
		}
	}
	sort.Strings(orphanPersistent)
	if len(orphanPersistent) > 0 {
		b.WriteString("\nApproved & in your tool pool, but NOT loaded in this session (exists already — inspect with tool_def get, or load_tool it to call/test it; don't re-author a duplicate):\n")
		for _, name := range orphanPersistent {
			mode := persistentModeByName[name]
			if mode == "" {
				mode = "shell"
			}
			fmt.Fprintf(&b, "  - %s [%s] — %s\n", name, modeLabel(mode), persistentDescByName[name])
		}
	}
	// Orphaned: the last agent carrying the tool was deleted, so the record
	// left every catalog. Listing them is what makes the disappearance
	// legible — otherwise a tool the model used last week is simply absent,
	// and "absent" reads as "I must reach it some other way."
	{
		var orphaned []OrphanedTempTool
		for _, o := range orphanedPool {
			if !persistentByName[o.Tool.Name] && !inSession[o.Tool.Name] {
				orphaned = append(orphaned, o)
			}
		}
		sort.Slice(orphaned, func(i, j int) bool { return orphaned[i].Tool.Name < orphaned[j].Tool.Name })
		if len(orphaned) > 0 {
			b.WriteString("\nORPHANED — definition survives but the tool is NOT callable by anyone (its last carrying agent was deleted). Re-home in Admin › Orphaned Tools, or tool_def get then re-create it. Do NOT try to reach these another way:\n")
			for _, o := range orphaned {
				mode := o.Tool.Mode
				if mode == "" {
					mode = "shell"
				}
				former := o.FormerAgentName
				if former == "" {
					former = "deleted agent"
				}
				fmt.Fprintf(&b, "  - %s [%s] (was on %s) — %s\n", o.Tool.Name, modeLabel(mode), former, o.Tool.Description)
			}
		}
	}
	return b.String(), nil
}
