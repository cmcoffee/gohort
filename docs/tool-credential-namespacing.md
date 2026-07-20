# Tool & credential namespacing

**Status:** Phases 1-3 (credentials axis) shipped. Related: `project_credential_ownership`,
`reference_tenancy_map`, `docs/agent-record-split.md`,
`docs/secured-credential-tool-binding.md`.

The target: tools and API credentials live in **two namespaces** — a global
(system) one and a per-user one — so the deployment scales past 1-2 users without
the admin having to reason about every agent.

## Two namespaces

- **Global / system namespace** — *this is what the admin page becomes.*
  Deployment-wide, admin-managed. Holds **global credentials** and **global
  tools**. Nothing per-user shows here.
- **User namespace** — per-user, user-managed. Holds *most* credentials and
  tools: the user's own keys and their authored tools.

## Credential types (three)

| Type | Config lives | Secret lives | "Who may use it" |
|---|---|---|---|
| **Global / shared** | global (admin) | global — one shared secret | `AllowedUsers` gate (protects the shared secret) |
| **Hybrid** (`cred_scope: per_user`) | global (admin) | per-user — each user's own key (Account page) | `AllowedUsers` gate *(optional admin restriction)* **AND** the user has supplied a key; the call runs **as that user** |
| **User-owned** | user | user | ownership — the user's own agents; no cross-user ACL |

`AllowedUsers` is the single "which users" control for the two types whose config
lives on the admin page (global + hybrid). User-owned credentials are governed
purely by ownership. The hybrid's effective rule is `AllowedUsers ∧ has-key`.

## Tools (two)

- **Global tools** — defined once (admin). Users **opt them into their agents**
  (add/remove). This is the one thing from the global namespace a user pulls into
  their own fleet.
- **User tools** — authored by/for a user; live in the user namespace; available
  to that user's agents.

## Access model — two tiers

The unit that scales is the **user**, refined per-agent by each user for their own
(small) fleet — never a per-agent ACL the admin has to manage across everyone.

- **Tier 1 — `AllowedUsers` on the credential (admin, coarse, scalable).** Empty =
  all users. Non-empty = only those users. This is the primary grant; it lives on
  the credential (global namespace), a short legible list.
- **Tier 2 — per-agent opt-out (user, fine, self-managed).** `AgentRecord.Disabled`
  `Credentials` — a user excludes a specific credential from a specific one of
  *their own* agents ("my casual chatbot can't hit payments"). Naturally
  partitioned per-user, so it never becomes an unmanageable central list.

**Enforcement:** an agent (running in a session for user `U`) may dispatch through
credential `X` iff `X.AllowedUsers` is empty or contains `U` (tier 1) **and** `U`
hasn't excluded `X` on that agent (tier 2). The user is the **session** user
(`sess.Username`), not `agent.Owner` — a system-owned seed agent runs on behalf of
whoever is in the session.

Secured credentials are a separate axis: their access follows the tool-binding
model (`docs/secured-credential-tool-binding.md`), not `AllowedUsers`.

## Admin page (target)

Shows the **global namespace only**: global + hybrid credential *config* (with
`AllowedUsers`) and global tools. A hybrid credential's *secret* is not here — each
user adds their key on their Account page. Per-user credentials/tools do not appear.

## Where we are vs. target

Credentials, agents, and tools already carry an `Owner`, and credentials already
have `cred_scope` (shared vs per-user = the hybrid) plus per-user secret storage
(`SaveUserSecret`). So the substrate is partly there; the work is classifying,
gating by user, and splitting the surfaces.

## Phases

1. **Tier-1 `AllowedUsers` + tier-2 per-agent opt-out (in progress).** The
   credential-access model: `AllowedUsers` on the credential (enforced by the
   session user), the pre-existing `DisabledCredentials` as the per-agent
   refinement. Replaces the per-agent `EnabledCredentials` experiment.
