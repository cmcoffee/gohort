package orchestrate

// People outside gohort (docs/design/outside-audience.md).
//
// An outside person's turn runs as agenthost.OutsideScope: a store of their
// own, apart from the owner's and from every other person's. That is the path
// a shared agent's recipient already takes (memory_scope.go), so their notes,
// findings, history, past sessions and threads are theirs without any layer
// learning a new namespace. What differs is what reaches them from underneath:
//
//   - the agent's saved notes, read-only, so it knows its job (the owner's
//     decision, 2026-11-05), whatever the agent's share switches say;
//   - not the owner's cortex, which is a record of the owner's own activity;
//   - not the owner's findings, which are what the agent worked out across the
//     owner's conversations. Curated knowledge (uploads, attached collections)
//     still reaches them: it is published material, not anyone's private data.
//
// And the agent has to be set to serve them (AgentRecord.OutsideAudience).

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/ui"
)

// outsideAudience reports a turn for a person outside gohort.
func (t *chatTurn) outsideAudience() bool {
	return t != nil && agenthost.IsOutsideScope(t.user)
}

// outsideConfirm is an outside turn's answer to every call that would need the
// owner's go-ahead: no, without queuing it. The kit already leaves out tools
// that ask first; this is for a lent tool whose credential, or the owner's
// "confirm writes", asks on its own.
func outsideConfirm(string, string) bool { return false }

// outsideConfirmRefusal is what the model is told in place of the denial.
func outsideConfirmRefusal(tool string) string {
	return tool + " needs its owner's approval for each use, and nobody is there to give it on this conversation: it is not available here. Say so plainly rather than trying another way."
}

// outsiderNotServedReply is what someone gets who is not the owner, writing
// through a transport that marks its outsiders (a Teams bot), to an agent not
// set to serve people outside gohort. Fixed text: no turn runs.
const outsiderNotServedReply = "This assistant only answers its owner here."

// outsiderNotServed reports a run from someone a transport marks as outside,
// who is not the owner, to an agent that does not serve people outside. Such
// a run used to go ahead as the owner's, in the owner's own memory: every
// colleague who wrote to a Teams bot, and on a bot open to every tenant
// anyone at all, had the agent's notes, findings and history of its owner.
// Now it is refused, and the owner who wants them answered sets the agent to
// serve people outside, where each runs in a store of their own.
func outsiderNotServed(run AgentSyncRun, target AgentRecord) bool {
	return agenthost.IsOutsideScope(strings.TrimSpace(run.OutsideScope)) && !run.SenderIsOwner && !target.OutsideAudience
}

// outsideRunAs is who a run executes as once a transport's proposed outside
// store is weighed: that store, for a sender who is not the owner on an agent
// that serves people outside gohort; otherwise who it already was. A proposal
// that is not an outside store is ignored rather than trusted as a user name.
func outsideRunAs(runtimeUser string, run AgentSyncRun, target AgentRecord) string {
	scope := strings.TrimSpace(run.OutsideScope)
	if scope == "" || run.SenderIsOwner || !target.OutsideAudience || !agenthost.IsOutsideScope(scope) {
		return runtimeUser
	}
	return scope
}

// outsideRunRefusal refuses a run for an outside person on an agent its owner
// has not set to serve people outside, or returns nil.
func outsideRunRefusal(runtimeUser string, target AgentRecord) error {
	if agenthost.IsOutsideScope(runtimeUser) && !target.OutsideAudience {
		return agenthost.ErrNotForOutsideAudience
	}
	return nil
}

// --- Reach (design D) -----------------------------------------------------
//
// An outside turn gets the tools its owner lent the agent for people outside
// (AgentRecord.OutsideTools) and a few that act on nothing but the person's
// own store or arithmetic. Nothing the owner marked ask-first is offered even
// when lent: there is nobody on the other end who can say yes. No delegation
// to the owner's other agents, no custom tools found mid-turn, no Sources:
// what is not listed is not there, rather than refused when reached for.

// outsideAlwaysTools are the tools every outside turn has: the person's own
// memory, and arithmetic and dates.
var outsideAlwaysTools = []string{"remember", "recall", "forget", "calculate", "date_math", "time_in_zone"}

