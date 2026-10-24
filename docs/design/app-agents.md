# App agents: an app's own agents, set up where they run

Status: **built** (v0.7.372 to v0.7.385, 2026-10-05). An app agent is an agent
an app registers for itself (`appagents.RegisterAppAgent`): Scribe's Guide
Author and Guide Curator, Publishing's Publisher, Servitor's investigator,
Anvil, Casefile. This records what they are, where they are set up, and
which guardrails reach them, because each of those was settled by a mistake
in the other direction first.

## What an app agent is

It is the app's, not the person's. It runs only inside its app, with tools
the app hands it on each turn (Scribe's section tools, Servitor's SSH
commands), and it is not dispatchable from anywhere else. Every one
registered today is `Hidden`.

Each person still has their own copy, layered over the app's definition
(`agent_overlay.go`): a field they change is theirs, every other field
follows the app, and the prompt and description are the app's and are never
taken from a copy (`frameworkOwnedSeedFields`).

## Where they are set up: in the app, never in orchestrate

Settled after trying both. Shown in orchestrate, an app agent picked up every
surface that hangs off a selected agent there: schedules, channels, cortex,
delegation, intake, sharing. Each of those is a way to run it somewhere it
cannot work. So:

- Orchestrate does not list app agents. A Fleet > App agents page and putting
  them in the agent picker were both tried and removed (v0.7.379, v0.7.381).
- `/orchestrate/agent/<app-agent-id>` says "Set up in <app>" and edits
  nothing (`renderAppAgentEditor`).
- They cannot be deleted. `deleteAgentReporting` refuses them on every path
  (editor, API, Builder's `delete_agent`): that path reverts a seed and drops
  its memory, and the app cannot work without the agent.

### The settings page

`orchestrate.AppChat` with `Settings: true` serves a generic page under the
app's chat prefix (`chat/settings`), linked as "Agent settings" in the chat's
toolbar (`apps/orchestrate/app_agent_settings.go`):

- **Which agents:** the chat's own first, then every app agent registered
  under the same `OwningApp`, then any listed in `AppChat.Agents` that another
  app owns but this one runs (Scribe lists Publishing's Publisher, and the
  section says a change applies wherever it runs). One section each.
- **What it shows:** Budgets (plan steps, worker rounds, gap detection,
  tracked plan) and Reasoning (think mode, budget, effort, the lead toggles),
  built by `budgetReasoningFields`, the same function orchestrate's editor
  uses, so the two cannot drift.
- **What it saves:** `settings/data` takes those fields, for those agents,
  and refuses any other field rather than dropping it quietly
  (`saveAppAgentSettings`). It is per person.
- **Reset to default:** removes the person's copy (`resetAppAgent`). Memory
  and conversations stay: a seed revert drops them because the persona they
  grew under is thrown away, and an app agent's persona never changed.

## AppChat: one wiring for an app's chat

Every app that put an orchestrate agent behind its chat used to wire the
panel by hand and picked its own subset of URLs, and a URL left off is a
control that is silently missing. `AppChat.Panel` fills every chat endpoint
an app leaves empty (edit and retry on a message, rename, rejoining a running
turn after a reload, tool confirmations, question cards, guard notices, run
reconnect) and `ServeAppChat` answers them from orchestrate's own public
handlers. The app keeps its send, which carries its tools. Runs live at
`api/runs/` under the app, because the run stream finds its id by that path
segment.

Scribe is on it. Agents, Customapps, Filestore and Anvil are not yet.

## Guardrails

What reaches an app agent, and why each piece is there:

| Guardrail | How it reaches app agents |
|---|---|
| Deployment Always rules, agent rules, reply guards, loop guards, turn judges, spend caps | Turn level, on orchestrate's runner, which `AppChat`, `RunAgentSync` and the publish runs all use |
| Tool-result fence | By what a tool declares (`CapNetwork` or `FetchesExternal`). Scribe's tools declared nothing until v0.7.374 |
| Tool-result injection scan | Always on for app agents (`scansToolResults`), whatever the stored switch says: the switch lived on orchestrate's Security page, which app agents are not set up on, so it was off for all of them |
| Confirmation and the pre-action check | Per tool. Scribe's `delete_section` asks (no "always allow"); other edits do not, because each is one step from undone in History and asking would stop the curator's unattended work |
| Private mode and Stop | A tool that starts its own run passes the handler's `ctx`. Scribe's `research` used `context.Background()`, so a Private turn's research went online and Stop never reached it |

### Apps that run their own loops: AppLoopGuard

Servitor runs its own loops (`RunAgentLoop` directly), not orchestrate's
runner, so none of the above saw an investigation. `orchestrate.AppLoopGuard`
is those checks for one run of an app agent: `Apply(cfg)` sets the guardrail
hooks and wraps the tools in the tool-result policy (`withToolResultPolicy`,
shared with the runner). One guard per run, so a worker that reads injected
text taints the run the investigator then acts in. Every model call it makes
goes to the worker, so a private investigation stays local.

Servitor applies it to the investigator, probe workers, synthesis, the
after-run consolidation (on its own context), mapping, the workspace session
and the repo audit; `guard_wiring_test.go` fails on an unguarded loop config.
The command-line sysprobe has no web user and is left out.

## Open

- Servitor's command tools declare no capabilities, so their output is not
  fenced or scanned by orchestrate; Servitor's own prompts carry a provenance
  fence. Declaring them `FetchesExternal` would scan every command's output,
  at a cost per command.
- Tools an app hands in skip the agent's allowed-tools list and reach bands
  (`runner.go` around the app-tools append).
- Agents, Customapps, Filestore and Anvil still wire their chats by hand.
