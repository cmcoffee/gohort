package orchestrate

// A sub-agent belongs to its parent: it helps that parent with its work and
// has no way in of its own. A channel bound to it is its top-level parent's
// (channelAgentFor), a schedule made for it wakes that parent, which hands
// the work on (scheduleAgentFor), and it has no Cortex of its own (what would
// reach it is filed in the parent's, see cortexHolder).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// topAgentOf is the top-level agent of id's family: id itself for an agent
// that no other owns. Stops on a cycle or a parent that no longer exists.
func topAgentOf(udb Database, id string) string {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		a, ok := loadAgent(udb, id)
		if !ok || strings.TrimSpace(a.OwnedBy) == "" {
			return id
		}
		if _, parentOK := loadAgent(udb, a.OwnedBy); !parentOK {
			return id
		}
		id = a.OwnedBy
	}
	return id
}

// agentAncestors is id's parents, nearest first, up to its top-level agent:
// empty for a top-level agent. Stops where topAgentOf does, on a cycle or a
// parent that no longer exists. A right granted to any of them is the
// sub-agent's too (a sub-agent is listed in no picker a grant is made in);
// a restriction on any of them binds it as well.
func agentAncestors(db Database, id string) []string {
	if db == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{id: true}
	for {
		a, ok := loadAgent(db, id)
		if !ok {
			return out
		}
		p := strings.TrimSpace(a.OwnedBy)
		if p == "" || seen[p] {
			return out
		}
		if _, ok := loadAgent(db, p); !ok {
			return out
		}
		seen[p] = true
		out = append(out, p)
		id = p
	}
}

// agentStore is an owner's agent store, for the checks that are handed an
// agent but no store.
func agentStore(owner string) Database {
	if orchestrateBaseDB == nil || strings.TrimSpace(owner) == "" {
		return nil
	}
	return UserDB(orchestrateBaseDB, owner)
}

