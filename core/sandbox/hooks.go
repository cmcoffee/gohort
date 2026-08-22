// What the confinement mechanics need from the host, and nothing more.
//
// This package answers one question — how do we run somebody else's code
// without letting it reach the rest of the machine — and it deliberately does
// NOT answer the other one: what a confined process may then ask the host for.
// That second question is credential enforcement (which secret, which secured
// binding, which URL) and it stays in core beside the SecureAPI that decides it.
// The seam between them is NewHook below: the mechanics start a broker they know
// nothing about, and thread its socket into the sandbox.
//
// Everything here is assigned by core at init. A nil hook is a binary that never
// wired this package up (a CLI, a test); each accessor says what it does in that
// case, and the answer is always the one that grants LESS rather than more.

package sandbox

import "errors"

// Error is a string error, the same shape core uses. Local rather than imported:
// a leaf cannot import the package it left.
type Error string

func (e Error) Error() string { return string(e) }

var errNoHookServer = errors.New("no sandbox hook server is wired into this binary")

// HookServer is the capability broker, seen from here: a unix socket a confined
// process talks to, and a way to shut it down. Whatever else it does — secrets,
// fetch_via, credential binding — is none of this package's business.
type HookServer interface {
	// Path is the host-side socket path bound into the sandbox.
	Path() string
	Close()
}

var (
	// WorkspacesDir is the root under which per-user workspaces live. Empty
	// means unconfigured, and every caller here already treats that as "no
	// workspace to mount".
	WorkspacesDir func() string

	// BulkStagingDir is the staging area a bulk run reads from, mounted
	// read-only when it is set.
	BulkStagingDir func() string

	// GohortLibDir returns the host directory holding the gohort python helper,
	// bind-mounted read-only at GohortLibMountPath. Empty = the helper is not
	// available and scripts importing it will fail; the sandbox still runs.
	GohortLibDir func() string

	// NewHook starts the capability broker for one run. sess is the caller's
	// session, passed straight through — this package never looks inside it.
	// nil = no broker in this binary, which is the safe direction: a script
	// gets no socket, so it can ask the host for nothing.
	NewHook func(workspaceDir string, capabilities []string, sess any) (HookServer, error)
)

func workspacesDir() string {
	if WorkspacesDir == nil {
		return ""
	}
	return WorkspacesDir()
}

func bulkStagingDir() string {
	if BulkStagingDir == nil {
		return ""
	}
	return BulkStagingDir()
}

func gohortLibDir() string {
	if GohortLibDir == nil {
		return ""
	}
	return GohortLibDir()
}

func newHook(workspaceDir string, capabilities []string, sess any) (HookServer, error) {
	if NewHook == nil {
		return nil, errNoHookServer
	}
	return NewHook(workspaceDir, capabilities, sess)
}

// GohortLibMountPath is the in-sandbox path where the gohort helper package is
// bind-mounted (read-only). Scripts get this path in PYTHONPATH so
// `from gohort import fetch` resolves regardless of where the running script
// lives within the workspace.
//
// It lives here rather than with the broker because WHERE something is mounted
// inside the sandbox is a confinement fact: the mount args are built here.
const GohortLibMountPath = "/opt/gohort-lib"

// GohortBinMountPath is a shim bin dir under the RO-mounted gohort lib,
// prepended to PATH inside every sandbox. It holds tiny executables
// (fetch_url / fetch_via / browse_page) so a script can call gohort's fetch
// family as ORDINARY COMMANDS instead of subprocessing an LLM tool name — which
// is not a shell binary and only fails with FileNotFoundError, the exact footgun
// that sent authored watch scripts down a "the API is impossible" rewrite
// spiral. The shims proxy to the SAME broker that `from gohort import ...` uses,
// so granted capabilities are still enforced there: an ungranted credential is
// denied at the broker, not the shim. No new privilege — just symmetry between
// "call the tool" and "shell out to the tool".
const GohortBinMountPath = GohortLibMountPath + "/bin"
