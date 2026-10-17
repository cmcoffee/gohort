# Servitor holds systems; agents investigate; evidence lives in file stores

Status: **stage 1 built** (v0.8.333, evidence on file stores); stages 2 to 4 are plans. Written 2026-10-16.

## Why

Workspaces and evidence bundles were built into Servitor before orchestrate agents could do the
job, so each one rebuilt, inside Servitor, something agents now do in general:

- **A workspace is a coordinator over several systems.** It lists members and their roles, scouts
  them, drills into the right one and joins the answers. An orchestrate agent does that now: since
  v0.8.144 it reaches a Servitor system only through its Sources, since v0.8.147 each system in its
  Sources gets its own `investigate_<system>` tool on every kind of run, and the lead can plan the
  steps (lead plans mode).
- **A bundle is two things: storage and an investigation.** The investigation is what an agent
  does. The storage is still worth having: it is encrypted at rest, every file is indexed (format,
  line count, time span, emitting host, severity histogram), timestamps are parsed, and
  `bundle_timeline` merges files in time order. File stores do the rest of what a bundle's home
  does (a folder, upload, archive expansion, caps, case intake and pulls), and the case intake
  pipeline already keeps log bundles in them.

Both were made appliance types because the appliance record was the cheapest chassis: it gave
sharing, agent access, memory, sessions, linked repos and a place in the picker for free
(`docs/servitor-evidence-bundles.md` says so, and weighs "attachment" as the cheaper option that
gets none of that). Agents and file stores now give all of those things themselves.

## The model

| Thing | Where it lives | What it is |
|---|---|---|
| A live machine | Servitor: SSH system, or Remote (through a peer) | Something to connect to |
| Code | Servitor: Git repo system, also linked from systems | Something to read and search |
| Evidence | A file store folder, ingested | Files with an index, a timeline and a summary |
| The investigation | An orchestrate agent | Joins systems, code and evidence through its Sources |