// schedulerFamily is the agents whose schedules an agent's Scheduler shows:
// its top-level parent and every agent under it. [""] (everything) when no
// agent is named.
func schedulerFamily(udb Database, user, agentID string) []string {
	if strings.TrimSpace(agentID) == "" || udb == nil {
		return []string{agentID}
	}
	ids := agentFamilyIDs(udb, user, topAgentOf(udb, agentID))
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// uniqueSchedulerRows drops a row already listed: a link group two family
// members share comes back once per member.
func uniqueSchedulerRows(rows []map[string]any) []map[string]any {
	seen := map[string]bool{}
	out := rows[:0:0]
	for _, m := range rows {
		k := fmt.Sprint(m["_kind"], "\x00", m["_id"], "\x00", m["_uid"], "\x00", m["_group"], "\x00", m["_group_head"], "\x00", m["name"])
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	return out
}

// channelAgentFor is messaging.ChannelAgentFor: a channel bound to a
// sub-agent is bound to its top-level parent, which answers it and can hand
// the work to the sub-agent.
func channelAgentFor(owner, agentID string) string {
	if orchestrateBaseDB == nil {
		return agentID
	}
	return topAgentOf(UserDB(orchestrateBaseDB, owner), agentID)
}

// subAgentHandoffLead opens the line a schedule made for a sub-agent carries
// once it wakes the parent instead. Its presence is also how a schedule says,
// on the Scheduler, which sub-agent its work is for (subAgentHandoffOf).
const subAgentHandoffLead = "This is work for your sub-agent "

// scheduleAgentFor is core.ScheduleAgentFor: a schedule made for a sub-agent
// wakes its top-level parent, with a line at the end of its task telling the
// parent to hand it on. At the end so the task's first line, which names a
// recurring task on the Scheduler, stays the person's own. Idempotent: a
// task that already carries the line is left as it is.
func scheduleAgentFor(owner, agentID, text string) (string, string) {
	if orchestrateBaseDB == nil || strings.TrimSpace(agentID) == "" {
		return agentID, text
	}
	udb := UserDB(orchestrateBaseDB, owner)
	a, ok := loadAgent(udb, agentID)
	if !ok || strings.TrimSpace(a.OwnedBy) == "" {
		return agentID, text
	}
	top := topAgentOf(udb, agentID)
	if top == agentID {
		return agentID, text
	}
	if subAgentHandoffOf(text) != "" {
		return top, text
	}
	return top, withSubAgentHandoff(text, chFirst(a.Name, a.ID))
}

// withSubAgentHandoff is text with the line that hands the work to the
// sub-agent called name, at its end.
func withSubAgentHandoff(text, name string) string {
	line := subAgentHandoffLead + name + ": hand it to " + name + " with agents(action=\"run\") and pass on what it returns."
	if strings.TrimSpace(text) == "" {
		return line
	}
	return strings.TrimRight(text, "\n ") + "\n\n" + line
}

// withoutSubAgentHandoff is text without its handoff line, for the editor,
// which shows the line as its "Who does it" picker instead.
func withoutSubAgentHandoff(text string) string {
	i := strings.LastIndex(text, subAgentHandoffLead)
	if i < 0 {
		return text
	}
	return strings.TrimRight(text[:i], "\n ")
}

// scheduleForOption is one choice in a schedule editor's "Who does it"
// picker. Value "" is the agent itself.
type scheduleForOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// scheduleForOptions is who a schedule on agentID's Scheduler can be for: the
// top-level agent of its family, then each sub-agent under it. Empty when the
// agent has no sub-agents, so the picker is not drawn.
func scheduleForOptions(udb Database, user, agentID string) []scheduleForOption {
	if udb == nil || strings.TrimSpace(agentID) == "" {
		return nil
	}
	top := topAgentOf(udb, agentID)
	ids := agentFamilyIDs(udb, user, top)
	if len(ids) < 2 {
		return nil
	}
	var subs []scheduleForOption
	for id := range ids {
		if id == top {
			continue
		}
		a, ok := loadAgent(udb, id)
		if !ok {
			continue
		}
		// A sub-agent under another sub-agent is named with the one between,
		// so two that share a name under different helpers can be told apart.
		path := []string{chFirst(a.Name, a.ID)}
		for _, p := range agentAncestors(udb, id) {
			if p == top {
				break
			}
			if pa, ok := loadAgent(udb, p); ok {
				path = append([]string{chFirst(pa.Name, pa.ID)}, path...)
			}
		}
		subs = append(subs, scheduleForOption{Value: id, Label: "Sub-agent: " + strings.Join(path, " > ")})
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Label < subs[j].Label })
	label := top
	if a, ok := loadAgent(udb, top); ok {
		label = chFirst(a.Name, a.ID)
	}
	return append([]scheduleForOption{{Value: "", Label: label + " itself"}}, subs...)
}

// familySubAgentNamed is the sub-agent in agentID's family that a handoff
// line names, or "" when none is called that any more.
func familySubAgentNamed(udb Database, user, agentID, name string) string {
	if udb == nil || name == "" || strings.TrimSpace(agentID) == "" {
		return ""
	}
	top := topAgentOf(udb, agentID)
	for id := range agentFamilyIDs(udb, user, top) {
		if id == top {
			continue
		}
		if a, ok := loadAgent(udb, id); ok && chFirst(a.Name, a.ID) == name {
			return id
		}
	}
	return ""
}

// scheduleForName checks that forID is a sub-agent in agentID's family and
// returns the name its handoff line uses.
func scheduleForName(udb Database, user, agentID, forID string) (string, error) {
	if strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("this schedule runs no agent, so it has no sub-agents to hand work to")
	}
	top := topAgentOf(udb, agentID)
	a, ok := loadAgent(udb, forID)
	if !ok || forID == top || !agentFamilyIDs(udb, user, top)[forID] {
		return "", fmt.Errorf("%q is not a sub-agent of this schedule's agent", forID)
	}
	return chFirst(a.Name, a.ID), nil
}

// subAgentHandoffOf is the sub-agent a schedule's task is handed to, or "".
func subAgentHandoffOf(text string) string {
	i := strings.LastIndex(text, subAgentHandoffLead)
	if i < 0 {
		return ""
	}
	name, _, ok := strings.Cut(text[i+len(subAgentHandoffLead):], ":")
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}

