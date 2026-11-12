# Agents that split work and propose changes to themselves

Status: agreed 2026-11-11 (decisions below). Stage 1, the decline
record on `propose_rule`, is built (0.9.313); the rest is not. The
changes proposed after review are under "Proposed since", not yet agreed.

Two abilities, one rule between them: an agent may narrow itself on its
own, and may only *ask* to change itself. Nothing in either lets an agent
reach anything its owner has not already given it.

- **Helpers** (single run): the agent splits a task into short-lived
  helpers, each holding part of its own authority. They need no approval,
  because they can do nothing the agent itself could not.
- **Proposals** (lasting): a new sub-agent, an edit to its own
  instructions, a rule. Approved once by the owner. A "no" is kept and
  checked, so it is not asked again.

## What exists, and is reused

| Need | Already there |
|---|---|
| Who a sub-agent belongs to | `AgentRecord.OwnedBy`. Its runs are charged against every ancestor's limits (`familyBudgets`), and approvals flow down the chain (`autonomousApprovedSet`). |
| Depth and loops | Dispatch lineage on the context: no cycles, at most `maxDispatchDepth` (3) hops, and past the first hop nothing beyond the root agent's policy (`handOffLineage`). |
| Parallel work | `dispatch_fanout`: up to `tune_dispatch_fanout_lanes` lanes. |
| Something that waits for the owner | `core.Authorization` in `delegation_authorizations`, shown in Approvals and as in-thread cards. Approve and deny go through `resolveApproval` and `handleApprovalDeny`. |
| An agent proposing something | `propose_rule`: asks in chat when the owner is there, otherwise queues; flags a proposal made after reading outside content or by a non-owner (`proposalFlags`); caps pending at 5. |
| Keeping and undoing an agent's edits | `saveAgentAs(db, rec, reason)` keeps each revision; `rollbackAgent` restores one. |
| Comparing two texts by meaning | `Embed` + `Cosine`, as fact dedup uses them (0.90 = the same). Every instance has an embedder now (the built-in). |
| Seeing what it did | The Activity tab, with sub-agents rolled up under their parent. |

What is missing: an agent that lives for one run, a proposal of anything
but a rule, and any record of a decline (today a "no" is an activity line,
and the model is told only not to ask again *in this conversation*).

## Part 1: helpers

**What the agent does.** One new action on the agents tool,
`agents(action="split")`, with a list of parts (at most 3). Each part names:

- the brief: what this helper is to do and return;
- the tools it may use, chosen from the agent's own tools on this turn.

The helpers run (in parallel up to the fan-out lanes), and their results
come back as one tool result, each part with its own answer.

**What a helper is.** An agent record built in memory for the run and
never saved:

- Owned by the agent that split (`OwnedBy`), hidden, no memory, no Cortex,
  the posture every sub-agent already gets (`enforceSubAgentPosture`).
- Its tools are the ones the part named, and only those that were on the
  splitting agent's own turn after `finalizeDispatchCatalog`. A name not on
  that turn is refused, with the part, before anything runs.
- Never the fleet tools: a helper cannot split, delegate or message
  anyone. That makes helpers one level deep, whatever the depth cap.
- Its credentials are the splitting agent's; its spend and quotas are
  charged to it (`familyBudgets`).
- Ask-first marks and never-unattended decisions bind it as they bind the
  agent (it is a dispatch, and those already carry through one).

**Caps.** At most 3 parts per split, 1 split per run, and 20 helper runs
per agent a day; at the daily limit the split action is withdrawn for the
rest of that day, and the Activity tab says so. A run's worst case is four
model loops. All tunables, deployment-wide: the same for every agent, with
no per-agent override until one agent clearly outgrows them. A
split counts as one dispatch hop, so a helper inside a run that was itself
delegated twice is refused by the depth cap that exists.

**What the owner sees.** Each helper is a run on the Activity tab, under
its agent, with the brief it was given.

