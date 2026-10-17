# Agent Types: an agent is built from a type, and keeps following it

Status: **design, nothing built yet.** Written 2026-10-17. The first stage closes a gap in what
already exists; the rest adds to it.

## Why

Two things asked for the same answer.

- **Servitor's method, without a system.** Servitor maps a machine: a plan first, a walk of the
  steps, findings recorded as facts, documents and a topology graph, gaps reported, and then
  answers from what it gathered before it goes live (`search_<sys>_knowledge`, then
  `investigate_<sys>`). That method is worth having for things that are not machines: a GitLab
  instance reached through its API, a Jira project, a document estate. Making each of those a
  Servitor system is the wrong move (tool-backed services were retired in v0.8.331 for that
  reason); the method has to belong to agents.
- **Kinds of agent that are a procedure, not a preset.** The New Agent wizard offers Assistant
  and Specialist, which are starting settings. A debate agent or a deep research agent is a
  procedure: stages in an order. An agent's machine already replaces its brain
  (`docs/agent-machines.md`), so a kind of agent can be "these settings, and this machine".

An **Agent Type** is that: a named kind of agent whose settings and procedure every agent built
from it follows, while each agent keeps its own permissions, reach and memory.

## What exists today

More than it looks like, in three places that do not yet know they are one thing.

- **The wizard's two axes.** "Assistant or Specialist" (`wizard_kinds`,
  `apps/orchestrate/page_agent_wizard.go`; the agent page's `agentTypeTemplates`) sets Cortex,
  memory mode and recall hints. Separately, "Start from a template" offers archetypes.
- **Archetypes, which already track.** `apps/orchestrate/archetypes/*.md` holds the shapes
  Builder composes from: conversational, investigator, knowledge base, research, scheduled
  watcher. A shape that ships a `record` can be instantiated, and an agent built from it
  **follows** it (`ShapeID`, `OverriddenFields`, `OverlayRev`, `agent_overlay.go`): every field
  the agent has not decided for itself is read from the shape at load, so a fix to the research
  recipe reaches research agents made months ago. Detaching (`detachAgentFromShape`) freezes the
  agent as it reads today. The investigator and the watcher ship no record, because their
  subject varies, so nothing follows them.
- **Machines.** `AgentRecord.Machine` names a machine that replaces the agent's procedure;
  identity stays in its instructions.
- **An agent as a Source.** Attached to another agent, an agent gives `consult_<agent>`
  (`agent_reference_source.go`): a question, answered by running it.
- **Scoped instances.** `RunScopedAgent` runs one agent record with state kept per scope, which
  is how every Servitor system shares the hidden investigator template. The layering decided for
  it (2026-06-30): settings from the template, live; facts, graph and sessions per instance;
  rules and knowledge layered.

So following, overriding and detaching are built. What is missing is a type that carries a
machine, a single list of types, a boundary on what may be followed, and types made by anyone
other than the framework.

## The model

A type is an archetype, extended. It carries:

| Part | What it is | Today |
|---|---|---|
| Name and purpose | "Librarian: catalogues one subject and is the one asked first" | `summary`, `template.label` |
| Settings | The starting record: memory mode, Cortex, thinking, caps | `record` |
| Procedure | A machine, or none | new: the record's `machine`, which a type can now ship |
| Needs | What it cannot work without: "a way to reach the subject", "a file store" | new; declared, never granted |
| Questions | What the wizard asks for this type: what to map; a topic and its sides | `asks` |
| Origin | Built in, registered by an app, or made by a person | new |

The types this gives:

| Type | Procedure | Status |
|---|---|---|
| Assistant | none | the wizard's preset today, becomes a type unchanged |
| Specialist | none | the same |
| Librarian | the Map machine | first new type; see below |
| Debate | its stages, as a machine | later; see Stages |
| Deep research | its stages, as a machine | later |
| A person's own | whatever Builder authored | later |

## In the type, layered, or the agent's own

The rule: **a type decides how an agent works, and may make it stricter. It never decides what
the agent can reach or who it answers to, and never holds what the agent remembers.**

**In the type: how it works.** Followed until the agent's owner changes the setting.

- Procedure: `orchestrator_prompt` (the base instructions), `machine`, `plan_guidance`,
  `work_plan`, `gap_check`, `pre_mortem`, `lead_plans`, `lead_review`, `consult_lead`,
  `intake_form`.