// foldSubAgentSchedules points every schedule that wakes a sub-agent at its
// top-level parent: at start-up for schedules made before that was the rule,
// and when an agent is moved under a parent (only, then names that agent).
// Standing agents and monitors fold as they are saved; a recurring task is
// re-armed, kept on its own id and name, and reports to the parent's thread.
func foldSubAgentSchedules(owner, only string) int {
	n := 0
	wanted := func(id string) bool { return id != "" && (only == "" || id == only) }
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if wanted(sa.AgentID) {
			if to, _ := scheduleAgentFor(owner, sa.AgentID, sa.Mission); to != sa.AgentID {
				SaveStandingAgent(RootDB, sa)
				n++
			}
		}
	}
	for _, m := range ListEventMonitors(RootDB, owner) {
		if wanted(m.WakeAgent) {
			if to, _ := scheduleAgentFor(owner, m.WakeAgent, m.WakeBrief); to != m.WakeAgent {
				SaveEventMonitor(RootDB, m)
				n++
			}
		}
	}
	for _, rt := range listAgentRecurringTasks(owner, "") {
		p := rt.Payload
		if !wanted(p.AgentID) || isTaskWake(p.Prompt) {
			continue
		}
		to, text := scheduleAgentFor(owner, p.AgentID, p.Prompt)
		if to == p.AgentID {
			continue
		}
		// Its id and name are kept before the session changes: both fall
		// back on the session and the prompt, and its notes and link group
		// are filed under the id.
		p.UID = recurringTaskUID(p)
		if strings.TrimSpace(p.Name) == "" {
			p.Name = recurringName(p)
		}
		p.AgentID, p.Prompt, p.SessionID = to, text, cortexSessionID(to)
		next, err := computeNextFire(&p, time.Now().In(UserLocation(owner)))
		if err != nil {
			Log("[orchestrate.schedules] %s: could not re-arm %q on its parent: %v", owner, recurringName(p), err)
			continue
		}
		UnscheduleTask(rt.TaskID)
		if _, err := ScheduleTask(OrchestrateScheduledUpdateKind, p, next); err != nil {
			Log("[orchestrate.schedules] %s: could not re-arm %q on its parent: %v", owner, recurringName(p), err)
			continue
		}
		n++
	}
	return n
}

// scheduleHandoffs counts parentID's schedules that hand their work to the
// agent called name.
func scheduleHandoffs(owner, parentID, name string) int {
	if parentID == "" || name == "" {
		return 0
	}
	n := 0
	for _, sa := range ListStandingAgents(RootDB, owner) {
		if sa.AgentID == parentID && subAgentHandoffOf(sa.Mission) == name {
			n++
		}
	}
	for _, m := range ListEventMonitors(RootDB, owner) {
		if m.WakeAgent == parentID && subAgentHandoffOf(m.WakeBrief) == name {
			n++
		}
	}
	for _, rt := range listAgentRecurringTasks(owner, "") {
		if rt.Payload.AgentID == parentID && subAgentHandoffOf(rt.Payload.Prompt) == name {
			n++
		}
	}
	return n
}

// moveSubAgentSchedules is foldSubAgentSchedules for everyone, once at
// start-up.
func moveSubAgentSchedules() {
	moved := 0
	for _, u := range deploymentUsers() {
		moved += foldSubAgentSchedules(u, "")
	}
	if moved > 0 {
		Log("[orchestrate.schedules] %d schedule(s) made for sub-agents now wake their parents", moved)
	}
}

// moveSubAgentChannels moves every channel bound to a sub-agent onto its
// top-level parent, once at start-up, for bindings made before a sub-agent
// could not hold one.
func moveSubAgentChannels() {
	moved := 0
	for _, u := range deploymentUsers() {
		for _, ch := range ListChannels(RootDB, u) {
			if to := channelAgentFor(u, ch.AgentID); ch.AgentID != "" && to != ch.AgentID {
				from := ch.AgentID
				SaveChannel(RootDB, ch) // SaveChannel applies channelAgentFor
				moved++
				Log("[orchestrate.channels] %s: channel %q moved from sub-agent %s to its parent %s", u, chFirst(ch.Name, ch.ID), from, to)
			}
		}
	}
	if moved > 0 {
		Log("[orchestrate.channels] moved %d channel(s) from sub-agents to their parents", moved)
	}
}