**Who may split.** A per-agent setting, "May split work into helpers", off
by default and switched on per agent. Never offered on an outside-audience
run. Off, the action is not in the agent's tools at all, so an agent that
cannot split is not tempted to.

**Gated on parallel lanes.** The setting is offered only where
`tune_dispatch_fanout_lanes` is above 1 (a hosted Worker such as Bedrock).
On one lane, helpers run one after another and buy nothing a single loop
does not, at three times the model calls.

**Done once before.** `tools/orchestrator` held a `delegate` tool: a Lead
orchestrator calling an inline `dispatch_worker`, each worker a fresh loop
on a subset of the session's tools, two layers at most. Its registration
was dropped early because a fourth dispatch route confused the model's
catalog, and it never had lineage, ask-first marks, guardrails, spend
charged to the agent, cancellation with the run, or an Activity row. The
code was deleted in 0.9.314. Helpers, if built, are an action on the
`agents` tool so they inherit all of that, and they come back only when a
run shows the need.

**A helper is its agent.** Every per-agent decision (ask-first marks,
never-unattended, guardrails, tool-result scanning, quotas, spend) is read
for the agent that split, never for the helper's own id. A check keyed on
the helper's id would be a check nothing ever set.

## Part 2: proposals

**What the agent does.** One tool, `propose_change`, offered beside
`propose_rule` (which keeps its name: agents list it in their allowed
tools, and renaming it would need a migration). Kinds:

