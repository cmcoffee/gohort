package core

import (
	"bytes"
	"io"
	"strings"

	"github.com/cmcoffee/snugforge/nfo"
)

// Agent defines the interface for fuzz agents.
type Agent interface {
	Get() *AppCore
	Name() string
	Desc() string
	SystemPrompt() string
	Init() error
	Main() error
}

// CLIApp is an optional marker interface — opt-in for apps that
// have a meaningful command-line workflow beyond "use the dashboard."
// The menu hides every Agent that does NOT implement this from
// --help, and refuses CLI dispatch with a friendly "use serve"
// hint. Default is dashboard-only because that's where almost
// every app lives now; CLI is the exception.
//
// Implement as a no-op method on the agent struct:
//
//	func (T *MyApp) CLI() {}
type CLIApp interface {
	CLI()
}

// ChatTool defines the interface for chat tools.
// Tools are lightweight functions available in chat mode and the agent loop.
type ChatTool interface {
	Name() string
	Desc() string
	Params() map[string]ToolParam
	Run(args map[string]any) (string, error)
}

// ConfirmableTool is an optional interface that ChatTool implementations
// can implement to indicate the tool requires user confirmation before execution.
type ConfirmableTool interface {
	NeedsConfirm() bool
}

// InternetTool is an optional interface that ChatTool implementations
// can implement to indicate the tool contacts the internet. Tools that
// implement this are excluded from private-mode chat sessions.
type InternetTool interface {
	IsInternetTool() bool
}

// CapabilityTool is an optional interface ChatTool implementations can
// satisfy to declare what side effects they have (CapRead / CapNetwork /
// CapWrite / CapExecute). The agent loop reads these via Tool.Caps when
// AllowedCaps gating is active. A tool that doesn't implement this is
// treated as "unannotated" and passes the cap filter unconditionally —
// migration-safe; tighten gradually as tools opt in.
type CapabilityTool interface {
	Caps() []Capability
}

// CategorizedTool is an optional interface a ChatTool implements to CLAIM a
// category — the section header it groups under in every tool picker (see
// Tool.Category for the field this populates).
//
// Temp tools carry their category as a stored field; a registered Go tool had
// no way to state one, so its grouping fell back to the admin-curated
// ToolGroup.Members list. That works for a fixed set of built-ins and not at
// all for tools that appear at runtime: an MCP server's tools are discovered
// when it connects, so nobody can pre-list them as members of anything, and
// they all landed in the generic capability bucket together.
//
// ToolCategory below is the canonical accessor.
type CategorizedTool interface {
	Category() string
}

// ToolCategory returns the category a tool claims, or "" when it claims none —
// in which case the caller falls back to ToolGroup.Members and then to the
// capability label, in that order.
// ChatToolCaps is what a tool declares it can DO, or nil when it declares
// nothing. One accessor, because "read the optional interface if it is there"
// is the kind of three-line idiom that gets written five times and then
// disagrees with itself in one of them.
func ChatToolCaps(ct ChatTool) []Capability {
	if c, ok := ct.(CapabilityTool); ok {
		return c.Caps()
	}
	return nil
}

func ToolCategory(t ChatTool) string {
	if c, ok := t.(CategorizedTool); ok {
		return strings.TrimSpace(c.Category())
	}
	return ""
}

// FrameworkTool is an optional interface tools implement to declare
// "I'm framework infrastructure — never offer me as a user-toggleable
// option in any picker." Tools tagged this way are still REGISTERED
// (they appear in RegisteredChatTools), still EXECUTABLE, and still
// wired into agent / phantom catalogs when the framework decides
// they're needed (workspace is always wired; stay_silent / keep_going
// ride along on every turn; skills appear when the owner
// has workers). They just don't show up in the chip pickers /
// allowed-tools lists that users see.
//
// IsFrameworkTool below is the canonical accessor — pickers consult
// it rather than maintaining their own skip lists.
type FrameworkTool interface {
	IsFrameworkTool() bool
}

// IsFrameworkTool reports whether the given ChatTool is framework
// infrastructure that should be hidden from user-facing pickers.
// Returns false for tools that don't implement FrameworkTool or
// implement it as false — back-compat default is "user-selectable".
func IsFrameworkTool(t ChatTool) bool {
	if ft, ok := t.(FrameworkTool); ok {
		return ft.IsFrameworkTool()
	}
	return false
}