// moveSubAgentPosters moves every sub-agent named as a bulletin board's
// poster onto its top-level parent, once at start-up. Grants made before
// rights lived on the parent named the sub-agent itself, which the Posters
// picker (visible agents only) can neither show nor untick, and which left
// the parent unable to post.
func moveSubAgentPosters() {
	moved := 0
	for _, u := range deploymentUsers() {
		udb := agentStore(u)
		for _, b := range listBulletins(udb) {
			var posters []string
			changed := false
			for _, id := range b.Posters {
				if top := topAgentOf(udb, id); top != id {
					Log("[orchestrate.bulletins] %s: board %q poster moved from sub-agent %s to its parent %s", u, b.Name, id, top)
					id, changed = top, true
					moved++
				}
				if !containsString(posters, id) {
					posters = append(posters, id)
				}
			}
			if changed {
				b.Posters = posters
				_ = saveBulletin(udb, b)
			}
		}
	}
	if moved > 0 {
		Log("[orchestrate.bulletins] moved %d board poster(s) from sub-agents to their parents", moved)
	}
}

// familyBudgets is the spend ceilings and action limits of a sub-agent's
// parents, which its runs are held to and charged against as well as its
// own (AgentLoopConfig.SharedBudgets). Without it a parent capped at six
// posts a day could have its sub-agent post six more: each agent's count
// was its own. A parent's allowance is kept under its own id, the key its
// own runs are charged to, so it counts the whole family. Nil for a
// top-level agent, or parents with no caps.
func familyBudgets(a AgentRecord) []SharedBudget {
	if strings.TrimSpace(a.OwnedBy) == "" {
		return nil
	}
	db := agentStore(a.Owner)
	var out []SharedBudget
	for _, id := range agentAncestors(db, a.ID) {
		p, ok := loadAgent(db, id)
		if !ok || (len(p.ActionQuotas) == 0 && p.DailySpendUSD <= 0) {
			continue
		}
		out = append(out, SharedBudget{Key: p.ID, Label: chFirst(p.Name, p.ID) + " and the agents under it",
			ActionQuotas: p.ActionQuotas, DailySpendUSD: p.DailySpendUSD})
	}
	return out
}

// contactHolder is the agent whose decisions about who may be messaged
// apply to agentID: its top-level parent for a sub-agent, which sends
// through that parent's channels and has no messaging settings of its own.
func contactHolder(owner, agentID string) string {
	if db := agentStore(owner); db != nil {
		return topAgentOf(db, agentID)
	}
	return agentID
}

// familyContactBlocked reports whether agentID may not message handle: its
// top-level parent may not, or it is blocked itself (a block recorded on a
// sub-agent before its settings moved to the parent still holds, since a
// sub-agent may be stricter than its parent).
func familyContactBlocked(owner, agentID, handle string) bool {
	if IsContactBlocked(RootDB, owner, agentID, handle) {
		return true
	}
	top := contactHolder(owner, agentID)
	return top != agentID && IsContactBlocked(RootDB, owner, top, handle)
}

// familyContactAllowed reports whether agentID may message handle without
// asking: its top-level parent may, or it was allowed itself before its
// settings moved to the parent. A block is checked first, by the caller.
func familyContactAllowed(owner, agentID, handle string) bool {
	if IsContactPreAuthorized(RootDB, owner, agentID, handle) {
		return true
	}
	top := contactHolder(owner, agentID)
	return top != agentID && IsContactPreAuthorized(RootDB, owner, top, handle)
}

// subAgentChannelRefusal is what binding a channel to a sub-agent says: the
// channel belongs on its parent, named, which can hand the work to it. ""
// for an agent that is not a sub-agent.
func subAgentChannelRefusal(udb Database, agentID string) string {
	top := topAgentOf(udb, agentID)
	if top == agentID {
		return ""
	}
	name := top
	if a, ok := loadAgent(udb, top); ok && strings.TrimSpace(a.Name) != "" {
		name = a.Name
	}
	return "A sub-agent has no channel of its own: bind it to " + name + ", the agent this one belongs to, which can hand the work to it."
}

// udbFor is the owner's agent store.
func udbFor(T *OrchestrateApp, user string) Database {
	if T == nil || T.DB == nil {
		return nil
	}
	return UserDB(T.DB, user)
}