// outsideKit narrows a run's tools to what an outside person may use.
func outsideKit(tools []AgentToolDef, target AgentRecord, asksFirst func(string) bool) []AgentToolDef {
	lent := map[string]bool{}
	for _, n := range outsideAlwaysTools {
		lent[n] = true
	}
	for _, n := range target.OutsideTools {
		lent[strings.TrimSpace(n)] = true
	}
	kept := tools[:0:0]
	for _, td := range tools {
		name := td.Tool.Name
		if !lent[name] || td.NeedsConfirm || (asksFirst != nil && asksFirst(name)) {
			continue
		}
		kept = append(kept, td)
	}
	return kept
}

// outsideCredentialDeny is the credential deny set for an outside turn: every
// credential the deployment or the owner holds, but the ones the owner lent
// the agent for people outside, on top of what was already denied.
func outsideCredentialDeny(deny map[string]bool, owner string, target AgentRecord) map[string]bool {
	lent := map[string]bool{}
	for _, n := range target.OutsideCredentials {
		lent[strings.TrimSpace(n)] = true
	}
	out := map[string]bool{}
	for n, v := range deny {
		out[n] = v
	}
	add := func(cs []SecureCredential) {
		for _, c := range cs {
			if !lent[c.Name] {
				out[c.Name] = true
			}
		}
	}
	add(Secure().List())
	if owner = strings.TrimSpace(owner); owner != "" {
		add(Secure().ListUser(owner))
	}
	return out
}

// --- Limits (design E) ----------------------------------------------------

// The limits an agent serving people outside applies when its owner set none.
// A zero is the default, never "unlimited": one busy or abusive person must
// not be able to spend the owner's budget for everyone.
const (
	outsideDefaultPerMinute = 10
	outsideDefaultPerDay    = 100
	outsideDefaultSpendUSD  = 1.0
)

func outsidePerMinute(a AgentRecord) int {
	if a.OutsidePerMinute > 0 {
		return a.OutsidePerMinute
	}
	return outsideDefaultPerMinute
}

func outsidePerDay(a AgentRecord) int {
	if a.OutsidePerDay > 0 {
		return a.OutsidePerDay
	}
	return outsideDefaultPerDay
}

func outsideSpendUSD(a AgentRecord) float64 {
	if a.OutsideDailySpendUSD > 0 {
		return a.OutsideDailySpendUSD
	}
	return outsideDefaultSpendUSD
}

// The ceiling across ALL of an agent's people outside, an administrator's
// tunable rather than the owner's. The per-person limits above are only as
// good as the person id, and a chat widget often gives each anonymous visitor
// a fresh one: every new id came with its own turns and its own dollar, and
// nothing capped the agent as a whole. On a hosted model that is the
// deployment's money; on a local one it is everyone else's capacity.
const (
	tuneOutsideAllPerDay   = "tune_outside_all_per_day"
	tuneOutsideAllSpendUSD = "tune_outside_all_spend_usd"
)

func init() {
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneOutsideAllPerDay, Category: "People outside gohort",
		Label:  "Turns per 24 hours, all outside people, per agent",
		Help:   "The most turns one agent takes from people outside gohort in a day, all of them together.",
		Detail: "Each person also has the agent's own per-person limits. This is the ceiling over all of them, because a platform can give every visitor a fresh id and each new id starts with its own allowance. Past it, every outside person on that agent is told to try again later, and the owner gets one notice a day.",
		Kind:   KindInt, Default: 2000, Min: 10, Max: 1000000})
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneOutsideAllSpendUSD, Category: "People outside gohort",
		Label:  "Spend per 24 hours, all outside people, per agent (US$)",
		Help:   "The most one agent spends on people outside gohort in a day, all of them together. Counted from recorded usage, so an estimate.",
		Detail: "The ceiling over every outside person on an agent, beside each person's own limit, so fresh ids cannot add up to an open budget. Spend is estimated from recorded usage and the prices under Costs; with no prices set, it counts nothing.",
		Kind:   KindFloat, Decimals: 2, Default: 20, Min: 0.5, Max: 100000})
}

