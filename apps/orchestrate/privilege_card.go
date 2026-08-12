package orchestrate

import (
	"encoding/json"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
)

// The inline privileges card. When an authoring tool creates or changes an
// agent, the powers that agent now holds — which tools it may call unattended,
// whether it can schedule, author, or be published — were only visible after
// the fact, in the Permissions pane and the agent editor. So the build happened
// in one place and its consequences were reviewed in another, and the common
// path was to never review them at all: the first sign that a tool needed a
// human was an unattended fire refusing it days later.
//
// This puts the same governed records in the conversation that produced them.
// The card is a VIEW, not an authority: every control POSTs to
// /api/console/privileges, which re-checks ownership server-side and writes the
// same AgentRecord fields the pane edits. The model can emit the card; only the
// user can resolve it. Same posture as the credential_setup and agent_approval
// cards (see emitCredentialSetupCard / ToolSession.ApprovalPrompt).

// privilegeGrant is one row on the card: a tool the agent may call, or a
// capability flag it holds.
type privilegeGrant struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	// Policy is what happens on an UNATTENDED run:
	//   auto  — runs without asking (read-only, or vouched by a parent)
	//   allow — consequential, but pre-authorized (in AutoApproveTools)
	//   ask   — consequential and NOT pre-authorized: refused and queued
	// Only allow/ask are editable; auto has nothing to decide.
	Policy string `json:"policy"`
}

// privilegeFlag is one capability toggle (conductor tools, authoring, publish).
type privilegeFlag struct {
	Field string `json:"field"`
	Label string `json:"label"`
	On    bool   `json:"on"`
	// Locked marks a flag the card must not offer to change — a sub-agent's
	// posture is pinned by the framework, and showing a toggle that silently
	// won't take is worse than showing none.
	Locked bool `json:"locked,omitempty"`
}

