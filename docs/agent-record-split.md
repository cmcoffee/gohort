# AgentRecord split — base vs. user plane

**Status:** Phase 0 shipped. Phases 1–4 planned.

## Problem

There is exactly one runtime agent object — `AgentRecord`
(`apps/orchestrate/types.go`). *Every* agent is one: user-created orchestrate
agents, in-code seeds (Builder, Chat), sub-agents, and **app agents** (declared
by an app via `appagents.RegisterAppAgent`, mapped into an `AgentRecord` by
`appAgentSpecToRecord`).

"App agent" is therefore not a type — it's a set of runtime markers on the
shared struct (`Owner=="system"`, presence in the appagents registry, `Hidden`,
`OwnedBy`). Because the distinction is *inferred*, code that must respect the
app-agent boundary keys on drift-able proxies (`Hidden`, `Owner`) that a stale
per-user shadow can forge. That's how an LLM-authored tool (`ts3_list_clients`)
got bundled onto a visible app agent (Casefile's "Case Analyzer") and stuck.

## Invariant

> An app agent **is** a base `AgentRecord`. Only a `UserAgentRecord` can hold
> LLM-authored tools (`Tools []TempTool`) and user scope decisions. The boundary
> is the **type**, enforced by the compiler — no `Hidden` proxy, no registry
> filter, no resolution hack.

Two tool planes make this precise:

- **Embedded** — `Tools []TempTool`: full LLM-authored definitions, agent-scoped.
  The mixing plane. Moves to `UserAgentRecord`.
- **Referenced** — `AllowedTools []string` etc.: pointers to *shared* tools /
  credentials / pipelines. How an app agent "uses shared resources." Stays on
  the base.

## Field partition

| Base `AgentRecord` (app-safe) | `UserAgentRecord` (embeds `*AgentRecord`) |
|---|---|
| `ID, Owner, Name, Description` | `Tools []TempTool` ← the mixing plane |
| `OrchestratorPrompt` | `DisabledPersistentTools` |
| Behavior: `Cortex, Fleet, Mode, Triggers, RecallHints, TagName` | `EnabledCredentials` (was `DisabledCredentials`) |
| Memory: `MemoryMode, DisableExplicit` | `AttachedPipelines, DisabledPipelines` |
| `AllowedTools []string` (references to shared) | `Rules, Evals` |
| `Hidden, Exposed` | `OwnedBy, InheritParentTools` (sub-agent wiring) |
| Skills/Collections references | `Locked`, per-agent scope decisions |

Open calls: `Rules` and `Think` budget (app-agent shadows carry `Rules` today —
decide user-plane-only vs. a base customization slot).

## Credential model: allow-list, not deny-list

Today `DisabledCredentials` is a **deny-list** (default-allow): a new credential
reaches every agent unless someone remembers to deny it — a silent leak on a
non-action. Tools already use an **allow-list** (`AllowedTools`); credentials,
the more sensitive resource, should too. So the base→user split renames this to
`EnabledCredentials` (default-deny, least privilege).

Migration bakes in each agent's *current* effective allow-set
(`allRegistered − Disabled`) so no live agent loses access on the flip; tightening
is then a deliberate prune. App agents (base) have **no** credential field — they
use only app-declared / spec credentials and the secured-cred locking model.

## Storage & serialization

- Today: one `orchestrate_agents` table, gob-serialized `AgentRecord`.
- kvlite is gob, which **nests** embedded structs (see
  `reference_kvlite_gob_embedded`), so `UserAgentRecord{*AgentRecord, …}` changes
  the on-disk layout. Need a **read-compat shim**: decode the legacy flat struct,
  map into `UserAgentRecord`, gate on a `SchemaVersion`, re-serialize on next save
  (lazy migration — no big-bang sweep).
- App agents resolve from `AppAgentSpec`, never stored, so they skip the
  migration; their shadow carries **base fields only**.

## Resolution (`loadAgent`)

Split the entry point:

- `loadAppAgent(id) (*AgentRecord, bool)` — registry-backed, base only.
- `loadUserAgent(db, id) (*UserAgentRecord, bool)` — stored, migrated.
- `resolveAgent(id)` picks via `AppAgentByID` — the *last* place that lookup lives.

Signature audit is compiler-driven: every current `AgentRecord` consumer declares
its plane. Scope layer, `bundleAgentTool`, dispatch, page rendering →
`*UserAgentRecord`; persona/prompt/behavior readers → `*AgentRecord`.

## Blast radius (ranked)

1. gob layout migration (needs the shim + version gate + tests on real records).
2. Signature churn across scope / dispatch / page layers (compiler-caught, wide).
3. Shadow overlay split (app = base-only, user = full).
4. Credential default flip (behavior change; mitigated by bake-in).
5. Export/import — artifact bundles serialize agents (`export.go`); format changes.

## Phases

- **Phase 0 — SHIPPED.** Interim registry-keyed guards, so the invariant *holds*
  while the type split is built. Keyed on `isAppAgent(id)` (the appagents
  registry — authoritative, not the drift-able `Hidden`/`Owner` proxies):
  - `add_tool`, `bundleAgentTool` (runtime), `bundleAgentToolByID` (scope) refuse
    an app-agent target. Removal (`unbundleAgentToolByID`) stays open so an
    already-stuck tool can be cleaned off.
  - Tool / credential / pipeline scope pills exclude app agents by identity
    (fixes the visible-app-agent leak the `Hidden` proxy allowed).
  - A one-time lint warns when an app agent is registered `Hidden:false`.
- **Phase 1.** Introduce `UserAgentRecord` + read-compat shim + `SchemaVersion`.
  No behavior change; both planes still resolve. Migrate-on-load.
- **Phase 2.** Move field *access* to the correct type across the codebase
  (mechanical, compiler-driven).
- **Phase 3 SHIPPED (done independently of the type split).** Credentials flipped
  from the `DisabledCredentials` deny-list to an `EnabledCredentials` allow-list
  (least privilege — a newly-registered cred is denied by default, no longer
  silently reaching every agent). A `CredAllowlist bool` marker (not nil-vs-empty,
  which gob can't preserve) distinguishes migrated from legacy agents;
  `credentialDenySet` is dual-path (allow-list: deny open creds not listed; legacy:
  the old deny-list). Migration bakes in each agent's current effective access
  (`openCreds − Disabled`) on first scope-touch or at creation, so nobody loses
  access; secured creds are never scope-gated. Un-touched existing agents stay
  legacy until scoped (safe coexistence — no eager sweep, since agents aren't
  enumerable across owners). Tests: `TestCredentialAllowlist`,
  `TestSetCredentialScopeEndToEnd`, `TestCredentialDenyBuilder`.
- **Phase 4.** Delete the now-dead heuristics — including Phase 0's guards, the
  `Hidden`-from-spec refresh, and the scope-pill proxies. The type holds the line.

Each phase is independently shippable and reversible; Phases 1–2 are the real work.