// outsideAllBudget is the spend allowance for all of an agent's people
// outside, together, in the loop's rolling 24 hours.
func outsideAllBudget(a AgentRecord) SharedBudget {
	return SharedBudget{
		Key:           "outside-all|" + a.ID,
		Label:         "everyone outside gohort on this agent",
		DailySpendUSD: TuneFloat(tuneOutsideAllSpendUSD),
	}
}

// outsideTurnsAllTable holds an agent's recent outside turns, all people
// together, in the root store: the people's own stores are theirs, and the
// count has to see across them.
const outsideTurnsAllTable = "outside_turns_all"

// outsideBudget is one person's spend allowance on one agent, in the loop's
// rolling 24 hours beside the agent's own.
func outsideBudget(person string, a AgentRecord) SharedBudget {
	return SharedBudget{
		Key:           "outside|" + person + "|" + a.ID,
		Label:         "this person's",
		DailySpendUSD: outsideSpendUSD(a),
	}
}

// outsideLimitReply is what a person over a limit is told. Fixed text, not
// the model's: a turn that runs to say no would spend what the limit saves.
const outsideLimitReply = "Sorry, you've reached the limit for now. Please try again later."

// outsideTurnsTable holds each person's recent turn times, per agent, in the
// person's own store, so the day's count survives a restart.
const outsideTurnsTable = "outside_turns"

// outsideTurns is one person's recent turns on one agent.
type outsideTurns struct {
	At []time.Time `json:"at"`
}

var outsideTurnsMu sync.Mutex

// outsideAdmit counts a turn for a person, or refuses it when it would take
// them past a limit: turns in the last minute, turns in the last 24 hours, or
// spend in the last 24 hours. A refused turn is not counted. The reason is
// for the owner's notice; the person only ever sees outsideLimitReply.
func outsideAdmit(db Database, person string, a AgentRecord, now time.Time) (ok bool, reason string) {
	if over, spent, limit, _ := OverDailySpend([]SharedBudget{outsideBudget(person, a)}); over {
		return false, fmt.Sprintf("spent $%.2f of $%.2f in 24 hours", spent, limit)
	}
	if over, spent, limit, _ := OverDailySpend([]SharedBudget{outsideAllBudget(a)}); over {
		return false, fmt.Sprintf("everyone outside together spent $%.2f of $%.2f in 24 hours", spent, limit)
	}
	if db == nil {
		return false, "no store for this person"
	}
	outsideTurnsMu.Lock()
	defer outsideTurnsMu.Unlock()
	var rec outsideTurns
	db.Get(outsideTurnsTable, a.ID, &rec)
	dayAgo, minuteAgo := now.Add(-24*time.Hour), now.Add(-time.Minute)
	kept := rec.At[:0:0]
	lastMinute := 0
	for _, t := range rec.At {
		if t.After(dayAgo) {
			kept = append(kept, t)
			if t.After(minuteAgo) {
				lastMinute++
			}
		}
	}
	if lastMinute >= outsidePerMinute(a) {
		return false, fmt.Sprintf("%d turns in a minute", lastMinute)
	}
	if len(kept) >= outsidePerDay(a) {
		return false, fmt.Sprintf("%d turns in 24 hours", len(kept))
	}
	var all outsideTurns
	if RootDB != nil {
		RootDB.Get(outsideTurnsAllTable, a.ID, &all)
	}
	allKept := all.At[:0:0]
	for _, t := range all.At {
		if t.After(dayAgo) {
			allKept = append(allKept, t)
		}
	}
	if max := TuneInt(tuneOutsideAllPerDay); max > 0 && len(allKept) >= max {
		return false, fmt.Sprintf("everyone outside together took %d turns in 24 hours", len(allKept))
	}
	rec.At = append(kept, now)
	db.Set(outsideTurnsTable, a.ID, rec)
	if RootDB != nil {
		all.At = append(allKept, now)
		RootDB.Set(outsideTurnsAllTable, a.ID, all)
	}
	return true, ""
}

// outsideBusyReply is what a person sending a message while their last one is
// still being answered is told. Fixed text, as the limit reply is.
const outsideBusyReply = "One moment: I'm still answering your last message."

