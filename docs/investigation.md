# The investigation machine

Status: built (v0.6.106). `extras/investigation.machine.json`.

Replaces `extras/troubleshooting.machine.json` (the St4 Design-B sketch) and the standalone
`extras/investigator.agent.json` prompt. Both were built against a guess at the workflow; this one is
built against a description of it.

## The workflow it serves

> Aim it at a set of folders. It has a command. It can query the log bundle, search GitLab and
> Confluence, or investigate a running appliance. It should create a hunch based on the logs, then
> answer it based on evidence. Conversely, I may just have a specific technical question I need
> answered by everything except the log files.

Two things in that are load-bearing and neither is "more tools".

**A hunch and its test are different jobs.** An agent holding all four reaches will form a hypothesis
from the logs and then confirm it *from the same logs*, because that is the cheapest thing in front
of it. It reads as thorough and it is circular. Separating the two into phases is what stops it.

**An observation is not always a log.** Sometimes it is something seen on a live customer system, and
sometimes there is no observation at all — just a question. So logs are one source among several, not
the entry condition.

## Shape

```
triage   (transient, thinks)  Observation to explain, or a question? Routes.
hunch    (transient, thinks)  ONE hypothesis, plus what would confirm it, what
                              would refute it, and WHERE that evidence lives.
verify   (resident)           Go and get that evidence. Supported, refuted, or
                              unsettled. Guarded back to triage.
answer   (resident)           No observation: answer from docs, tickets, the
                              live system. Guarded back to triage.
```

## Why `look_where` is the important field

The first draft of this had `verify` carry no log tools, on the theory that a log-derived hunch must
not be confirmed from logs. That is wrong the moment the observation came from a live box — then the
logs *are* the independent evidence. A static tool restriction cannot express "independent of
wherever this came from".

So `hunch` states it instead. It declares the hypothesis, `confirms_if`, `refutes_if`, and
`look_where` — and `verify` follows that. Positive direction rather than negative restriction, and it
forces the hypothesis phase to answer the harder question: *what would settle this?* That question is
most of the difference between an investigation and a guess.

Being explicit about the limit: this is a **prompt-level discipline, not a gate**. Every tool is in
reach during `verify`. Enforcing it would mean two verify phases routed by source, which doubles the
machine to prevent something the prompt states plainly. Worth doing only if it misbehaves.

## The third outcome

`verify` reports SUPPORTED, REFUTED, or **UNSETTLED**, and the third is the one that needed writing
down. The first two are easy to produce; "the evidence does not settle it, and here is the one thing
that would" is the one an agent rounds into a conclusion unless told not to. A test asserts the
prompt still carries it, because it is the sentence most likely to be edited away as hedging.

A refuted hypothesis is stated as the most useful turn in an investigation, not a failure: it removes
a branch. The prompt says so, because the default behaviour is to soften it into "partially
supported".

## What it needs attached

The machine supplies the progression; the agent supplies the reach. Attach all of it to one agent:

| Reach | From |
|---|---|
| the log folders | a **file store** (`docs/file-stores.md`) attached under Sources |
| the command | a servitor **Local Command** appliance, connected |
| GitLab, Confluence, tickets | MCP servers (`core/mcp_manager.go`), admin-configured |
| a live system | a servitor SSH appliance, connected |

Nothing is per-investigation. A new folder appears in the store and is immediately searchable; a new
question just gets asked.

All four attach in one place: **Configure → Sources** in the chat toolbar. Each row names the tools
that attachment adds to the agent — `search_prod_logs`, `investigate_<system>` — because that is what
attaching actually does, and an attachment whose tools you cannot see is indistinguishable from one
that did not take. If you have ever told an agent to "check the logs in the log folder" and had it
answer that it has no such thing, that list is the fix: ask for the tool by the name on the row.

## Memory

Point the agent's own memory at generalizations only: `memory_mode: "agent"` (the Lessons-learned
directive) and `disable_inferred: true` (Reference Memory off — the layer that would otherwise
compound one incident's findings into the next by similarity).

Per-investigation specifics live in the session and go with it. The machine's own blackboard holds
the triage and the hunch for the length of the conversation and no longer, which is why nothing
needs per-folder memory scoping.

## Open

- **Loop or report on refutation.** `verify` stays put and reports rather than looping back for a
  second hypothesis. You are in the conversation; a silent re-hypothesis arriving with no visible
  seam is worse than being told. `change_phase` remains available when it genuinely needs to.
- **The blackboard overwrites.** Re-entering `hunch` replaces the previous hypothesis rather than
  keeping a history of what has been ruled out (`walk()` assigns `cur.State[ph.Name]`). Across a long
  investigation with several hypotheses, that loses the trail. This is prediction 1 from
  `docs/troubleshooting-machine.md` and this machine is the thing likely to hit it first.

## Loading it

Orchestrate mounts at `/orchestrate`, so its API paths carry that prefix. (Every curl in these docs
had it missing until v0.6.107 — the same relative-vs-absolute mistake that made the Files admin
section 404.)

```sh
# 1. Create it. The response carries the id.
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/orchestrate/api/machines \
  -H 'Content-Type: application/json' -d @extras/investigation.machine.json

# 2. Point the agent at it.
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/orchestrate/api/agents/<agentID> \
  -H 'Content-Type: application/json' -d '{"machine": "<machineID>"}'
```

Or ask Builder: *"load the machine in extras/investigation.machine.json and attach it to <agent>"* —
it has the `machine` tool and `attach_to_agents` does step 2 in the same call.

Or, once it exists, attach it from **Configure → Machines** in the chat toolbar, or the Phase machine
picker on the agent editor.

Then **open a NEW session** — the machine is pinned per session at creation, so an existing
conversation will not pick it up.
