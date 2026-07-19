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
2. **Classify + filter.** Mark credentials/tools global vs user-owned (Owner-based);
   the admin page filters to the global namespace.
3. **User namespace surface.** Where a user manages their own credentials + tools
   (Account page or a per-user console).
4. **Global-tool opt-in flow.** A catalog users pick global tools from into their
   agents.
5. **Multi-user hardening.** Per the tenancy map.

## Open questions

- Where users manage their namespace (Account vs a per-user admin-like console).
- User-owned creds/tools default: all of that user's agents, or per-agent opt-in.
- How `AllowedUsers` is edited in the admin UI at scale (a tags field now; a
  user-picker later — needs user enumeration, which doesn't exist yet).
- Classification of today's single-pool resources into global vs user-owned.
