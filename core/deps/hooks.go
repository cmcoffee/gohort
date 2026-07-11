package deps

// Injected hooks into core services this leaf can't import. deps is a pure
// package (it must not import core, or the graph cycles); the one hub service it
// needs is the sandbox workspaces root, injected by core at startup. See core.go.

// WorkspacesDir returns the sandbox workspaces root. Wired by core to its
// WorkspacesDir(). nil until then — the managed python-deps dir is a sibling of
// this path, so an unset hook means provisioning is skipped (callers treat "" as
// "not configured" and the sandbox import fails loudly, which is the right shape).
var WorkspacesDir func() string

// workspacesDir is the internal nil-safe accessor.
func workspacesDir() string {
	if WorkspacesDir == nil {
		return ""
	}
	return WorkspacesDir()
}
