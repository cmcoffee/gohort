# Sharing & Governance — post-deploy smoke test

A walkthrough for the UI + dispatch behavior shipped in the phase-5
sharing/governance batch (see `sharing-governance.md` for the design). None of
this was runtime-tested during development — it's declarative UI + access wiring —
so verify it against a running server after deploy. Work top to bottom; later
sections reuse fixtures from earlier ones.

## Fixtures

Create three accounts (Admin → Users):

- `admin` — an administrator.
- `alice`, `bob` — two ordinary (non-admin) users, both approved.

Have at least one **global credential** and one **user's own tool** to exercise
things:

- As `admin`, Admin → API Credentials → add an open credential `demo_api`
  (bearer, any base URL, a dummy secret).
- As `alice`, ask the assistant in chat to build a simple tool for her (so her
  Gateways "My tools" is non-empty). Call it `alice_tool`.

---

## A. Dashboard clustering + Agents rename

- [ ] The dashboard shows an **"Orchestrator"** cluster (a titled, bordered block)
      containing **Agents, Bridges, Knowledge, Gateways** — not scattered among the
      other app cards.
- [ ] The **Agents** card is the full-width **lead** card *inside* that cluster
      (not a separate hero above it), and it reads **"Agents"** — the word
      "Agency" appears nowhere user-facing (card, page title, help text).
- [ ] Non-family apps (custom apps, etc.) sit below the cluster; **Administrator**
      is the wide card at the bottom.
- [ ] On a narrow window the cluster grid collapses to one column.

## B. Admin credential access — tier 1 (which users)

As `admin`, Admin → API Credentials, on the `demo_api` row:

- [ ] There is an **"Access"** button. Opening it shows a user picker (from the
      approved users), NOT a per-agent list.
- [ ] Add `alice` to the list and save. Reopen — `alice` persists.
- [ ] **Edit** (the expand) shows only the config form now — no inline
      allowed-users editor (that moved to the Access button).
- [ ] Flip the credential to **🔒 Secured** (the segmented pill). The **Access
      button disappears** (secured creds defer access to their bound tools —
      the "Bindings" expand is their access surface). Flip back to **Open** →
      Access returns.

## C. Agent editor — tier 2 (which of my agents)

As `admin`, open any agent in the editor (Agents → a row → Edit):

- [ ] A **"Credentials this agent may use"** section lists the credentials you're
      granted (includes `demo_api` if you're allowed it), **all checked by
      default**.
- [ ] Secured credentials are **not** listed here (access follows their bindings).
- [ ] Uncheck `demo_api`, reload the editor — it stays unchecked (persisted to the
      agent's opt-out). Re-check it — persists.

## D. Global tools access — symmetric with credentials

As `admin`, Admin → Global Tools:

- [ ] `alice_tool` appears (owner `alice`). **Share** it.
- [ ] On the shared row, an **"Access"** button opens the same kind of user
      picker — "which users may adopt this tool." (Non-shared rows show no Access
      button.) There is **no** per-agent scope pill on this section anymore.
- [ ] Restrict adoption to `bob` only, save.

## E. Admin governance console (the user-plane view)

As `admin`, Admin page — three sections near API Credentials:

- [ ] **User-owned credentials** — after alice creates a credential on her
      Gateways page (section F), it appears here with owner `alice`, and
      **Disable** / **Enable** / **Delete** work (disable = revoke without delete).
- [ ] **Global-tool adoptions** — after a user adopts a shared tool (section F),
      one row per (tool, adopter) shows here, with **Remove**; a **⚠ tool
      unshared** badge appears if you later unshare a tool someone adopted.
- [ ] **User-owned agents** — see section G/H.

## F. Gateways (as a user) + tool promotion

As `alice`, open **Gateways**:

- [ ] **My credentials** — create one (`alice_cred`). Confirm it then appears in
      the admin **User-owned credentials** governance section (E).
- [ ] **My tools** — `alice_tool` is listed. It has a **"Request to publish"**
      action (a modal with a note field) — visible only while the tool isn't
      shared and has no request pending.
- [ ] Send a publish request. The row now shows a **"Publish requested"** badge
      and the request action is gone.
- [ ] As `admin`, Admin → **Pending promotions**: alice's request is listed with
      her note. **Approve** it → the tool becomes Shared; back on alice's Gateways
      "My tools", the "Publish requested" badge is **gone** (sharing fulfilled the
      request), and a "Shared" badge shows instead.
- [ ] Regression check for the badge fix: take another of alice's tools, and as
      admin **Share it directly** from Global Tools (not via Approve). Alice's "My
      tools" must **not** show "Publish requested" for it (a shared tool never
      shows the badge).
- [ ] **Global tools** catalog: as `bob` (whom you restricted in D), the shared
      `alice_tool` appears and can be **Added**. As a user NOT in its access list,
      it does **not** appear.

## G. Agent peer-share + recipient run (the core flow)

As `admin` (agent owner), open an agent in the editor:

- [ ] A **"Share with users"** section (a user picker). Add `bob`. Save.
- [ ] As `bob`, on the **dashboard**: a card for that agent appears (at
      `/agents/<slug>`).
- [ ] `bob` opens it and chats — it responds. Crucially, the session is **bob's**
      (his session list, his memory), and any credential the agent uses resolves
      in **bob's** namespace (not the owner's secret).
- [ ] As `alice` (NOT in the share list), the agent card does **not** appear, and
      hitting `/agents/<slug>` directly returns **404** (no slug-existence leak).
- [ ] Remove `bob` from the share list → his card/access disappears.

## H. Agent delegate / publish

As `admin`, Admin → **User-owned agents** (the governance table):

- [ ] The agent you shared in G is listed with owner + **"Shared with: bob"** +
      a **Shared** badge.
- [ ] Click **Publish** (the "delegate to users" action). Confirm. The row now
      shows a **Published** badge and the Publish button is gone.
- [ ] The owner's share is intact (Shared-with still shows `bob`).
- [ ] The agent is now a normal published app — grant its `/agents/<slug>` path to
      more users via Admin → Users → app access, and confirm they see it.
- [ ] **Revoke share** clears the recipient list (bob loses peer access) but the
      agent itself survives (and stays Published if you published it).

## I. Dispatch enforcement (behavioral — the access model)

Harder to see in the UI; verify by using a tool that dispatches through a
credential.

- [ ] **Open cred, restricted:** give `demo_api` an Access list of `[alice]`
      only. Have `bob` run an agent whose tool dispatches through `demo_api`
      (e.g. a `fetch_via` tool). The call is **refused** ("not shared with you") —
      tier-1 is enforced at dispatch, not just at tool-build.
- [ ] **Open cred, same tool, alice:** alice runs it → the call **succeeds**.
- [ ] **Secured cred defers to tools:** secure `demo_api`. Now a **bound** tool
      dispatches through it for **whoever can use that tool**, regardless of the
      old Access list (AllowedUsers is ignored for a secured cred). A **revoked**
      binding is refused for everyone (Admin → API Credentials → Bindings →
      Revoke).

---

## If something's off

- **A shared agent doesn't appear for the recipient** → confirm the owner's
  `AllowedUsers` actually saved (agent editor "Share with users"), and that the
  agent isn't a sub-agent or a clone-only seed (those never surface on `/agents/`).
- **The recipient sees the agent but a tool fails** → expected when the agent
  references a tool/credential that only resolves in the *owner's* namespace; the
  recipient brings their own (the "share the shape" model). Not a bug.
- **A governance section is empty** → these enumerate across all users; confirm
  the fixture user actually created the resource (own credential / adopted tool /
  shared agent), and that you're logged in as an admin.
