# Tool & credential namespacing

**Status:** Design + phase 1 in progress. Related: `project_credential_ownership`,
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

### In scope — credentials (the axis we've built out)

- **"My API Credentials"** — a user CRUDs their OWN credentials (stamped
  `Owner = them`). Reuses the credential form; every op is scoped to
  `Owner == currentUser`; these never appear on the admin page.
- **"Global credential keys"** — for GLOBAL **hybrid** creds
  (`cred_scope=per_user`) the user is granted (`AllowedUsers`), a place to paste
  their own key (`SaveUserSecret`) — the non-OAuth counterpart to "Connected
  accounts".

### The crux — per-user storage keying

The credential store is a single `name → record` namespace (`secureAPITable[name]`),
so two users' `github` collide. **User-owned creds must be keyed by `(owner, name)`**
— a per-user sub-store or key prefix — with owner-aware `Save`/`Load`/`List`.
Global creds keep their bare-name keys. This keyed store is the main plumbing
Phase 3 adds, and it's a prerequisite before any user can safely create one.

### Rides with this: admin filtering

The admin credential list now filters to `Owner == ""` (global only). Safe *now*,
because user-owned creds have a home (the Account surface). This is the phase-2
deferral, unblocked.

### Deferred

- **Tools user-surface + admin tool-filtering.** Tools are already per-owner in
  the admin; giving users their own tool surface (and filtering the admin) is a
  symmetric but separate chunk — do it after the credential surface proves the
  pattern.
- Global-tool opt-in catalog (phase 4); user enumeration / `AllowedUsers` picker /
  user management (phase 5).

### Open questions (phase 3)

- User-owned cred names: globally unique, or per-user (needs the keyed store —
  recommend **per-user**, since a user shouldn't have to avoid others' names).
- Do user-owned creds need any scope? **No** — the owner's agents get them; the
  tier-2 per-agent opt-out is still available.
- Hybrid-key entry: a new section, or fold into "Connected accounts".

## Open questions

- Where users manage their namespace (Account vs a per-user admin-like console).
- User-owned creds/tools default: all of that user's agents, or per-agent opt-in.
- How `AllowedUsers` is edited in the admin UI at scale (a tags field now; a
  user-picker later — needs user enumeration, which doesn't exist yet).
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