| Kind | What it carries | Applied on approval by |
|---|---|---|
| `sub_agent` | name, purpose, instructions, tools (a subset of the proposing agent's own) | the same code `create_agent` uses for a dispatched Builder: `OwnedBy` the proposer, `InheritParentTools` |
| `instructions` | a replacement for its own instructions, with the reason | `saveAgentAs(rec, "proposed by the agent")`, so it can be rolled back |
| `setting` | one field from a short allowlist (the fields Agent Assist may change; never tools, credentials, exposure, limits or budgets) | the same |

Every proposal also carries **evidence**: the runs that show why (their
ids). The proposal path checks the ids exist on this agent's family and
shows them on the card; a proposal with none still goes, marked "no runs
cited".

**What it cannot carry.** Anything that widens what the agent family may
reach: a tool or credential it does not hold, an exposure change, a limit.
Those stay with the owner and Builder. A proposal of one is refused, and
the agent is told to ask the owner instead.

**How it reaches the owner.** As `propose_rule` does: an in-chat card when
the owner is in the conversation, otherwise an Authorization
(`Action: "propose_change"`) in Approvals. The card shows the change as a
diff and the cited runs, with the same flags `propose_rule` adds.

**Proposals from runs that read outside content** (a web page, a mail, a
document) are where an injection would try to give an agent more. They
carry the existing flag. On an outside-audience run there is no proposing
at all: the tool is not offered.

**How far a family may grow.** Lasting sub-agents are capped by shape,
not only by approval, since each approval looks reasonable alone:

- **One level.** Only a top-level agent may propose a sub-agent. A
  sub-agent's `propose_change` has no `sub_agent` kind.
- **At most 5 sub-agents per family**, counting the ones the owner made.
- **At the limit it turns off.** A family with 5 sub-agents is not offered
  the `sub_agent` kind at all (its tool description says the family is
  full and the owner can make room), rather than refused proposal by
  proposal. Removing a sub-agent, or the owner raising the cap, turns it
  back on.

Both numbers are tunables.

## Part 3: a "no" that holds

**A decline is a record.** New table `agent_proposal_declines`, in the
owner's store, one row per decline:

- the agent family it binds: the **root** agent (follow `OwnedBy` up), so
  a sub-agent or a helper cannot raise what its parent was told no;
- the kind, the target (which agent, which field), the change itself;
- a fingerprint of the normalized change, and the proposal text's vector
  with its `EmbedVersion`;
- when, the owner's reason if given, and which "no" it was (below);
- reversed: by whom and when, if it was. A reversed decline is kept, marked,
  never deleted.

`propose_rule` declines are written here too.

**Built in stage 1** (0.9.313): the record, the fingerprint and embedding
check (0.90 dropped, 0.75 to 0.90 shown marked), and Not now / No on every
surface a rule proposal is answered from (the in-chat card, the in-thread
card, Requests, and the agent's Security page). The table also keeps a
vector made in an older embedding space and re-embeds it on the next check.
Left for stage 2: Stop suggesting this kind and the pause valve (a mute
needs its way back, the Declined view, built first), and the evidence
check (chat turns write no run record and an agent does not know its run
ids, so there is nothing yet to check cited runs against).

**Three kinds of "no"** on the card:

- **Not now**: holds for 30 days.
- **No**: holds until reversed.
- **Stop suggesting this kind**: no proposals of this kind from this agent
  family until the owner turns them back on.

**The check, before the owner sees anything.** A new proposal is compared
with the family's standing declines:

1. Same fingerprint: the same change.
2. Otherwise, cosine of the proposal text against each decline's vector
   (same `EmbedVersion` only): 0.90 or above is the same change reworded.
3. Its kind is muted for the family.

Between 0.75 and 0.90 is similar, not the same: it reaches the owner,
marked "looks like one you declined on <date>", with that decline beside
it, and the owner decides.

A match is **dropped, not queued**. The agent's tool result says: "The
owner declined this on <date>: <reason>. It can be proposed again only
with runs since then that show it is needed." The drop leaves a
breadcrumb on the turn and a line on the Activity tab.

**Coming back with new evidence.** A matching proposal goes through only
when it cites runs dated after the decline (checked against the run
ledger, not taken from the text). The card then says "you declined this
on <date>; since then:" and the runs.

**A valve.** At most 3 open proposals per agent family (`propose_rule`'s 5
counts in it). Three declines from one family within 7 days pause its
proposals; the agent's page says so and has the switch to resume.

**Declines stay visible.** A Declined view on the agent's Security page,
beside Activity: each record, its reason, and Reverse.

## Order

1. The decline store and the check, wired into `propose_rule` first. Small,
   and the thing that makes every later proposal safe to allow.
2. `propose_change` with `sub_agent`, `instructions`, `setting`, and the
   Declined view.
3. Helpers (`agents(action="split")`). Independent of 1 and 2; could go
   first if splitting matters more than proposals.

## Proposed since (not yet agreed)

- **Stage 2 narrowed.** Instead of a `sub_agent` kind, a sub-agent goes
  through the request that exists for it (`request_build` to Builder), with
  its declines written to and checked against this table. `propose_change`
  carries only `instructions` (and `setting` if a need shows). The
  Declined view, Stop suggesting this kind and the valve come with it.
- **Helpers deferred** until a run shows the need, and then only where
  fan-out lanes are above 1 (Part 1).

No migration: new tool, new action, new table, new setting; `propose_rule`
keeps its name and shape.

## Decided (2026-11-11)

1. **Helpers are off by default**, switched on per agent, never on an
   outside-audience run. Over-splitting (a model reaching for a tool it is
   offered, answering through helpers with no memory) and competing with
   delegating to the specialist built for the work weighed more than spend.
2. **The near-miss band** (0.75 to 0.90) goes to the owner, marked; only
   0.90 and above is dropped.
3. **Proposals after outside content** are flagged, as `propose_rule`'s
   are; on outside-audience runs proposing is not offered.
4. **Names**: `agents(action="split")`, helpers, `propose_change`, and
   Not now / No / Stop suggesting this kind.
5. **Not now** holds for 30 days.
6. **Growth**: sub-agents one level deep, at most 5 per family; at the
   limit the `sub_agent` kind is withdrawn, not refused each time.
   Helpers: 3 parts per split, 1 split per run, 20 helper runs per agent
   a day, the action withdrawn for the day at the limit. Deployment-wide
   tunables, no per-agent override.
