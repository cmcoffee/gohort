# Secured credential → tool binding

**Status:** Spec. Prereq context: `project_secured_credentials`,
`project_credential_ownership`.

## The model (intended)

Securing a credential moves access control from the **credential** plane to the
**tool** plane:

- A secured cred is reachable **only** through tools that DECLARE (bind to) it —
  never the ambient auto-route, never a direct `fetch_url_<cred>`.
- Access to the cred = access to a declaring tool. Whoever has that tool scoped
  can use the cred *through* it. The cred's own scope pill becomes irrelevant.
- The secret **never** reaches the agent/script — dispatch injects it
  server-side (`fetch_via:` / api-mode `credential=`). `secret:<cred>` (raw key
  handed to the script) is never allowed for a secured cred.

"The tool is the access unit; scope the tool, and its holders get the cred."

## What already holds — do NOT rebuild

- **Dispatch already follows tool-scope.** `SecureAPI.dispatch` (secure_api.go)
  does not check the agent's credential scope (`CredentialDenied`) or the Secured
  flag. Declaring tools dispatch fine; comments say so outright ("Secured
  credentials stay dispatchable — their declaring tools keep working"; "the
  explicit declaring tools, whose own scope is the access [control]").
- **Ambient paths correctly skip secured creds.** `AutoRouteCredential` and the
  direct `fetch_url_<cred>` tool both exclude Secured — otherwise any script
  could reach the secured host and the lock would mean nothing.
- **Editing a tool that already holds the grant is preserved** (the
  `_existing_cred_grants` fix): a description edit no longer strips a
  `fetch_via:<secured>` binding.

## The gap

1. **You can't AUTHOR a new binding to a secured cred.** The authoring guard
   (`tools/temptool/temptool.go:~429`) blocks `fetch_via:<secured>` the SAME as
   `secret:<secured>` — even though `fetch_via` never exposes the secret
   (server-side dispatch). So the only way to bind a new tool is the clumsy
   unsecure → wire → re-secure dance. That defeats "the tool is the access unit."
2. **No explicit binding record.** Which tools are bound to a secured cred is
   implicit in each tool's `hook_capabilities` / `credential` field. There's no
   authoritative list to review, revoke, or lock against.
3. **No binding edit-lock.** Nothing distinguishes "edit the tool's logic" (fine)
   from "re-point its credential wiring" (should require review).

## Proposed mechanism

1. **Reviewed binding (the core change).** Authoring a tool that declares
   `fetch_via:<secured>` or api-mode `credential=<secured>` is ALLOWED, but the
   tool lands in the existing pending-approval queue (`QueuePendingTempTool`)
   flagged as a *credential-binding request*. On approval the binding is
   sanctioned and the tool works; reject blocks it. The secret is server-side
   throughout. `secret:<secured>` stays hard-blocked — never reviewable (securing
   means the secret never reaches a script).
2. **Explicit binding record.** The secured cred tracks its set of approved tool
   bindings (`cred → {toolID…}`) — the authoritative "declaring tools" list.
   Powers the admin view, revocation, and the edit-lock. Backfilled on migration
   from tools whose `hook_capabilities` already grant the cred (grandfathered
   pre-secure declaring tools).
3. **Binding edit-lock.** A tool bound to a secured cred stays editable in
   logic/description, but its credential wiring (the `fetch_via:` grant /
   `credential` field) is immutable without re-review. Drop/swap → admin action.
4. **Access = tool scope.** No dispatch change — already true. Lock it in with a
   test so a future scope check can't regress it.

## Admin surface

- The secured cred's card lists its **bound tools**, and for each, the agents it's
  scoped to — i.e. the **effective access set** for the cred. Revoke a binding
  here (severs the cred from the tool; does not delete the tool).
- The tool-approval queue entry shows a "binds secured credential X" flag.

## Phases

- **P1** — allow `fetch_via:` / api-mode binding to a secured cred → routes to the
  approval queue (keep `secret:<secured>` blocked). Add the explicit binding
  record on the cred + migration backfill.
- **P2** — binding edit-lock (wiring immutable without re-review).
- **P3** — admin surface: bound-tools list + effective-access view + revoke.

## Open decisions

- **Grandfathering:** backfill the binding record from existing tools whose
  `hook_capabilities` already grant the (now-secured) cred? → **Yes**, on
  migration, so today's working declaring tools appear in the record.
- **`secret:<secured>` ever allowed via review?** → **Hard-never.** Securing's
  contract is that the secret never reaches a script; `secret:` violates it.
- **Revoke semantics:** delete the tool, or just sever the cred? → **Sever** —
  the tool stays, dispatch fails until re-bound/re-reviewed.

## Immediate unblock (pre-P1), e.g. `ts3_status` → `ts3_api`

Admin > APIs → unsecure `ts3_api` → author `ts3_status` as api-mode
(`credential=ts3_api`) or shell + `fetch_via:ts3_api` → re-secure. Now it's a
declaring tool: dispatch works, and Gohort (which has the tool scoped) can use it
— the model, realized by hand until P1 removes the dance.
