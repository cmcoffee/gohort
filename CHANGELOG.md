# Changelog

Notable changes to the gohort framework surface app authors build against. The
public surface and its stability tiers are defined in
`core/ui/AUTHORING.md` ("The public surface"). Framework internals (anything
under `core/internal/`, and undocumented `package core` symbols) can change
without a changelog entry.

## Unreleased (0.5.109 batch)

### Added
- `ui.Head` — typed builder for app-specific `<head>` behavior (client actions,
  block renderers, markdown extensions, CSS). Apps compose extensions in Go
  instead of hand-writing `<script>` blobs; the framework assembles the
  `<script>`, the `window.uiRegister*` calls, and the readiness guard. See
  `Page.Head` and `core/ui/extensions.go`.
- `window.uiOpenModal(opts)` — the shared modal primitive (plain fixed-overlay,
  mobile-safe, Escape-to-close, no backdrop-click-to-close). `uiOpenSimpleModal`
  is now a thin backward-compatible wrapper over it.

### Changed
- Modals no longer dismiss on backdrop click anywhere (a text-selection drag
  ending on the backdrop was dismissing them mid-copy). Dismiss via Escape or a
  close button.
- Migrated the admin Reset-password modal and the guides knowledge/sources/
  settings modals off hand-written `<head>` blobs onto `ui.Head`. No public API
  change; behavior preserved.

### Internal
- Introduced the `core/internal/` boundary (SDK public-API work). Moved
  `core/mcpclient` → `core/internal/mcpclient` (used only by `package core`).
  Apps are now compiler-barred from importing it. More `package core` internals
  will move here as they prove separable.
- `core/ui/runtime.go`'s CSS/JS extracted to `core/ui/assets/` (real files,
  `//go:embed`); the JS split into ordered fragments under `assets/runtime/`.
  App-facing runtime behavior unchanged.
