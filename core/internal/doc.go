// Package internal roots the framework's non-public machinery.
//
// Go enforces the boundary at compile time: anything under core/internal/ can
// be imported only by packages rooted at core/ (i.e. package core and its
// sub-packages). Apps (under apps/…, private/…) and main (repo root) physically
// cannot import it — so code moved here can be refactored freely without
// breaking any app.
//
// This is the enforcement half of the SDK public-API boundary (SDK roadmap
// item #2). The public, semver-committed surface an app author builds against
// is documented in core/ui/AUTHORING.md ("Backwards-compatibility tiers");
// everything else in package core is framework internals and is being migrated
// under here package-by-package as it proves separable. mcpclient was the first
// (only package core's mcp_manager.go used it).
package internal