// SingleFireTool is an optional interface ChatTool implementations
// can satisfy to declare "only one call to me per batch." When the LLM
// emits multiple parallel calls to a single-fire tool in one response,
// only the FIRST runs; the rest get a SKIPPED notice. The round itself
// CONTINUES — this isn't a round-abort; it's a per-tool batch dedup.
//
// Use for tools where multi-fire-per-batch is structurally wrong
// regardless of other tools: authoring actions (create_agent /
// add_tool), outbound communication (send_email / vapi_call), long-
// lived resource creation (watcher_create), or anything where
// bundling indicates the LLM is parallel-planning rather than
// reasoning step-by-step.
//
// For cross-tool single-fire (e.g. find_image + fetch_image attaching
// images — only one across the group fires), use AgentLoopConfig's
// SingleFireGroups field instead. Tool-level single-fire is implicit
// (auto-grouped as a one-element group); the explicit config handles
// multi-tool groups.
type SingleFireTool interface {
	SingleFirePerBatch() bool
}

// SerialFireTool is an optional interface for stateful tools whose batched
// calls are a legit SEQUENCE, not a duplicate. When the LLM emits several
// calls in one response, they all run — but SEQUENTIALLY in submission order,
// each observing the prior call's mutations — instead of the single-fire
// first-wins skip. Other tools in the same batch still run in parallel.
//
// Use for authoring tools where a batch like [delete X, create Y] is a real
// two-step edit: single-fire would run the delete and SKIP the create,
// leaving state half-changed (the tool gone, its replacement never made) and
// costing a round to recover. Serial-fire runs both in order, this turn, with
// no concurrent-mutation race. tool_def is the canonical case.
//
// Prefer SingleFireTool for tools where a second call is genuinely wrong
// (send_email, an image attach) rather than the next step of a sequence.
type SerialFireTool interface {
	SerialFirePerBatch() bool
}

// TrustedOutputTool is an optional interface a ChatTool implements to declare
// its result is framework-generated control / authoring text, not raw external
// content — so the untrusted-content fence should be suppressed even when the
// tool declares CapNetwork for a sub-capability (e.g. tool_def's "test"
// action). Maps to Tool.TrustedOutput at conversion time. A tool whose PURPOSE
// is fetching external content must NOT implement this as true.
type TrustedOutputTool interface {
	TrustedOutput() bool
}

// SessionChatTool extends ChatTool for tools that need per-session state.
// When a *ToolSession is provided via GetAgentToolsWithSession, RunWithSession
// is called in preference to Run.
type SessionChatTool interface {
	ChatTool
	RunWithSession(args map[string]any, sess *ToolSession) (string, error)
}

// DynamicChatTool is an optional interface for tools whose SCHEMA — the
// description and parameters the LLM actually sees — depends on live state:
// what's configured, what's registered, what this caller can reach. A static
// tool advertises everything and refuses at call time ("that provider isn't
// configured"), burning a round the model had no way to predict; a dynamic one
// advertises only what will actually run.
//
// SchemaWithSession REPLACES Desc/Params when the catalog is built with a
// session. Desc() stays meaningful and must keep returning a stable,
// session-independent string: it feeds the semantic tool index (tool_index.go),
// which is global and can't be re-embedded per session, and every admin/picker
// surface that lists tools without a caller.
//
// Returning NIL params means "nothing to offer under this session" — the tool
// is dropped from the catalog entirely (see ChatToolAvailable). Never return a
// non-nil schema with an empty action enum: an empty Enum invalidates the
// whole tool payload for the turn, which is the failure TestNoEmptyEnumValues
// guards against at the source level and a dynamic enum can reach at runtime.
//
// The returned schema MUST be deterministic for identical state. Tool schemas
// sit at the front of the prompt, so a description or enum that reorders
// between turns invalidates the prompt prefix cache and re-pays cold prefill
// every turn. Sort anything derived from a map, and keep fixed orders fixed.
//
// Implementations must be CHEAP: this runs on every catalog build, not once
// per call. Read config, not the database; memoize on the session if a lookup
// is genuinely expensive.
type DynamicChatTool interface {
	ChatTool
	SchemaWithSession(sess *ToolSession) (desc string, params map[string]ToolParam)
}

// NeedInteract pauses for user input.
func NeedInteract() {
	nfo.PressEnter("\n(press enter to continue)")
}

// GetBodyBytes returns a function that returns an io.ReadCloser for the given byte slice.
func GetBodyBytes(input []byte) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(input)), nil
	}
}
