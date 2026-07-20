# Sharing & Governance — namespacing phase 5

Continues `tool-credential-namespacing.md`. Phases 1–4 built the two ownership
planes (global vs user-owned) and the Gateways surface. This batch builds the
**access controls** that sit on top of them: who a shared resource reaches, how
the admin governs the deployment without owning every resource, and how a
user-owned resource is promoted into the global catalog.

## The model this batch implements

Every shareable resource — credential, tool, agent — lives in one of two planes:

- **Global plane** (`Owner == ""`). The deployment-wide catalog. Admin-owned.
  Access = **which users** may adopt it. Per-*agent* wiring is a user-plane
  decision made *after* adoption, not an admin lever.
- **User plane** (`Owner == <username>`). The identity's own resource. The owner
  shares it; the admin governs but does not originate the share.

The admin surface therefore stops being a *provisioning* console and becomes a
**governance** console. Three deliverables give it that role, plus one user-plane
deliverable (peer sharing) that the governance console oversees.

Authority direction, fixed by this batch:

> **user creates → user shares peer-to-peer freely → user requests global →
> admin promotes.**

The admin never *originates* a user's share. They govern the global tier and the
promotion boundary between planes. Peer (1:1 / named-set) sharing needs **no**
admin approval; only **global promotion** is gated. (A stricter deployment could
gate all cross-user sharing — see Open decisions.)

---

## Deliverable 1 — Generalized `AllowedUsers` picker (ACL editor)

Today `AllowedUsers` exists only on `SecureCredential`, and it's edited as a
free-text tags field. Generalize it into one ACL concept across all three
resource kinds, edited with one primitive.

### Data model

| Resource | Field today | Change |
|---|---|---|
| `SecureCredential` | `AllowedUsers []string` | keep; empty = open to all *permitted* users |
| `PersistentTempTool` | `Shared bool` only | **add** `AllowedUsers []string` — gates who may *adopt* a Shared tool |
| `AgentRecord` | none | **add** `AllowedUsers []string` — the recipients of a peer share |

Semantics are uniform: **empty `AllowedUsers` on a global/shared resource = open
to everyone; a non-empty list = only those users.** On a *user-owned* resource,
`AllowedUsers` is the peer-share recipient set (empty = private to the owner).

### Editor primitive — reuse `ChipPicker`, not a new component (SHIPPED helper)

`core/ui` already has the right generic primitive: **`ChipPicker` in "attach"
mode** is a dynamic multi-select over `OptionsSource` that saves the selection as
a `[]string` (`AttachedField` for the current set, `PostTo` + `SaveKey` to save).
Adding a `UserPicker` would be redundant surface area — exactly what the mission
warns against. So there is **no new component and no new JS**; instead a thin
constructor standardizes the ACL configuration:

```go
// core/ui/components.go — ui.ACLPicker(cfg) ChipPicker
ui.ACLPicker(ui.ACLPickerConfig{
    OptionsSource: "api/user-candidates", // GET → {items:[{value,label,desc?}], allowed_users:[...]}
    PostTo:        "api/allowed-users",    // POST {allowed_users:[...]}
    Noun:          "user",
})
```

- `OptionsSource` is an app endpoint mapping `AuthListUsers` to
  `{value: username, label: "Name <email>"}`, and carrying the current selection
  under `allowed_users`. Admins get all users; a user sharing their own agent gets
  all users too (peer sharing is open).
- Defaults: `Attached`/`SaveKey` = `allowed_users`, stored key `value`, label key
  `label`. Override per surface as needed.

Reused verbatim by Deliverable 1, Deliverable 3, and peer sharing.

### Where it appears

- **Admin API Credentials** (global creds): ACL editor in the existing edit
  Expand — replaces the tags field.
- **Admin Global Tools**: ACL editor on each Shared tool.
- **Gateways** (peer sharing): ACL editor on the user's own agents/creds — see
  peer-sharing section.

### Enforcement (mostly already wired for creds)

- Creds: `BuildTools` / `Resolve` already honor `AllowedUsers`. No change.
- Tools: the global-tool catalog (`handleGlobalTools` GET) must **hide** a Shared
  tool from a user not in its `AllowedUsers`, and `SetGlobalToolAdopted` must
  **refuse** adoption of a tool the user isn't permitted. New gate:
  `func canAdoptGlobalTool(db, user, name) bool`.
- Agents: peer-share resolution reads `AllowedUsers` (below).

---

## Deliverable 2 — Admin audit & revoke console — SHIPPED (creds + adoptions)

The admin is currently **blind** to the user plane (`List` returns only global;
user resources live under `ListUser(username)`). For a security posture the admin
needs read + kill across every user's namespace, even resources they don't own.