// outsideInFlight is the outside people with a turn running, per agent: one
// turn at a time each, so one person cannot hold every slot the owner's
// integrations share and turn everyone else away.
var outsideInFlight = struct {
	sync.Mutex
	on map[string]bool
}{on: map[string]bool{}}

// outsideOneAtATime takes the person's one turn on agent, or reports they
// already have one running. done gives it back, once however often it is
// called.
func outsideOneAtATime(person, agent string) (done func(), ok bool) {
	key := person + "|" + agent
	outsideInFlight.Lock()
	defer outsideInFlight.Unlock()
	if outsideInFlight.on[key] {
		return func() {}, false
	}
	outsideInFlight.on[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			outsideInFlight.Lock()
			delete(outsideInFlight.on, key)
			outsideInFlight.Unlock()
		})
	}, true
}

// noteOutsideLimit tells the owner a person outside reached a limit. One
// notice per agent per day: repeats fold into it with a count (core/notices),
// and the next day's is a new one.
func noteOutsideLimit(owner string, a AgentRecord, person, reason string, now time.Time) {
	if strings.TrimSpace(owner) == "" {
		return
	}
	notices.Record(RootDB, notices.Notice{
		Owner: owner,
		Agent: a.ID,
		Kind:  notices.KindBlocked,
		Title: fmt.Sprintf("People outside gohort reached a limit on %s (%s)", chFirst(a.Name, a.ID), now.In(UserLocation(owner)).Format("2006-01-02")),
		Body:  fmt.Sprintf("Latest: %s, %s. They were told to try again later. The limits are on the agent's Access page, under People outside gohort.", strings.TrimPrefix(person, "outside:"), reason),
	})
}

// --- The owner's choices (Access page) -------------------------------------

// outsideToolOptions are the tools an owner can lend people outside: what the
// agent itself carries (its allowlist, or the default pool when it has none),
// without the ones every outside turn has anyway. A name lent before and no
// longer offered stays listed, so it can be taken back.
func outsideToolOptions(user string, a AgentRecord) []ui.SelectOption {
	always := map[string]bool{}
	for _, n := range outsideAlwaysTools {
		always[n] = true
	}
	carried := map[string]bool{}
	for _, n := range a.AllowedTools {
		carried[strings.TrimSpace(n)] = true
	}
	var out []ui.SelectOption
	for _, o := range availableWorkerToolOptions(user) {
		if always[o.Value] || (len(carried) > 0 && !carried[o.Value]) {
			continue
		}
		out = append(out, o)
	}
	return keepUnofferedValues(out, a.OutsideTools, "No longer offered", "Lent earlier; the agent no longer carries it.")
}

// lendableOutsideTools holds a save to what the picker offers. A name already
// lent stays (it is taken back by unticking it); a name newly added must be
// one outsideToolOptions lists, so a hand-made save cannot lend a tool the
// Access page never offers, such as the framework's own.
func lendableOutsideTools(user string, a AgentRecord, prev []string) []string {
	if len(a.OutsideTools) == 0 {
		return a.OutsideTools
	}
	ok := map[string]bool{}
	for _, n := range prev {
		ok[strings.TrimSpace(n)] = true
	}
	carried := a
	carried.OutsideTools = nil
	for _, o := range outsideToolOptions(user, carried) {
		ok[o.Value] = true
	}
	var out []string
	for _, n := range a.OutsideTools {
		if n = strings.TrimSpace(n); n != "" && ok[n] && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// outsideCredentialOptions are the credentials an owner can lend people
// outside: the deployment's that they may use, and their own.
func outsideCredentialOptions(owner string, a AgentRecord) []ui.SelectOption {
	var out []ui.SelectOption
	seen := map[string]bool{}
	add := func(c SecureCredential, group string) {
		if seen[c.Name] {
			return
		}
		seen[c.Name] = true
		out = append(out, ui.SelectOption{Value: c.Name, Label: c.Name, Group: group})
	}
	for _, c := range Secure().List() {
		if Secure().UserMayUse(c, owner) {
			add(c, "Shared")
		}
	}
	for _, c := range Secure().ListUser(owner) {
		add(c, "Yours")
	}
	return keepUnofferedValues(out, a.OutsideCredentials, "No longer offered", "Lent earlier; no longer available to you.")
}