Retired: local command (v0.8.330, replaced by file store commands), tool-backed service (v0.8.331,
replaced by an agent's own tools and MCP), and, by this plan, workspace and evidence bundle.

## What each one becomes

### Workspace: an agent with the members as Sources

| Workspace has | The agent equivalent |
|---|---|
| Members | Each member system attached as a Source |
| Member roles ("scheduler + primary DB") | Lines in the agent's notes or instructions |
| Member links ("this evidence came off that system") | Same: notes, or the evidence folder named next to the system |
| Scout, then drill | The Sources' search tools first, then `investigate_<system>` on the one that fits |
| `investigate_cluster` (ask every member) | Lead plans mode: one plan step per member |
| Read-only drill of a member | Unchanged: a Source's investigate tool is the system's investigator under its own approval rules |
| Shared with people | The agent is shared, and each member system is shared with them too |

What to check before retiring it: whether the scout (a cheap parallel search over every member
before any drill) is worth a prompt pattern or a small tool, `search_all_sources`, because one
search call per Source is several rounds where the scout was one. Measure on a real question first.

### Evidence bundle: an evidence folder in a file store

`core/bundle` already depends on nothing in Servitor: it opens a bundle by `(owner, id)` and the
appliance id was only doing duty as the id (`core/bundle/bundle.go:5-10`). So:

1. **A store folder can be ingested as evidence.** A switch on the store ("Index folders as
   evidence"). A folder is ingested when it lands (upload, a command's download, a pull) and again
   when it changes. The id is `store:<slug>/<folder>`, owned by the store's admin owner.
2. **The agent gets the evidence tools for that store,** next to its existing list/search/read:
   `summarize_<slug>` and `timeline_<slug>` (search and read stay the store's own, over the files). Like
   `linkedRepoTools`, each takes the folder as an argument, so one store holds many cases.
3. **Encryption at rest is kept for the indexed copy.** The store's folder is the operator's
   plaintext files, as it is now; the index and line slices live in the encrypted bundle store,
   as they do today. Deleting the folder wipes its evidence (the store's sweep and retention
   cover it).
4. **Keeping cases apart becomes the agent's question.** `ToolsOnly` exists because one command
   system over many bundles mixed last week's incident into this week's. With evidence in
   folders, the case is the folder, but an agent's working notes and saved facts are per agent
   (`factsNamespace(agentID)`), not per session, so an agent that works many cases carries each
   one's notes into the next. See Open decisions.

### Git repo, SSH, Remote: unchanged

The `LinkedRepos` pattern (a system names repos, the session gets `linkedRepoTools` with a `repo`
argument) is the model the evidence tools copy. Repos stay Servitor systems: the encrypted clone,
history and the investigation's map of the code are what they are for.

## Stages

Each stage ships on its own, and nothing existing stops working until its replacement is in use.

1. **Evidence on file stores. BUILT, v0.8.333** (`docs/file-stores.md`, Evidence). The switch, the
   ingest on arrival, the `summarize_<slug>` and `timeline_<slug>` tools, wiping on delete. Tests: a folder pulled in is indexed; two folders
   in one store stay separate; deleting a folder wipes its index; the tools refuse a folder that
   is not in the store. Nothing in Servitor changes.
2. **Stop new workspaces and bundles.** Add both to `retiredSystemTypes`
   (`apps/servitor/web_appliances.go`); their tabs show only on existing ones. The message for a
   workspace says to attach the systems to an agent; for a bundle, to upload into a file store
   with evidence on.
3. **Move existing ones, with a button, not a migration.**
   - Workspace: "Make an agent from this workspace" creates an agent with the members as Sources
     and the roles and links in its notes, then shows the agent. The workspace stays until the
     owner deletes it.
   - Bundle: "Move to a file store" picks a store with evidence on, copies the staged files into a
     new folder, and re-ingests. The bundle stays until the owner deletes it.
   A button because both need a person's choice (which agent, which store), and a silent move of
   evidence is the kind that loses a case.
4. **Remove the types** once no records of them remain, or after a release with a notice for
   those that do. What goes (from the current wiring):
   - Workspace: `workspace.go`, `workspace_lead.go`, `workspace_session.go`, the members picker,
     the workspace branches in `agent_provider.go`, `ask_system.go` (`ask_system` is offered only
     for workspace members), `reference_source.go`, `peer_appliance.go`, `chat_page.go`,
     `tool_vocabulary`.
   - Bundle: `bundle_*.go` in Servitor (not `core/bundle`), the bundle fields on `Appliance`,
     `/api/bundle/upload` and `/api/bundle/ingest`, the bundle branches in `probe_session.go`,
     `appliance_prompts.go`, `chat_page.go`, `reference_source.go`, `peer_appliance.go`,
     `web_map.go`, `purgeAppliance`.
   - Tests that pin the old shape: `workspace_test.go`, `tools_only_test.go` (the workspace
     bypass case), `tool_vocabulary_test.go`, `sources_door_test.go` (its workspace record),
     `bundle_test.go`. Replaced by tests on the agent and store paths.
   - Docs: `servitor-workspace-mvp.md`, `servitor-evidence-bundles.md` and
     `servitor-composite-view.md` get a status line pointing here.

## What depends on these today

Checked before writing, so a stage can find what it has to touch:

- **Sessions and memory** are keyed by the appliance id (`servitor_sessions:<id>`,
  `app:servitor:<id>`, the older ssh_* tables). A moved bundle or workspace leaves those behind;
  stage 3 says so on the button, and deleting the old record purges them as today.
- **Scribe and CodeWriter** list every Servitor system as a reference source
  (`reference_source.go`). Evidence in a store reaches them only if file stores become a
  reference source too, which is a question for stage 1.
- **MCP** (`servitor_list_systems`) and **peers** (`peerApplianceDesc`) list bundles and workspaces
  by type. They drop them in stage 4; a peer that still has some reports them as before.
- **`ask_system`** exists only for workspace members. When workspaces go, it can go, leaving one
  asking tool per system.

## Open decisions

- **One agent for many cases, or one per case.** An agent's notes and facts outlive the session,
  so an agent working case after case mixes them, the problem `ToolsOnly` solved for bundles.
  Options: an agent per case (made from a template agent), a per-session notes mode for case
  agents, or telling the agent to keep case detail out of standing notes and checking it does.

- **Evidence owner.** A store is the admin's, and an app or agent reaches it by grant. Should a
  person's own uploaded bundle (today a bundle system they own) go into a store they own? File
  stores have no per-user stores yet; the intake pattern uses an admin store per purpose.
- **The scout.** Keep it as a tool for agents (`search_all_sources`), or rely on the lead to fan
  out. Decide by measuring rounds and time on the same question both ways.
- **What the evidence index holds for very large folders.** Bundles cap at 8 GiB per bundle and
  2 GiB per file. A store folder can be bigger; index the first N GiB and say so, or refuse.
- **Archives the bundle ingest cannot open** (xz, 7z, zstd, encrypted). File store commands can now
  run an extractor; the store could run a mapped extract command before indexing.