2. **Classify (SHIPPED) + filter (deferred to phase 3).** Credentials gained an
   `Owner` field: empty = global (admin surface, gated by `AllowedUsers`), a
   username = user-owned (that user's namespace only — enforced as `AllowedUsers ==
   [Owner]`). Tools were already owner-namespaced. The admin shows a Global /
   User-owned badge. **Filtering** the admin to global-only is deliberately held
   until phase 3 — hiding user-owned resources before users have a surface to
   manage them would orphan them in the UI.
3. **User namespace surface.** Scoped below.
4. **Global-tool opt-in flow.** A catalog users pick global tools from into their
   agents.
5. **Multi-user hardening.** Per the tenancy map.

## Phase 3 scope — the user namespace surface

**Home:** the existing **Account page** (`apps/account`), already the per-user
authenticated surface (Preferences, Connected accounts, API keys). We add
user-namespace management there rather than a new console. Enforcement is *done*
(phases 1-2): a user-owned credential is owner-only, a hybrid runs as the session
user. Phase 3 is the **surface** + one real plumbing problem.

### In scope — credentials (the axis we've built out) — SHIPPED

- **"My API credentials"** (SHIPPED) — a user CRUDs their OWN credentials
  (stamped `Owner = them`) from a section on the Account page
  (`apps/account`, `api/credentials`). Every op is scoped to
  `Owner == currentUser` via the keyed store (`ListUser` / `LoadUser` /
  `DeleteUser` / `Save` with `Owner` set); these never appear on the admin page.
  Offers the simple key-based types (bearer/header/query/basic_auth/none);
  OAuth2 stays admin-managed.
- **"Global credential keys"** (already present) — for GLOBAL **hybrid** creds
  (`cred_scope=per_user`) the user is granted, the pre-existing "Connected
  accounts" section is exactly this: `PerUserConnectionsFor` + `SaveUserSecret`.

### The crux — per-user storage keying (SHIPPED)

The credential store was a single `name → record` namespace, so two users'
`github` would collide. User-owned creds are now keyed by `(owner, name)` via
`credStoreKey` (`@u:<owner>:<name>`), with owner-aware `Save`/`Load`/`List`.
Global creds keep their bare-name keys; `List()` skips the `@`-prefixed keys.
`Resolve(name, user)` shadows a global with the user's own at dispatch time.

### Rides with this: admin filtering (SHIPPED, for free)

The admin credential list shows global-only with no extra code — it reads
`ListWithPending`, which wraps `List()`, which already excludes the
`@`-prefixed user-owned keys. This was the phase-2 deferral, unblocked.

### Tools — SHIPPED (rides with phase 3)

The tools axis is simpler than credentials: the persistent-tool pool is already
keyed by **username** (`persistent_temp_tools[user]`), so there was never a
collision to solve — the per-user namespace substrate already existed.

- **Auto-catalog** (SHIPPED) — `BuildTools(sess)` now emits the session user's
  OWN credentials as `fetch_url_<name>` tools (deduped so a user cred shadows a
  same-named global), so a credential created on the Account page is immediately
  a first-class callable tool for the owner's agents — not reachable only via a
  temp-tool `fetch_via`. This is the concrete gap the "My API credentials"
  surface would otherwise have left open.
- **"My tools"** (SHIPPED) — a section on the Account page (`api/tools`) lists the
  user's OWN persistent tools (mode + description, a missing-dependency badge
  resolved in the user's namespace, a shared badge) with a break-glass Delete.
  Authoring stays in chat/Builder — a tool is a script or API definition, not a
  hand-filled form — so this surface is view + delete, not create.

**Admin tool-filtering — intentionally NOT done.** Unlike credentials (where
user-owned creds live in a separate `@u:` keyspace that `List()` hides for free),
tool pools are ALL per-user; there is no separate namespace to hide. The admin
persistent-tools page is genuine deployment OVERSIGHT — every user's tools with an
owner badge + break-glass delete + the Shared toggle that defines the global
pool. Filtering it to "global only" would *remove* oversight, not tidy a
namespace, so it stays. "Global tools" here = the `Shared` pool, and the admin
already surfaces that distinction per-row.

### Global-tool opt-in catalog — SHIPPED (phase 4)

`Shared` tools used to auto-load for every user (push). Now they're **opt-in**
(pull): a Shared tool loads for a user's agents only once they adopt it.

- **Adoption store** (`core/temp_tool_persist.go`): `adopted_global_tools[user]`
  = the names a user has pulled in. `LoadAdoptedGlobalTools` / `SetGlobalTool`
  `Adopted` / `MergeAdoptedGlobalTools`.
- **Enforcement**: the two agent-execution tool-load paths (`runner.go`,
  `operator_wake.go`) filter the shared pool to the user's adoption set. The
  other two `LoadSharedPersistentTempTools` call sites (credential-tool scan,
  broken-dep resolvability) stay full-pool — they ask "does this tool exist,"
  not "does this user load it."
- **Catalog surface**: the Gateways page (`api/global-tools`) — GET lists the
  shared catalog with an `adopted` flag; POST `{name, adopt}` toggles it.
- **Migration** (`migrateGlobalToolAdoption`, deploy-wide one-shot marker):
  grandfathers every EXISTING user into the shared tools they saw under the old
  auto-load model, so nothing disappears. Users created after the marker start
  empty (true opt-in). Admin "Share" copy updated: it publishes to the catalog,
  not to everyone's pool.

## The Gateways app

The user-namespace surfaces live in their own app (`apps/gateways`), not scattered
on Account. **Gateways = a user's outward reach**: My API credentials, Connected
accounts (per-user OAuth/MCP), My tools, and the Global-tools catalog. **Account =
identity + preferences**: password, timezone, and inbound personal-access tokens
(the keys an external MCP client uses to reach THIS user's agents — the *inbound*
side, kept on Account deliberately).

The OAuth/MCP consent + callback endpoints stay registered on `/account/…` for
redirect-URI stability (a provider registered `/account/oauth/callback`); the
Gateways "Connected accounts" card calls them by absolute path and the callback
redirects the user back to `/gateways/`.

### Deferred

- User enumeration / `AllowedUsers` picker / user management (phase 5).

### Open questions (phase 3)

- User-owned cred names: globally unique, or per-user (needs the keyed store —
  recommend **per-user**, since a user shouldn't have to avoid others' names).
- Do user-owned creds need any scope? **No** — the owner's agents get them; the
  tier-2 per-agent opt-out is still available.
- Hybrid-key entry: a new section, or fold into "Connected accounts".

## Phase 5 — sharing & governance

Access controls on top of the two planes: generalized `AllowedUsers` picker
(creds/tools/agents), an admin audit/revoke console over the user plane, and a
user-initiated → admin-approved promotion flow. Specified in
`sharing-governance.md`. Note: user enumeration (`AuthListUsers`) **already
exists**, so the open question below about the user-picker is resolved there.

## Open questions

- Where users manage their namespace (Account vs a per-user admin-like console).
- User-owned creds/tools default: all of that user's agents, or per-agent opt-in.
- ~~How `AllowedUsers` is edited at scale~~ — resolved in phase 5
  (`ui.UserPicker` over `AuthListUsers`).
- Classification of today's single-pool resources into global vs user-owned.

## Agent sharing

An agent is shared by **reference**; its credential references (by name) resolve
at RUNTIME in the **recipient's** namespace via `Resolve(name, sessionUser)`. A
shared agent therefore never carries the sharer's secret. Three behaviors by
credential type:

- **Hybrid** (per-user secret): the recipient's own key — the ideal shareable
  vehicle (share the shape, everyone brings their own secret).
- **Global**: works if the recipient is in `AllowedUsers` (or it's open).
- **User-owned**: the recipient can't dispatch through the sharer's private
  secret — they supply their own same-named credential.

**Delegation opt-in.** At share time, per user-owned credential the agent uses,
the sharer chooses "recipient brings their own" (DEFAULT — safe) or "let
recipients use my credential" (explicit). "Use mine" is delegated **use, not
disclosure**: the secret dispatches server-side (the recipient never sees it),
bounded by the credential's own URL allow-list + `RequiresConfirm`; it's
auditable and revoked with the share. Mechanically, the share record carries a
per-credential `delegate` flag and `Resolve`, when running a shared agent, honors
it (resolves to the sharer's credential instead of the recipient's namespace).
This belongs with the sharing UI (`project_cross_user_sharing`); the keyed store
and `Resolve` built in phase 3 are its foundation.
