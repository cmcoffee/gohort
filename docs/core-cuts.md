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
| `sandbox*` | 4 | 3,195 | 0 | **3** — Error, NetworkConnector, ToolSession | 18 | best candidate |
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