// privilegeToolRows classifies every tool the agent can reach into card rows.
// bundled carries the temp tools the authoring call just committed, which are
// not yet resolvable through the registry — they're classified from the record
// in hand.
func privilegeToolRows(sess *ToolSession, rec AgentRecord, bundled []TempTool) []privilegeGrant {
	approved := map[string]bool{}
	for _, t := range rec.AutoApproveTools {
		approved[strings.TrimSpace(t)] = true
	}
	// A sub-agent inherits its parent's authority wholesale: the parent vouched
	// for it by building it with this toolset, so its consequential tools do NOT
	// stop and ask on an unattended fire (autonomousGate). Reflect that instead
	// of drawing per-tool controls that would never fire.
	subAgent := strings.TrimSpace(rec.OwnedBy) != ""

	byName := map[string]*TempTool{}
	// The user's stored tools first, then the ones this call just committed on
	// top — an update that rewrote a tool must be judged on what it is NOW.
	// Without the store lookup, editing an existing agent classified its whole
	// kit as "unresolved" (store-scoped tools aren't in the registry), which
	// reads as "everything needs approval" and is simply wrong.
	if ListUserAgentTools != nil && sess != nil {
		stored := ListUserAgentTools(sess.DB, sess.Username)
		for i := range stored {
			byName[stored[i].Name] = &stored[i]
		}
	}
	for i := range bundled {
		byName[bundled[i].Name] = &bundled[i]
	}
	names := map[string]bool{}
	for _, n := range rec.AllowedTools {
		if n = strings.TrimSpace(n); n != "" {
			names[n] = true
		}
	}
	// Tools committed by this very call count even when the allowlist hasn't
	// caught up yet. byName is only a LOOKUP table (it holds the user's whole
	// pool) — listing from it would show tools this agent can't call.
	for i := range bundled {
		if n := strings.TrimSpace(bundled[i].Name); n != "" {
			names[n] = true
		}
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	rows := make([]privilegeGrant, 0, len(ordered))
	for _, name := range ordered {
		detail, consequential := classifyPrivilegeTool(sess, name, byName[name])
		row := privilegeGrant{Name: name, Detail: detail}
		switch {
		case !consequential:
			row.Policy = "auto"
		case subAgent:
			row.Policy = "auto"
			row.Detail = detail + " · via parent"
		case approved[name]:
			row.Policy = "allow"
		default:
			row.Policy = "ask"
		}
		rows = append(rows, row)
	}
	return rows
}

// classifyPrivilegeTool reports a short human detail for a tool and whether it
// is consequential — which the Policy field defines as "would stop for approval
// on an unattended run", so the answer is the gate's, not a second opinion:
// toolAlwaysConfirms is the clause autonomousToolAllowed decides on once the
// sub-agent and pre-authorized bypasses (handled by the caller) are out.
//
// It used to answer with temptool.NeedsConfirm, describing itself as "the same
// predicate autonomousGate honors". That stopped being true when the gate moved
// to the credential's own toggle, and the card kept marking tools "ask" — asking
// the user to grant something the runtime never withholds. NeedsConfirm still has
// a job (it selects the guardrail pre-action check), it just isn't this one.
//
// DETAIL still reports reach — "raw network", the credential's name — because the
// card's other job is disclosure, and a tool that runs freely is exactly the one
// worth being told about.
func classifyPrivilegeTool(sess *ToolSession, name string, tt *TempTool) (string, bool) {
	owner, udb := "", Database(nil)
	if sess != nil {
		owner, udb = sess.Username, sess.DB
	}
	if tt != nil {
		// The credential comes off the record in hand, not a name lookup: a tool
		// this very call committed is not in the session or the store yet, so
		// resolving it by name would find nothing and read as "runs freely".
		switch {
		case strings.TrimSpace(tt.Credential) != "":
			return "credential: " + strings.TrimSpace(tt.Credential), credentialAlwaysConfirms(owner, tt.Credential)
		case tt.RawNetwork:
			return "raw network", false
		case temptool.NeedsConfirm(tt):
			return "consequential", false
		default:
			return "read-only", false
		}
	}
	defs, err := GetAgentToolsWithSession(sess, name)
	if err != nil || len(defs) == 0 {
		// Unresolvable at authoring time (a tool that doesn't exist yet, or one
		// scoped elsewhere). Say so rather than guessing a tier — and treat it
		// as consequential so the card never under-states what it can't see.
		return "unresolved", true
	}
	if defs[0].NeedsConfirm {
		return "needs approval", toolAlwaysConfirms(udb, owner, sess, name)
	}
	return "read-only", false
}

// privilegeFlagRows lists the capability toggles worth showing. The three that
// change what an agent IS are always shown (on or off, so their absence is
// visible); the narrower ones appear only when they're on.
func privilegeFlagRows(rec AgentRecord) []privilegeFlag {
	sub := strings.TrimSpace(rec.OwnedBy) != ""
	flags := []privilegeFlag{
		{Field: "fleet", Label: "Conductor tools (schedule, monitors, delegate)", On: rec.Fleet, Locked: sub},
		{Field: "author", Label: "Authoring tools (build agents, tools, apps)", On: rec.Author, Locked: sub},
		{Field: "exposed", Label: "Published to dashboard", On: rec.Exposed, Locked: sub},
	}
	if rec.MCPExposed {
		flags = append(flags, privilegeFlag{Field: "mcp_exposed", Label: "Reachable over MCP", On: true, Locked: sub})
	}
	if rec.AllowBuilderDispatch {
		flags = append(flags, privilegeFlag{Field: "allow_builder_dispatch", Label: "May hand work to Builder", On: true, Locked: sub})
	}
	return flags
}

// emitPrivilegeCard builds the card for a just-saved agent and hands it to the
// app's PrivilegePrompt. No-op without a live viewer (a wake or scheduled
// authoring run has no one to show it to) — the tool's text result stands on
// its own in that case.
func emitPrivilegeCard(sess *ToolSession, rec AgentRecord, bundled []TempTool) {
	if sess == nil || sess.PrivilegePrompt == nil || rec.ID == "" {
		return
	}
	tools := privilegeToolRows(sess, rec, bundled)
	flags := privilegeFlagRows(rec)
	// A card with nothing consequential and no capability on is noise — the
	// agent got read-only tools and holds no powers. Stay quiet.
	if !privilegeCardWorthShowing(tools, flags) {
		return
	}
	toolsJSON, _ := json.Marshal(tools)
	flagsJSON, _ := json.Marshal(flags)
	data := map[string]string{
		"tools": string(toolsJSON),
		"flags": string(flagsJSON),
	}
	if strings.TrimSpace(rec.OwnedBy) != "" {
		data["sub_agent"] = "1"
	}
	sess.PrivilegePrompt(rec.ID, rec.Name, data)
}

// privilegeCardWorthShowing keeps the card off screen for an agent that gained
// nothing to decide: every tool auto-runs and no capability flag is on.
func privilegeCardWorthShowing(tools []privilegeGrant, flags []privilegeFlag) bool {
	for _, t := range tools {
		if t.Policy != "auto" {
			return true
		}
	}
	for _, f := range flags {
		if f.On {
			return true
		}
	}
	return false
}
