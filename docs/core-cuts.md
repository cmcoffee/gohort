# What can leave core, measured

Status: measured 2026-08-22 against v0.6.337. Re-run it with
`go run ./scripts/cutmap core <prefix>` rather than trusting these numbers after
the package has moved on.

Package `core` is 169 source files, 86,163 lines and 3,613 exported symbols, dot-imported
by 548 files. It was 111 files / 60,540 lines on 2026-08-06, so it has taken on 58 files
and 25,600 lines in sixteen days with nothing removed. The earlier extraction project
(`project_core_modularization`) cut 144 → 111 over weeks; that was undone in about a week.

## The measurement that decides a cut

Three numbers per candidate, in the order they kill an idea:

1. **Pinned methods.** A file declaring a method on a type that lives in another file
   cannot move without that type — Go forbids methods on another package's type. This is
   what stopped the pipeline extraction and it is invisible to a symbol graph.
2. **Outbound TYPE edges.** A leaf may not import the package it left (that is the cycle),
   so every type it speaks must move with it or be restated locally as a minimal interface.
   A func or a value can be inverted with a hook var the host assigns; a type cannot.
   **This is the number that predicts the work.**
3. **Inbound files.** Who in core still calls it. Small is good, and the facade pattern in
   `core/core.go` means callers outside core never see the move at all.

## Ranked, 2026-08-22

| cluster | files | lines | pinned | type edges | func+value edges | verdict |
|---|---|---|---|---|---|---|
| `sandbox*` | 4 | 3,195 | 0 | **3** — Error, NetworkConnector, ToolSession | 18 | **DONE v0.6.339**, see below |
| `vector*` | 2 | 1,652 | 0 | 3 — Database, EmbeddingConfig, MemoryProvenance | 11 | good, smaller |
| `workspace*` | 6 | 1,335 | 0 | 4 — Database, TempTool, ToolSession, TunableSpec | 15 | good |
| `mcp_*` | 3 | 2,224 | 0 | 8 | 17 | possible |
| `template*` | 4 | 706 | 0 | 9 | 6 | no — it is vocabulary |
| `image_*` | 8 | 3,479 | **1** | 27 total | | no — pinned + cycle |
| `machine*` | 7 | 3,828 | **2** | 42 total | | no |
| `pipeline_*` | 8 | 4,597 | **2** | 45 total | | no (known) |
| `peer_*` | 13 | 5,296 | 0 | **13** | 43 | no — see below |

Some of those type edges have known answers: `Error` becomes `errors.New` (what `core/deps`
did), `Database` becomes a local two-method interface that `core.Database` satisfies
structurally (`core/docs`, `core/media`), and a `TunableSpec` registration belongs on
core's side of the seam, never the leaf's — an imported package's `init` runs FIRST, so a
leaf registering through a hook registers into a nil (v0.5.867, silent).

## Why peer looked right and is not

By file counts it is the best cluster in core: 13 files, 5,296 lines, zero pinned methods,
and only five core files use it — twelve of those uses are `HandlePeerX` route registrations
in `webapp.go`.

At symbol level it is the worst except `connector_*`: **59 outbound symbols, 13 of them
types**, spanning ten subsystems (llm, embeddings, image generation, transcription, search,
cost, sandbox exec, secure API, scheduler, connectors). That is not an accident of layout.
Peer's whole job is to expose this deployment's capabilities to another one, so its
dependency surface is the union of every capability it proxies. Extracting it means
inverting all of them, and every hook that arrives nil in a CLI or test binary disables a
capability silently.

The internal structure rules out a smaller bite: the wire (`peer_key`, `peer_token`,
`peer_token_client`) and the adapters are mutually recursive — `peer_serve` alone uses 18
symbols from the wire, and the wire calls back into `peer_remote`, `peer_models` and
`peer_investigate`. It is one package or none.

## The rate, not the size

Any single extraction buys back about a week at the current rate. Nothing today makes adding
a fourteenth `peer_` file to core a decision rather than the default — the hub is where
`AppCore` and the shared vocabulary live, so that is where everything lands. A ceiling test
(fail the build when package `core` passes N files or M exported symbols) turns the drift
into a prompt at the moment someone is choosing, which is the only point where it is cheap.

## What the sandbox cut actually looked like (v0.6.339)

The prefix was four files; the cut was three, and the line it was cut along is worth more
than the lines it removed.

`sandbox_hook.go` is not sandbox plumbing. It is the **capability broker**: it decides
whether a confined script may read a secret, dispatch through a secured credential, or
fetch a URL, and it enforces that against the SecureAPI, the session's denials and the
secured-binding rules. Its coupling to `Secure()` runs through unexported methods
(`sec.loadSecret`), which cutmap cannot see — it skips selector expressions on purpose, and
a method call on a type from another file looks like nothing at all. **That is the tool's
remaining blind spot, and it is the same shape as method pinning.**

So the boundary became a question rather than a prefix: *how do we confine a process*
(backend selection, exec, seatbelt profile, mounts, PATH shims) versus *what may the
confined process ask the host for* (credentials, secrets, fetch_via). The first left as
`core/sandbox`; the second stayed beside the SecureAPI that answers it.

Cost: three hook vars (`WorkspacesDir`, `BulkStagingDir`, `GohortLibDir`), one interface
(`HookServer` — `Path()` and `Close()`, which is everything the mechanics need of a broker),
and `sess any` for a session this package passes through and never reads. `Error` became a
local type; `NetworkConnector` disappeared entirely because `NetworkAllowedFromContext`
already lived in `core/netgate`. The two in-sandbox mount paths moved to the leaf and are
re-exported under their old names, so the broker's own file did not change.

A nil hook here fails toward LESS: no broker means no socket, so a script can ask the host
for nothing. That direction is not automatic and is the thing to check on every hook in
security-adjacent code.

Six test files followed the code, and four had to be SPLIT because they tested both halves
(`path_scope_enforce`, `sandbox_shim`, `sandbox_hook_path`, `sandbox_hook_lib`,
`sandbox_watch_probe`). A test that spans the seam is usually the seam telling you where it
really is.