**Shipped:** two admin sections next to API Credentials.
- **User-owned credentials** — `SecureAPI.ListAllUserOwned()` enumerates every
  `Owner != ""` credential; rows carry owner/name/type/secured/disabled with
  owner-aware **Disable** (`SetDisabledOwned` — revoke without delete) / **Enable**
  / **Delete** (`DeleteUser`). Endpoint `api/user-credentials`.
- **Global-tool adoptions** — one row per (tool, adopter) from
  `LoadAdoptedGlobalTools` across `AuthListUsers`, a **⚠ stale** flag when the tool
  has left the shared catalog, and a **Remove** action (`SetGlobalToolAdopted
  false`). Endpoint `api/tool-adoptions`. Shows a shared tool's blast radius.
- Covered by `TestUserOwnedGovernance`.

**User-owned tools** are already visible in the existing Global Tools section (the
persistent pool is admin-visible), so they aren't duplicated here. **User-owned
agents** are deferred to Deliverable 5 — there is nothing to audit or revoke until
peer-sharing exists; that section joins this area then.

### New admin section: "User namespace"

One `ui.Table` per resource kind, enumerated across all users:

```
for _, u := range AuthListUsers(db):
    creds  += SecureAPI.ListUser(u.Name)
    tools  += LoadPersistentTempTools(db, u.Name)
    agents += user-owned agents for u.Name
adoptions = for each global tool: which users adopted it (LoadAdoptedGlobalTools)
```

Columns: Owner, Name, Kind, Shared/AllowedUsers summary, State (enabled/disabled).

### Row actions (governance, not ownership)

- **Revoke** → force-`Disabled = true` on the record (hard kill; owner keeps it
  but it stops resolving). Reuses the existing `Disabled` field on creds/tools;
  agents get `ForcePrivate` or a disable equivalent.
- **Force-private** (agents) → set `ForcePrivate`.
- **Unshare** → clear `Shared` / `AllowedUsers`.

All gated by `CanManageShared(reqUser, owner, isAdmin)` — admin bypasses the
owner check, which is exactly what `core/sharing.go` already encodes.

### Adoption view

A companion table: for each Shared global tool, the list of users who've adopted
it (from `adopted_global_tools`). Lets the admin see blast radius before revoking
a global tool and, if needed, force-unadopt (`SetGlobalToolAdopted(false)`).

---

## Deliverable 3 — Promotion-request flow (user plane → global plane)

The bottom-up path. A user who built a useful cred/tool/agent requests it be
published deployment-wide; the admin approves.

### Store

New table `promotion_requests` (in the app DB, admin-visible):

```go
type PromotionRequest struct {
    ID        string    // random
    Owner     string    // requesting user
    Kind      string    // "credential" | "tool" | "agent"
    Name      string    // resource name in the owner's namespace
    Note      string    // owner's justification
    Created   time.Time
    State     string    // "pending" | "approved" | "denied"
    DecidedBy string    // admin username
}
```

### Owner side (Gateways)

A **"Request to publish"** row action on the user's own creds/tools/agents →
opens a `ModalButton` (note field) → POST creates a `pending` request. A pill on
the row shows "Requested".

### Admin side (governance console)

A "Pending promotions" table. **Approve** / **Deny** row actions.

**Approve** semantics differ by kind — this asymmetry is a hard safety rule:

