package temptool

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"time"
)

// updateGrouped applies a PARTIAL edit to an existing tool without recreating
// it whole — the fix for the recreate-everything pain (a one-action change
// meant resupplying all N actions, inviting copy-paste errors). It loads the
// record, patches the provided fields (for a toolbox: upserts the given
// actions by name and applies remove_actions), then routes the merged result
// through createGrouped so persistence is identical to create.
func updateGrouped(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil {
		return "", fmt.Errorf("requires a session")
	}
	name := strings.TrimSpace(StringArg(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	// Only the fields this call actually passes — a patch to url_template
	// must not be refused over a description it inherited.
	if err := CheckAuthoredToolText(args); err != nil {
		return "", err
	}
	// Stage breadcrumbs. An update that never returns leaves the UI showing
	// "running" forever with nothing in the log to say WHERE it stopped —
	// resolve, script syntax-check (which shells out), or persist. The gap
	// between the last stage logged here and the next line in the log is the
	// answer. Cheap, and it turns the next hang into a five-second diagnosis
	// instead of a code read.
	t0 := time.Now()
	stage := "resolve"
	Debug("[tool_def] update %q: begin", name)
	defer func() {
		Debug("[tool_def] update %q: returned after %s (last stage: %s)", name, time.Since(t0), stage)
	}()
	_ = stage
	if persistentToolLocked(sess, name) {
		return fmt.Sprintf(lockedToolMsg, name), nil
	}
	existing, ok := loadExistingToolRecord(sess, name)
	if !ok {
		// Not in the user-wide pool, pending queue, this session's drafts, or
		// this agent's own live tools — but it may be bundled to ANOTHER of the
		// user's agents (e.g. Builder repairing a tool that lives on a channel
		// agent's record). Resolve it there and redirect the write-back to that
		// agent so the edit lands IN PLACE, not promoted to the shared pool.
		if FindUserAgentTool != nil {
			if tt, ownerAgent, found := FindUserAgentTool(sess.DB, sess.Username, name); found {
				existing = tt
				ok = true
				sess.BundleAuthoredToolTo = ownerAgent
				defer func() { sess.BundleAuthoredToolTo = "" }()
			}
		}
	}
	if !ok {
		// Before declaring it missing: it may be a DEPLOYMENT-WIDE shared
		// tool. Those are callable by everyone but live in their owner's
		// pool, so the searches above never see one you don't own. Saying
		// "no tool named X" there is actively misleading — the model just
		// called it.
		if _, owner, shared := FindSharedToolWithOwner(sess.DB, name); shared {
			return fmt.Sprintf("Tool %q is a DEPLOYMENT-WIDE SHARED tool owned by %s, so you cannot edit it from here — an edit would change it for every user. Nothing is broken and there is nothing to report. Options: ask %s to make the change; or copy it into your own pool with action=\"create\" under a NEW name (use action=\"get\" to read its current definition first) and edit that. Do NOT re-create it under the SAME name.",
				name, owner, owner), nil
		}
		return "", fmt.Errorf("no tool named %q to update — use action=\"create\" to make a new one, or action=\"list\" to see what exists", name)
	}
	// Scope-preserving write-back (flattened namespace): when the resolved
	// record is AGENT-scoped, pin the write-back to a carrying agent so
	// finalize routes through AttachToolToAgent — which updates the ONE store
	// row in place with its ScopeAgents intact. Without this, a non-Builder
	// session's finalize falls to sess.BundleTool, which would extend the
	// tool's scope to the EDITING agent — an edit must never be a grant.
	// (Builder's AdminPersistTempTool path also preserves scope; the redirect
	// just makes every session take the same explicit route.)
	if sess.BundleAuthoredToolTo == "" {
		if row, found := UserToolByName(sess.DB, sess.Username, name); found && len(row.ScopeAgents) > 0 {
			sess.BundleAuthoredToolTo = row.ScopeAgents[0]
			defer func() { sess.BundleAuthoredToolTo = "" }()
		}
	}
	stage = "merge"
	merged := tempToolToCreateArgs(existing)

	// Secured-credential bindings are AUTO-RESOLVED on the re-run create below:
	// the tool already declares the cred, so the authoring guard just re-approves
	// it (unless an admin revoked it, which the guard respects). No pre-approval
	// or edit re-review needed — access follows the tool's own scope.

	// A toolbox has NO top-level url/body/method/etc — those are per-ACTION.
	// Passing one at the top level used to be silently ignored (the "I set
	// body_template on the toolbox and nothing changed, so I tried again five
	// times" trap). Reject with a redirect that shows the correct shape.
	if existing.Mode == TempToolModeToolbox {
		for _, f := range []string{"url_template", "command_template", "method", "body_template", "response_pipe", "script_body", "script_name", "params", "required"} {
			if _, present := args[f]; present {
				return "", fmt.Errorf("%q is a PER-ACTION field on a toolbox, not a top-level one — setting it at the top level does nothing. Put it INSIDE the action: actions=[{name:\"<action>\", %s:...}]. Example fixing a reply body: actions=[{name:\"reply_to_comment\", body_template:{\"parent_id\": {comment_id}, \"content\": {content}}}] (unspecified fields on that action are preserved)", f, f)
			}
		}
	}

	// Patch top-level scalar fields when provided (present = intent to change).
	// "expand" (toolbox presentation toggle) rides the same present-means-change
	// path — BoolArg in createToolboxGrouped reads whatever value lands here.
	for _, f := range []string{"description", "credential", "url_template", "command_template", "method", "body_template", "content_type", "headers", "response_pipe", "response_extract", "category", "script_body", "script_name", "expand"} {
		if v, present := args[f]; present {
			merged[f] = v
		}
	}
	if v, present := args["params"]; present {
		merged["params"] = v
	}
	if v, present := args["required"]; present {
		merged["required"] = v
	}
	// url_template and command_template are ONE stored field
	// (TempTool.CommandTemplate) under two names, and
	// tempToolToCreateArgs seeds BOTH from it so either spelling works on
	// the way in. That is exactly what made a patch silently revert:
	// passing command_template alone left the STALE url_template sitting
	// beside it, and api-mode create reads url_template — so the update
	// reported success, echoed the new value, and persisted the old one.
	// get and live dispatch then both showed the original template, which
	// reads as a broken write path rather than a merge that preferred the
	// wrong twin.
	//
	// Whichever the caller supplied now wins for both. When both are
	// supplied and differ, the mode decides which is meant.
	reconcileTemplateAliases(merged, existing, args)

	// Toolbox: upsert the given actions by name, then apply remove_actions.
	if existing.Mode == TempToolModeToolbox {
		cur, _ := merged["actions"].([]any)
		byName := map[string]int{}
		for i, a := range cur {
			if am, ok := a.(map[string]any); ok {
				byName[strings.TrimSpace(StringArg(am, "name"))] = i
			}
		}
		if inc, present := args["actions"]; present {
			incList, ok := inc.([]any)
			if !ok {
				return "", fmt.Errorf("actions must be an array of action objects to upsert")
			}
			for _, a := range incList {
				am, ok := a.(map[string]any)
				if !ok {
					return "", fmt.Errorf("each entry in actions must be an object")
				}
				an := strings.TrimSpace(StringArg(am, "name"))
				if an == "" {
					return "", fmt.Errorf("each action to upsert needs a name")
				}
				if idx, found := byName[an]; found {
					// Field-level MERGE, not whole-object replace. The docs
					// promise "just the changed fields"; a wholesale replace
					// silently dropped every field the caller didn't re-supply.
					// That was the "I set body_template but it reverted to the
					// param-name guess" bug: updating an action's params without
					// re-passing body_template lost the explicit body, and the
					// write-action scaffold regenerated the wrong one. Merge so
					// unspecified fields (body_template, response_pipe, method,
					// description, disabled, …) survive.
					if existingAM, ok := cur[idx].(map[string]any); ok {
						for k, v := range am {
							existingAM[k] = v
						}
						// Dropping a param has to drop its required entry too.
						// params is REPLACED by the merge above, required is only
						// replaced if the caller re-sent it — so removing a param
						// while leaving required alone strands a name that no
						// longer exists. Dispatch then demands a param the schema
						// doesn't declare ("requires param phone_number_id" for a
						// tool whose help lists no such param), which is
						// unfixable from the model's side: every subsequent
						// update reproduces it, and the tool reads as haunted.
						// The intent is unambiguous — the param is gone — so
						// prune rather than erroring.
						if _, sentParams := am["params"]; sentParams {
							if _, sentRequired := am["required"]; !sentRequired {
								existingAM["required"] = pruneRequired(existingAM["required"], existingAM["params"])
							}
						}
						cur[idx] = existingAM
					} else {
						cur[idx] = am
					}
				} else {
					cur = append(cur, am) // add new
					byName[an] = len(cur) - 1
				}
			}
		}
		if rem := stringSliceArg(args["remove_actions"]); len(rem) > 0 {
			remSet := map[string]bool{}
			for _, r := range rem {
				remSet[strings.TrimSpace(r)] = true
			}
			kept := make([]any, 0, len(cur))
			for _, a := range cur {
				am, _ := a.(map[string]any)
				if am != nil && remSet[strings.TrimSpace(StringArg(am, "name"))] {
					continue
				}
				kept = append(kept, a)
			}
			cur = kept
		}
		if len(cur) == 0 {
			return "", fmt.Errorf("that would leave the toolbox with no actions — delete the tool instead if you mean to remove it")
		}
		merged["actions"] = cur
	}

	// createGrouped re-runs the full create path, which for a shell tool
	// SHELLS OUT to syntax-check the script (py_compile / bash -n). That
	// subprocess is the most likely place for a long stall — pair this line
	// with the [sandbox] spawn/exit breadcrumbs to tell them apart.
	stage = "create/persist (may syntax-check the script in a subprocess)"
	Debug("[tool_def] update %q: entering create path after %s", name, time.Since(t0))
	res, err := createGrouped(merged, sess)
	if err == nil {
		// An update that reports success while the record is unchanged is
		// the single most expensive failure this path can have: the caller
		// believes the fix landed, re-tests, sees the old behaviour, and
		// goes looking for the bug somewhere else entirely. It cost an
		// investigation most of a session.
		//
		// So the claim is checked rather than assumed, against the same
		// lookup action="get" uses.
		if warn := unlandedUpdateWarning(sess, name, args); warn != "" {
			return "Updated " + name + ", BUT THE CHANGE DID NOT LAND. " + warn + " " + res, nil
		}
	}
	if err != nil {
		return "", err
	}
	return "Updated " + name + " in place. " + res, nil
}

// securedBindingCreds returns the set of SECURED credentials a tool binds — its
// api/toolbox Credential plus every fetch_via:<cred> in its hook_capabilities.
// Used to re-pend (on material edit) or clear (on delete) exactly those bindings.
func securedBindingCreds(tt TempTool) map[string]bool {
	out := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if cr, ok := Secure().Load(name); ok && cr.Secured {
			out[name] = true
		}
	}
	add(tt.Credential)
	for _, c := range tt.HookCapabilities {
		if i := strings.IndexByte(c, ':'); i >= 0 && strings.EqualFold(strings.TrimSpace(c[:i]), "fetch_via") {
			add(c[i+1:])
		}
	}
	return out
}

func deleteGrouped(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(StringArg(args, "name"))
	if persistentToolLocked(sess, name) {
		return fmt.Sprintf(lockedToolMsg, name), nil
	}
	// Forget this tool's secured-credential bindings (approved/pending) on delete —
	// tidy-up under auto-resolve. A revoke tombstone is deliberately KEPT so an
	// admin's deny survives a delete + same-name recreate.
	if rec, ok := loadExistingToolRecord(sess, name); ok {
		for cred := range securedBindingCreds(rec) {
			_ = Secure().ForgetToolBinding(cred, name)
		}
	}
	// Agent-bundled tool: the durable copy lives on the agent RECORD and
	// is reconstituted every turn, so removing only the session/pool copy
	// leaves a "zombie" that keeps firing. Route through the app's
	// unbundle callback to remove it from the record, THEN evict the live
	// session copy so it can't dispatch again this turn either.
	if sess != nil && sess.BundledToolNames[name] {
		if sess.UnbundleTool == nil {
			return "", fmt.Errorf("tool %q is bundled onto this agent's record; this surface can't unbundle it — remove it from the agent in its editor (Tools modal → Remove), or ask Builder to update the agent", name)
		}
		if err := sess.UnbundleTool(name); err != nil {
			return "", fmt.Errorf("unbundle %q from the agent record: %w", name, err)
		}
		sess.RemoveTempTool(name)
		// Also clear any lingering session-draft / pending shadows of the
		// same name so it doesn't reappear from a different pool.
		if sess.DB != nil && sess.ChatSessionID != "" {
			RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
		}
		if sess.DB != nil && sess.Username != "" {
			DequeuePendingTempTool(sess.DB, sess.Username, name)
		}
		return fmt.Sprintf("Unbundled %q from this agent's record and dropped it from the session — it will not reload next turn.", name), nil
	}
	// Bundled to ANOTHER of the user's agents: the durable copy lives on that
	// agent's record. update already resolves + writes back there in place
	// (FindUserAgentTool → BundleAuthoredToolTo); without the same reach here,
	// delete either reported "no temp tool named X" or removed only a session
	// shadow while the record copy reloaded next turn. Mirror the in-place
	// semantics: remove from the owning agent's record, then clear any
	// session/pending shadows so nothing resurrects the name. Gated on the
	// name having NO higher-precedence durable home (pool / pending / session
	// draft) — when a duplicate exists in both the pool and an agent record,
	// delete peels the copy that actually WINS resolution first; a second
	// delete then reaches the record copy.
	if sess != nil && sess.DB != nil && sess.Username != "" && FindUserAgentTool != nil &&
		!toolInUserPools(sess, name) && !toolInSessionDrafts(sess, name) {
		if rec, ownerAgent, found := FindUserAgentTool(sess.DB, sess.Username, name); found {
			if DetachToolFromAgent == nil {
				return "", fmt.Errorf("tool %q lives on agent %s's record; this surface can't remove it — remove it from that agent in its editor (Tools modal → Remove)", name, ownerAgent)
			}
			for cred := range securedBindingCreds(rec) {
				_ = Secure().ForgetToolBinding(cred, name)
			}
			if err := DetachToolFromAgent(sess.DB, sess.Username, ownerAgent, name); err != nil {
				return "", fmt.Errorf("remove %q from agent %s's record: %w", name, ownerAgent, err)
			}
			sess.RemoveTempTool(name)
			if sess.ChatSessionID != "" {
				RemoveSessionTempTool(sess.DB, sess.ChatSessionID, name)
			}
			DequeuePendingTempTool(sess.DB, sess.Username, name)
			Log("[temptool.scope] removed %q from agent %s's record (in-place delete)", name, ownerAgent)
			return fmt.Sprintf("Removed %q from the owning agent's record (it lived on agent %s, not this session) — it will not reload next turn.", name, ownerAgent), nil
		}
	}
	t := &DeleteTempToolTool{}
	res, err := t.RunWithSession(args, sess)
	if err == nil && sess != nil && sess.DB != nil && sess.Username != "" {
		// Dequeue from pending-review pool too. If the LLM cancels a
		// tool it just authored, the admin shouldn't still see it in
		// their review queue — that'd be stale work that won't fire.
		DequeuePendingTempTool(sess.DB, sess.Username, name)
	}
	return res, err
}
