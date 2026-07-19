# Secured credential → tool binding

**Status:** Shipped. Model: **auto-resolve, deny-by-exception.** Prereq context:
`project_secured_credentials`, `project_credential_ownership`.

> History: this started as an approval-gated model (a new tool binding a secured
> cred needed admin sign-off). That was reverted — the approval step duplicated
> protections already in place and added friction. The current model auto-resolves
> access from the tool's declaration; an explicit revoke is the only admin gate.

## The model

Securing a credential moves access control from the **credential** plane to the
**tool** plane:

- A secured cred is reachable **only** through tools that DECLARE it (`fetch_via:`
  / api-mode `credential=` / a toolbox credential) — never the ambient auto-route,
  never a direct `fetch_url_<cred>`. The secret is injected server-side; the tool
  and agent never see it.
- **Declaring the cred auto-binds the tool** — no approval step. Access is
  auto-resolved from the declaration; the tool's own scope decides which agents
  can use it. "The tool is the access unit; scope the tool, and its holders get
  the cred."
- **`secret:<cred>` is always hard-blocked.** Handing the raw secret to a script
  violates securing's contract — that's not a binding and never is.
- **Revoke is the exception.** An admin can DENY a specific tool from the Bindings
  UI. A revoke is a **durable tombstone**: it refuses the tool at dispatch, blocks
  a same-name (re)author, and survives edits + delete/recreate. Re-approve to undo.

Why no approval gate: the secret is server-side regardless; the credential's own
`AllowedURLPattern` / `AllowedEndpoints` bound what any bound tool can hit; a
high-stakes cred uses `RequiresConfirm` (per-call Allow/Deny); and the tool's
scope already governs agents. A human checkpoint on *which tools may declare it*
was redundant.

## Binding states

Two states, tracked on the credential:

- `ApprovedToolBindings` — bound (auto-resolved on declaration, or an admin
  un-revoke). Shown as **Bound** in the admin UI.
- `RevokedToolBindings` — a durable admin deny (tombstone). Shown as **Revoked**.

`EnforceSecuredBinding(cred, tool)` at dispatch: revoked → refuse; otherwise allow
(and record the binding). A declaring-but-unrecorded tool is auto-bound on first
dispatch. Unnamed callers (`run_local` / persistent shell, already gated by the
hook's fetch_via grant) aren't binding-enforced.

## Where it's enforced

- **Authoring** (`tools/temptool`): declaring a secured cred via `fetch_via:` /
  api-mode / toolbox auto-approves the binding, unless the tool is revoked (then
  refused). `secret:<secured>` is hard-blocked.
- **Dispatch**: the fetch_via sandbox hook (via `SandboxHook.ToolName`) and the
  api/toolbox chokepoint `dispatchTempToolUncached` call `EnforceSecuredBinding`.
- **Edit**: a `tool_def(update)` just re-resolves (no re-review). A revoked tool
  can't be edited back into service — the guard respects the deny.
- **Delete**: `ForgetToolBinding` drops the approval but KEEPS a revoke tombstone,
  so a deny survives delete + same-name recreate.

## Admin UI

Each SECURED credential row (Admin > APIs) has a **Bindings** expand: the tools
**Bound** to it (auto-resolved) and any **Revoked**, with **Revoke** (deny) and
**Approve** (un-revoke) buttons. Backed by `GET api/secure-api?bindings={cred}`
and `POST api/secure-api?action={approve,revoke}_binding&name={cred}&tool={tool}`.
Effective access = the existing **Tools** expand (tool → the agents it's scoped
to). Securing itself still removes the generic `call_<cred>` tool + the auto-route.

## Tests

`TestSecuredToolBindingLifecycle`, `TestEnforceSecuredBinding`,
`TestSecuredBindingForgetKeepsRevoke` (core); `TestSecuredCredBindingAuthoring`,
`TestSecuredBindingAutoResolveEdits` (temptool).