| Kind | On approve |
|---|---|
| **Agent** | Mint a **global copy** (`Owner=""`); references resolve in each recipient's namespace at runtime (already how sharing works). Clean — no secret travels. |
| **Tool** | Publish the tool **definition** to the shared pool (`Shared=true`, `Owner=""`). Its `hook_capabilities` / temptool body carry no secret. Clean. |
| **Credential** | **Never** promote a static user secret to global (that would expose one user's secret deployment-wide). Only **hybrid / per-user-secret** creds may be promoted — the *shape* goes global, each adopter supplies their own secret. A static-secret cred promotion is **refused** with an explanatory error. |

On approve the request flips to `approved` and (optionally) the admin sets the
new global resource's `AllowedUsers` via the Deliverable-1 picker in the same
modal.

---

## Peer sharing (agents) — the user-plane path this console oversees

Free, no admin approval. Reuses `core/sharing.go` + the new `AgentRecord.AllowedUsers`.

- **Gateways** gets a "Share" row action on the user's own agents → `ui.UserPicker`
  modal → sets `AllowedUsers` + registers the agent in the shared index via
  `SetSharedOwner(appDB, agentsSharedIndex, id, owner, true)`.
- A recipient sees a shared agent if they're in `AllowedUsers`; credential
  references resolve in **their** namespace (`Resolve(name, sessionUser)`), so no
  secret travels — exactly the "share the shape" model already documented in the
  namespacing doc's Agent-sharing section (incl. the per-cred `delegate` opt-in).
- The admin console (Deliverable 2) can revoke any peer share (`CanManageShared`
  admin bypass).

---

## Sequencing within the batch

1. **Data + enforcement — SHIPPED.** `AllowedUsers` on `PersistentTempTool` +
   `AgentRecord`; `SharedToolAllowedUsers` + `CanAdoptGlobalTool` (permission, not
   existence — an unpublished name is harmless); `SetGlobalToolAdopted` refuses an
   ACL-denied adopt but always permits un-adopt; the Gateways catalog GET hides
   tools the user can't adopt. Covered by `TestGlobalToolAdoptACL`.
2. **ACL editor — SHIPPED (helper, not a new primitive).** `ui.ACLPicker` over the
   existing `ChipPicker`. No new component, no new JS.
3. **Deliverable 1 UI — SHIPPED.** `core.UserCandidatesJSON` + admin
   `api/user-candidates` (approved users as `{value,label}`, the shared ACLPicker
   source). Credential access: the Edit-Expand is now `Stack{FormPanel, ACLPicker}`
   (record mode over `api/secure-api?name={name}`); the free-text `allowed_users`
   tags field is gone (new creds default open, restrict from the row). Tool adopt
   access: an "Adopt access" `ExpandIf` (shared rows only) with an ACLPicker over a
   new `set_allowed_users` action + minimal `?allowed_users=` GET, backed by
   `core.SetPersistentTempToolAllowedUsers`. Covered by
   `TestSetPersistentTempToolAllowedUsers`. (UI follows the proven App-Groups
   FormPanel+ChipPicker pattern; needs a runtime smoke test on deploy.)
4. **Deliverable 2 — SHIPPED (creds + adoptions).** `ListAllUserOwned` +
   `SetDisabledOwned`; `api/user-credentials` + `api/tool-adoptions`; two admin
   sections. Agent audit deferred into step 5 (nothing to revoke until sharing).
5. **Peer sharing** — Gateways share action for agents (`AgentRecord.AllowedUsers`
   + `SetSharedOwner`), plus the user-owned-agents governance table in Deliverable 2.
6. **Deliverable 3** — promotion requests (store, Gateways request action, admin
   approve/deny with the kind asymmetry).

Steps 1–2 are the foundation (done); 3–6 each land independently on top.

## Open decisions

- **Peer-share approval.** Recommended: peer (named-set) sharing is free; only
  global promotion is gated. A regulated deployment may want a site setting
  ("require admin approval for any cross-user share") — a single gate in the
  peer-share handler, deferred unless asked.
- **Credential promotion of static secrets.** Refused outright (above). Confirm
  there's no case where an admin *wants* to bless a user's static-secret cred
  as global — if so, it must be a re-entry of the secret by the admin, never a
  copy of the user's stored one.
- **Global copy vs move on promotion.** Recommended: agents/tools **copy** to
  global (owner keeps their private original); the owner isn't stripped of their
  work by publishing it.
- **User enumeration exposure.** The `UserPicker` source reveals usernames/emails
  to any user sharing an agent. Acceptable for peer sharing within one
  deployment; note it isn't multi-tenant-safe if that ever changes.

---

## Appendix — dashboard clustering of the orchestrator family (related, separate batch)

Today `serve_dashboard` renders a **flat** card grid (`dashApp` list;
`featured` / `wide` / regular sizing only). But the hub tab row already knows the
orchestrator family — every member implements `WebAppHubTab` (`HubTab() (label,
order)`): Agents(10), Bridges(20), Knowledge(30), Gateways(40). The dashboard
should reflect that same grouping instead of scattering these among unrelated
apps.

**Reuse the existing signal — add no new metadata.** An app is "in the family"
iff it implements `WebAppHubTab`. That single interface already single-sources the
tab row; the dashboard reads the same set.

Proposed dashboard IA:

- **Hero:** the featured entry point (Chat/Orchestrate, `WebAppFeatured`) stays
  the full-width hero — the family's front door.
- **Cluster:** apps implementing `WebAppHubTab`, ordered by their `HubTab` order,
  render as one visually-grouped block **under a heading** (e.g. "Orchestrator")
  directly beneath the hero — a bordered group, not loose cards.
- **Everything else:** custom apps + standalone web apps keep the existing flat
  grid below the cluster.
- **Admin:** stays the bottom `wide` utility card.

Implementation is contained to `core/webapp.go`:

- `serve_dashboard` partitions `apps` into `{hero, familyCluster, rest, admin}`;
  membership test is a `WebAppHubTab` type-assert (same as `HubNav`).
- The cluster renders as a titled `<section>` with its own bordered grid; the
  heading label is a constant (or a new tiny `WebAppFamily() string` method only
  if we ever need >1 family — **not** needed now, don't add it speculatively).
- Family order = `HubTab` order, so tab-row and dashboard stay in lockstep from
  one source.

This is **UI/IA only** — no permissions, no data model. It can land before or
after the sharing batch; it shares nothing with it except the observation that
`WebAppHubTab` is the canonical "orchestrator family" membership test. Keep it a
separate commit.