- Sizing: `think`, `think_budget`, `effort`, `max_plan_steps`, `max_worker_rounds`,
  `context_depth`.
- Memory framing: `memory_mode`, `session_recall`, `recall_hints`, `enable_notes`,
  `seed_notes`, `channel` (Cortex), `disable_compaction`, `disable_explicit`, `disable_inferred`.

**Layered: the type's, plus the agent's own.** Each can only add, so neither side widens anything.

- `rules`, and the type's base knowledge.
- Restrictions: `guardrails` and their fail-closed setting, `no_unattended_tools`,
  `scan_tool_results` and its tightening, `force_private`. A type may make its agents stricter;
  the owner may add more and cannot remove what the type set.

**The agent's own: never read from the type.**

- Permissions: `allowed_tools`, `auto_approve_tools`, `attached_sources`,
  `attached_collections`, `attached_pipelines`, `allowed_skills`, `disabled_credentials`,
  `fleet`, `allow_builder_dispatch`, `dispatch_mode`, `allowed_dispatch_targets`, `inbound_mode`,
  `allowed_callers`, `authorized_identities`, `workspace_network`, `allow_private_mode`, `tools`.
- Exposure: `allowed_users`, every `share_*` setting, `exposed`, `everyone`, `public_name`,
  `mcp_exposed`, `show_on_dashboard`, `hidden`.
- Spend: `action_quotas`, `daily_spend_usd`, `run_priority`, `lead_model` (choosing a hosted
  model is a cost and a privacy decision).
- Identity and place: `name`, `owner`, `owned_by`, `owning_app`.
- State, which is never on the record at all: memory, the map, sessions, Cortex, facts and
  graph, schedules, channels, bulletins.

A type lists what it **needs**; the owner decides what fills it. A Librarian needs a way to reach
its subject, and the owner gives it a GitLab credential or an MCP server.

## Following, overriding, detaching

It runs one way. Nothing edited on an agent changes its type or any other agent of that type.

- A setting outside the type was never the type's, so editing it touches nothing.
- A layered setting: the owner's addition sits on top, and the type's own entries and its later
  additions still apply.
- A setting inside the type: the owner's value wins for that setting on that agent, and from then
  on that one setting stops following. Everything untouched keeps following, so an agent can
  follow the type's machine while keeping its own instructions. This is `OverriddenFields`
  today.

Two additions, so an override is never silent:

1. **The agent page marks each type setting** "from the type" or "yours".
2. **"Use the type's again", per setting**, removes it from `OverriddenFields`. Nothing does this
   today; the only way back is a new agent.