// reportToParent posts a scheduled run's report, made by a sub-agent, to its
// top-level parent's home thread as well: the sub-agent's own thread is its
// working context, and not a thread anybody reads. The card says which
// sub-agent ran it. A no-op for an agent that is not a sub-agent, or for an
// empty report.
func reportToParent(udb Database, agentID string, card ChatMessage) bool {
	if udb == nil || strings.TrimSpace(card.Content) == "" {
		return false
	}
	top := topAgentOf(udb, agentID)
	if top == agentID {
		return false
	}
	if a, ok := loadAgent(udb, agentID); ok && strings.TrimSpace(a.Name) != "" {
		card.ReportFrom = strings.TrimSpace(card.ReportFrom + " (by " + a.Name + ")")
	}
	if card.Created.IsZero() {
		card.Created = time.Now()
	}
	sid := cortexSessionID(top)
	if err := appendToStoredSession(udb, top, sid, ChatSession{ID: sid, AgentID: top}, card); err != nil {
		Log("[orchestrate] report from sub-agent %s to parent %s failed: %v", agentID, top, err)
		return false
	}
	return true
}

// cortexHolder is the agent whose Cortex a note about agentID is filed in,
// and the name the note is filed under. A sub-agent has none of its own
// (enforceSubAgentPosture), so its notes go to its top-level parent's,
// saying which sub-agent they are about: "(to Critic)" for a request made
// of it, "(by Critic)" for anything else.
func cortexHolder(db Database, agentID, from, kind string) (holder, label string) {
	top := topAgentOf(db, agentID)
	if top == agentID {
		return agentID, from
	}
	name := agentID
	if a, ok := loadAgent(db, agentID); ok && strings.TrimSpace(a.Name) != "" {
		name = strings.TrimSpace(a.Name)
	}
	how := "by"
	if kind == cortexKindRequest {
		how = "to"
	}
	return top, strings.TrimSpace(from + " (" + how + " " + name + ")")
}

// familyRequest reports whether a request from callerID to targetID stays
// inside one family, made of a sub-agent: its Cortex note would land in the
// parent's Cortex, where the parent's own conversation already shows it.
func familyRequest(db Database, callerID, targetID string) bool {
	top := topAgentOf(db, targetID)
	return top != targetID && top == topAgentOf(db, callerID)
}

// isSubAgent reports whether id has a top-level parent other than itself.
func isSubAgent(db Database, id string) bool {
	return db != nil && topAgentOf(db, id) != id
}

// scheduleHandoffView is what a schedule editor shows of a task, mission or
// brief that agentID runs: the text without its handoff line, the sub-agent
// that line names ("" for none), and the "Who does it" choices. A line naming
// a sub-agent that is not called that any more stays in the text, where it
// can be read and removed.
func scheduleHandoffView(udb Database, user, agentID, text string) (string, string, []scheduleForOption) {
	opts := scheduleForOptions(udb, user, agentID)
	if sub := familySubAgentNamed(udb, user, agentID, subAgentHandoffOf(text)); sub != "" {
		return withoutSubAgentHandoff(text), sub, opts
	}
	return text, "", opts
}

// scheduleHandoffEdit is the text to store when an editor saves: posted is
// what it sent (nil = unchanged) and pick its "Who does it" choice (nil =
// unchanged, "" = the agent itself). The editor was shown the text without
// its handoff line (scheduleHandoffView), so the line is rebuilt here, for
// the sub-agent it named unless the picker changed it. Nil when there is no
// line to keep and none was picked, so the caller stores posted as it is.
func scheduleHandoffEdit(udb Database, user, agentID, stored string, posted, pick *string) (*string, error) {
	cur := familySubAgentNamed(udb, user, agentID, subAgentHandoffOf(stored))
	if cur == "" && pick == nil {
		return posted, nil
	}
	forID := cur
	if pick != nil {
		forID = strings.TrimSpace(*pick)
	}
	text := stored
	if posted != nil {
		text = *posted
	}
	text = withoutSubAgentHandoff(text)
	if forID != "" && forID != topAgentOf(udb, agentID) {
		name, err := scheduleForName(udb, user, agentID, forID)
		if err != nil {
			return nil, err
		}
		text = withSubAgentHandoff(text, name)
	}
	return &text, nil
}