**Customize** (today's detach) stops following entirely and keeps the agent as it reads now.

A type's procedure must not depend on the wording of its own instructions, because an agent may
replace them. The machine carries procedure and the instructions carry identity, which keeps that
true by construction; a type whose machine needs something said in the instructions is refused
when it is saved.

## Who changes a type

| Origin | Who changes it |
|---|---|
| Built in, or registered by an app | Nobody from the page: it changes when the code ships. An admin can hide it, as an app can be switched off. |
| A person's own | Its owner. It reaches only their agents and the people they shared it with, the same sharing rungs as any agent (own, named, deployment). |
| Offered to everyone | Publishing needs an admin's approval, through the app publish gate (`core/promotion`). Every later change is approved again before it reaches other people's agents; until then they stay on the approved version. |

Because a type never grants permissions, the worst a bad change to a shared type can do is change
how its agents work, never what they can reach. A follower who wants to stay as they are uses
Customize.

## Where types live

One home per job: types are made in Extensions, used in Agents, approved in Admin.

| Where | What happens there |
|---|---|
| **Extensions › Types** | A person's own types, those shared with them, and those published by the deployment, in the layout Skills already uses ("Your skills", "Published by your deployment", a New form). Make one, edit it, share it, ask to publish it, and see which of your agents follow it. Built-in and app types are listed read-only. It sits beside Machines and Pipelines, which Extensions already keeps, because a type is mostly settings and a machine. |
| **Agents** | Where a type is used. The New Agent wizard asks for a type first, as its one question, in place of today's two axes. Each agent's page shows its type, marks each type setting "from the type" or "yours", and offers "Use the type's again" and Customize. |
| **Admin › Governance** | "Pending promotions" approves a type published to everyone, as it approves tools and apps today. A request to publish a type's next version is reviewed as a tool's Update is: Review shows what changes, and approving it is what moves followers on. An admin can also hide a built-in or app type, as an app can be switched off. |

## Librarian, the first type with a machine

The Librarian is the investigator archetype with a subject, a procedure, and a way for other agents
to read what it found.

- **Its subject is the agent's own** (a setting on the instance, like its name), so one type
  serves every subject. This is what lets the investigator shape ship a record at all.
- **The Map run** is its machine: plan the walk (discovery first, then depth), walk the steps
  with the agent's own tools, record each finding, report what it could not reach. Servitor's
  plan tools are already core (`core/plan.go`); its investigator guidance needs a version that
  says "map this subject with these tools" instead of SSH. A schedule re-runs it to keep the map
  fresh.
- **Its memory is the map:** findings by topic, facts, and graph links (repo to group, service to
  repo). A `map` memory framing names it as one ("your map of ...") where today's modes say
  lessons, notes or shortcuts.
- **Attached to another agent**, it gives `search_<librarian>_knowledge` and `get_<librarian>_facts`, which
  read the recorded map at once with no live calls and say to look there first, as Servitor's do;
  and `consult_<librarian>`, which answers from the map and goes live with its tools only when the map
  lacks it, recording what it finds. The map grows from use.
- **Sub-agent by default.** A sub-agent is private to its parent, always callable by it, and runs
  within its limits, so "my agent has a GitLab map" needs nothing new. When a second agent needs
  the same map, the Librarian is promoted to a top-level agent and attached under Sources.
- **Private.** Servitor's investigator never reaches a hosted model unless every model is
  declared private. A map can hold material as sensitive; the Librarian honours the agent's Private
  switch and its type sets `force_private` as a layered restriction, which an owner lifts only
  by Customize.

Example: an agent that writes code against an organisation's repositories gets a Librarian sub-agent
with a GitLab credential. Its map says which group holds which service, where shared libraries
live, and who owns what; the parent looks there before it searches any code, and attaches the
repositories that matter as Servitor Git repo systems for the code itself.

## Stages

Each ships on its own.

1. **Permissions are never followed.** Today a tracking agent reads every field it has not
   overridden from its shape, permissions included. Harmless while shapes ship only with gohort,
   and the hole once anyone can author one. Add the agent's-own list above to the fields the
   overlay never reads from the shape (beside `identityField`). Behaviour change, no data
   rewritten: an existing tracking agent keeps the permission values it was created with, and a
   later change to a shipped shape's tools no longer reaches it.
2. **One list of types.** Assistant and Specialist become types with the settings they set today, listed in Extensions › Types;
   the wizard's two axes become one question; the agent page shows "from the type" and "yours"
   and offers "Use the type's again".
3. **A type can carry a machine; the Librarian.** The instance subject, the Map machine, the `map`
   memory framing, the read-first Source tools, consults that record, the refresh schedule.
4. **Apps register types.** A Go registration beside `appagents.RegisterAppAgent`, so an app
   ships a type as it ships an app agent.
5. **People make types.** Builder authors a type as data (a record and a machine), shared on the
   usual rungs, published to everyone through the admin gate with versions.
6. **Debate and Deep research as types**, once the pipeline gaps are closed. The 2026-10-07 review
   put debate at about 80% expressible as data and research at about 50%. Missing: the run queue,
   restore and notify for pipeline runs; typed cards per stage; follow-up runs over a finished
   run; cited sources and claim checking as a stage.

## Migration

None. An agent with no `ShapeID` reads as having no type ("custom"), Assistant and Specialist keep
their settings, and nothing stored is rewritten. Stage 1 changes what an existing tracking agent
follows, and says so in its release notes.

## Open decisions

- **The `map` memory framing:** a new memory mode, or the investigator's shortcuts mode reworded.
- **Reading a shared Librarian's map:** someone who may use the agent can already learn the map by
  asking it, which argues for letting them read it directly. Confirm against the sharing rungs.
- **Version pinning:** what a follower sees when a newer approved version of a published type is
  waiting, and whether they can stay on the old one.
- **One type per agent.** Proposed yes; combinations come from sub-agents (an agent with a Librarian
  sub-agent), not from stacking types.
- **Servitor's systems as Librarians**, later: an SSH system as a Librarian whose tools are SSH. That
  continues `docs/servitor-systems-model.md` and is much larger; prove the Librarian on an API first.
