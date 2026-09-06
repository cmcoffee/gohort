package core

import (
	"fmt"
	"time"
)

// TempToolMode determines how a temp tool's body is interpreted at
// dispatch time. Two modes today: shell command (the original) and
// secure-API call (wraps a registered credential).
const (
	TempToolModeShell      = "shell"
	TempToolModeAPI        = "api"
	TempToolModePipeline   = "pipeline"
	TempToolModePersistent = "persistent" // long-lived shell process; action-dispatched (open/send/read/interrupt/close)
	TempToolModeToolbox    = "toolbox"    // multi-action wrapper bundling several api-mode endpoints under one tool name; surfaces in the catalog as a GroupedTool with action="<sub>" dispatch
)

// TempToolAction is one sub-endpoint of a toolbox-mode TempTool.
// Each action is a single api-mode HTTP endpoint with its own params,
// URL template, method, optional body template, and optional response
// pipe. The parent TempTool's Credential is shared across actions —
// real-world API wrappers almost always have one credential per API,
// and putting it at the parent level avoids the LLM having to repeat
// it per action. To call: <toolbox_name>(action="<sub-action name>",
// <action-specific args>).
type TempToolAction struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Params      map[string]ToolParam `json:"params,omitempty"`
	Required    []string             `json:"required,omitempty"`
	URLTemplate string               `json:"url_template"`
	// CommandTemplate makes this action a SHELL action instead of an HTTP one:
	// a command line with {placeholder} arguments, run exactly the way a
	// shell-mode TempTool's template is (placeholders shell-quoted, executed
	// through the sandbox). Exactly one of URLTemplate / CommandTemplate is set.
	//
	// It exists because "several related commands under one name" is the same
	// shape as "several related endpoints under one name", and a toolbox that
	// could only wrap an API was half a toolbox. A local binary is one thing
	// with several verbs — unpack, verify, list — and mapping it into loose
	// tools scattered across a catalog loses the fact that they are one thing.
	//
	// The dispatcher builds a synthetic shell-mode tool from the action and
	// hands it to the ordinary shell path, so an action behaves at call time
	// exactly as the same command would as a standalone tool: same quoting,
	// same sandbox, same workspace. A second execution semantic living inside
	// the toolbox is the thing to avoid — that is where drift starts.
	CommandTemplate string `json:"command_template,omitempty"`
	Method          string `json:"method,omitempty"`
	BodyTemplate    string `json:"body_template,omitempty"`
	// ContentType drives raw (non-JSON) body substitution for THIS action, the
	// same way TempTool.ContentType does for a single api tool. Empty = JSON
	// (placeholders JSON-encoded + validated); a non-JSON value like
	// application/xml or text/calendar switches the action's body_template to
	// RAW substitution + no JSON validation. Lets a toolbox mix JSON, XML, and
	// iCalendar actions (e.g. a CalDAV toolbox: REPORT/xml + PUT/text/calendar).
	ContentType string `json:"content_type,omitempty"`
	// Headers are extra request headers for THIS action, the same way
	// TempTool.Headers works for a single api tool. See TempTool.Headers.
	Headers      map[string]string `json:"headers,omitempty"`
	ResponsePipe string            `json:"response_pipe,omitempty"`
	// ResponseExtract parses THIS action's XML response into JSON (see
	// TempTool.ResponseExtract / ExtractSpec). Per-action so a toolbox can
	// have some JSON actions and some XML-extracted ones.
	ResponseExtract *ExtractSpec `json:"response_extract,omitempty"`
	// Disabled quarantines a single action without touching the rest of
	// the toolbox: the renderer drops it from the catalog (collapsed OR
	// expanded) and the dispatcher refuses to run it. Set it when one
	// action is broken so the other actions keep serving live instead of
	// the record being all-or-nothing. Re-enable via tool_def update
	// (actions=[{name, disabled:false}]).
	Disabled bool `json:"disabled,omitempty"`
}

// TempTool is a runtime-defined tool created via create_temp_tool or
// create_api_tool. The LLM sees it in its tool catalog like any other
// tool, but it lives only for the current session unless persisted.
//
// Two execution modes:
//
//   - shell (default): CommandTemplate is a shell command run through
//     RunSandboxedShell. Placeholders are POSIX-shell-quoted. Requires
//     CapExecute.
//
//   - api: CommandTemplate is reinterpreted as the URL template of an
//     HTTP call against the named Credential. Placeholders in the URL
//     are URL-path-encoded; placeholders in BodyTemplate are JSON-encoded.
//     The auth header is injected server-side from the credential's
//     encrypted secret. Requires CapNetwork.
type TempTool struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Params      map[string]ToolParam `json:"params,omitempty"`
	Required    []string             `json:"required,omitempty"`
	// Category is the grouping label the tool CLAIMS (see Tool.Category). It's
	// the persisted counterpart of the runtime Tool.Category and is copied onto
	// the runtime def in agentToolFromTemp. Because it lives on the per-user
	// tool record, a user setting it touches only their own tool — this is why
	// grouping needs no separate per-user group store. Empty = fall back to the
	// legacy ToolGroup.Members mapping, then the capability label. The matching
	// ToolGroup (by Name) is the registry that supplies the LLM-facing group
	// description.
	Category string `json:"category,omitempty"`
	// Locked freezes the tool's DEFINITION: while true, the AI's tool_def
	// cannot update, delete, or overwrite it — the user must unlock it in
	// Extensions › Tools first. Running the tool is unaffected. A
	// user-only control (the AI can't set or clear it), the tool analog of
	// SecureCredential.Secured / the credential re-draft guard.
	Locked bool `json:"locked,omitempty"`
	// Disabled hides the WHOLE tool from every agent's runtime catalog (not
	// dispatchable, not offered to the LLM) while keeping its definition in
	// the user's pool. For diagnostic / Builder-only tools the user doesn't
	// want cluttering agents. Distinct from TempToolAction.Disabled (which
	// quarantines ONE action of a toolbox) and AgentRecord.DisabledPersistentTools
	// (per-agent opt-out) — this is a global, user-managed on/off from Extensions › Tools.
	Disabled bool `json:"disabled,omitempty"`
	// BuilderOnly exposes the tool to the Builder authoring agent only — every
	// OTHER agent's runtime catalog omits it. For diagnostic / authoring-support
	// tools the user wants available while building but NOT surfaced to Chat,
	// Research, etc. Distinct from Disabled (off everywhere including Builder).
	BuilderOnly bool `json:"builder_only,omitempty"`
	// BoundOnly hides the tool from every agent's catalog while leaving it
	// available wherever something BINDS it by name — a servitor appliance's
	// toolset today, another binder later.
	//
	// The case it exists for: a set of read tools for one service, authored so
	// a servitor system can be investigated through them, which have no business
	// appearing in every chat the user has. Disabled turns the tool off
	// everywhere including its binder; BuilderOnly reserves it for authoring.
	// This one says "reachable only where it was deliberately attached", which
	// is neither of those.
	//
	// It is a VISIBILITY rule, not an access-control one. A binding is what
	// grants use, and the binding already had to be approved; this only stops
	// the tool being offered to agents that never asked for it.
	//
	// Builder still sees it, like the two above and for the same reason: it has
	// to load, RUN and fix these, and a tool it cannot run is a tool nobody can
	// repair. A set of read tools authored for one system is precisely what
	// somebody asks Builder to fix.
	BoundOnly bool `json:"bound_only,omitempty"`
	// Template records the tool template that authored this tool (provenance),
	// so it can be reconfigured through the same template later — the tool-side
	// analog of Connector.Template. Empty for hand-authored tools.
	Template string `json:"template,omitempty"`
	// CommandTemplate is the body. Interpreted as a shell command in
	// shell mode and as a URL template in api mode. `{arg_name}`
	// placeholders are substituted with the args at dispatch time
	// (quoting/encoding rules depend on Mode).
	CommandTemplate string `json:"command_template"`
	// Mode picks the execution backend. Empty defaults to shell for
	// backward-compatibility with TempTool records written before this
	// field existed.
	Mode string `json:"mode,omitempty"`
	// Credential is the registered secure-API credential name this
	// tool dispatches through. Used in api mode only.
	Credential string `json:"credential,omitempty"`
	// Method is the HTTP method for api mode (default GET). Any method is
	// allowed, including non-standard ones like CalDAV's REPORT.
	Method string `json:"method,omitempty"`
	// BodyTemplate is an optional request body template for api mode.
	// `{arg_name}` placeholders are JSON-encoded UNLESS ContentType marks the
	// body as non-JSON (see ContentType) — then they substitute as raw text.
	BodyTemplate string `json:"body_template,omitempty"`
	// ContentType overrides the request body's Content-Type for api mode.
	// Empty defaults to application/json (placeholders JSON-encoded, body
	// validated as JSON). A NON-JSON value (e.g. "application/xml" for
	// CalDAV/SOAP) switches the body to RAW substitution: `{arg}` placeholders
	// are inserted verbatim (no JSON quoting), the body is NOT JSON-validated,
	// and the header is sent as declared. This is what lets an XML/text API be
	// a first-class api-mode tool instead of forcing a shell detour.
	ContentType string `json:"content_type,omitempty"`
	// UploadParam names the tool parameter that carries a FILE to send, turning
	// this api tool into a multipart uploader. The model passes a
	// workspace-relative filename in that parameter; the dispatch reads it and
	// streams it as the form's file part. Every OTHER declared parameter is
	// sent alongside as a plain form field, so BodyTemplate is unused here —
	// multipart is the body.
	//
	// This is what makes "upload a file" DECLARABLE. Before it, a file could
	// only leave through Go written for one endpoint, which is how the
	// transcription client came to bypass the governed dispatch entirely.
	UploadParam string `json:"upload_param,omitempty"`
	// UploadFormField is the multipart field name the file goes in. Endpoints
	// disagree — OpenAI-compatible transcription wants "file", ComfyUI wants
	// "image" — and getting it wrong is a 4xx with no useful message, so it is
	// declared rather than guessed. Default "file".
	UploadFormField string `json:"upload_form_field,omitempty"`
	// Trial marks a tool authored mid-conversation that the user has not
	// confirmed. It is a real tool on a real agent — callable, visible, and
	// governed by the normal access controls — the flag only records that
	// nobody has vouched for it yet, so a UI can badge it and a cleanup can
	// reap the ones that were never kept.
	//
	// This replaces the session-scoped tool pool. Ephemerality is an ATTRIBUTE
	// of a tool, not a separate storage scope: the pool version produced an
	// owner-less global table, tools invisible to the person who owned them, a
	// cross-user delete, and a shadow-reconciliation pass on every read.
	Trial bool `json:"trial,omitempty"`

	// TrialSince is when the tool was marked Trial — the clock the reaper reads.
	// Zero on a confirmed tool. Stamped at attach time rather than derived from
	// the agent record's mtime, because editing an agent for any reason would
	// otherwise reset every unconfirmed tool's age.
	TrialSince time.Time `json:"trial_since,omitempty"`

	// Headers are extra request headers sent with an api-mode call, as
	// {name: value}. Some protocols carry REQUIRED semantics in a header
	// rather than the body or the URL: a CalDAV calendar-query REPORT (or a
	// PROPFIND) needs "Depth: 1" to match the collection's CHILD resources —
	// without it the server applies the query at Depth 0, matches nothing,
	// and returns a well-formed but EMPTY 207 multistatus. That reads as
	// "the call worked, the calendar is empty," which is how a broken read
	// tool passes verification and ships.
	//
	// The sandbox hook has always accepted headers (gohort.fetch_via(...,
	// headers={"Depth": "1"})), so a shell tool could do this and an api
	// tool could not — the asymmetry this field closes.
	//
	// Auth headers are ignored here (Authorization / Proxy-Authorization are
	// dropped at the request-build layer): auth comes from the credential, so
	// a tool can never smuggle its own.
	Headers map[string]string `json:"headers,omitempty"`
	// ResponsePipe is an optional shell command for api mode that
	// receives the raw API response on stdin and emits the LLM-visible
	// result on stdout. Runs in a tight sandbox (no network, no
	// writable filesystem, /tmp tmpfs only) so it can use jq, awk,
	// grep, sed, etc. to filter / reshape responses before they reach
	// the LLM's context. Empty = LLM sees the raw response unchanged.
	// Adding a pipe upgrades the wrapper tool's required caps to
	// include CapExecute.
	ResponsePipe string `json:"response_pipe,omitempty"`
	// ResponseExtract, when set, parses an XML api response into JSON via a
	// declarative, namespace-agnostic spec (see ExtractSpec) — so an XML/WebDAV/
	// CalDAV endpoint returns structured JSON directly, with no hand-written
	// ElementTree/xpath (which small models cannot get right). Runs on a 2xx
	// response body; a response_pipe, if also set, then projects the extracted
	// JSON (XML → JSON → jq).
	ResponseExtract *ExtractSpec `json:"response_extract,omitempty"`
	// Expand (toolbox mode only) surfaces each action as its own
	// top-level `<toolbox>_<action>` tool instead of one collapsed
	// action="<sub>" catalog entry. The record, credential, artifact,
	// and governance stay a single entity — expansion is purely how the
	// tool is PRESENTED to the LLM. Off by default (collapsed group);
	// opt in per tool via tool_def update(name, expand:true). Expanded,
	// a broken action fixes/quarantines as one named tool instead of an
	// opaque bundle. Ignored for non-toolbox modes.
	Expand bool `json:"expand,omitempty"`
	// Recipe is a declarative manifest of files that get deployed
	// into a fresh sandbox dir on every dispatch. Replaces the older
	// tar.gz-snapshot model: the recipe is human-readable, diffable,
	// editable, and rebuilds identically every time. Empty for self-
	// contained shell tools (e.g. "uname -a") whose CommandTemplate
	// references no files.
	Recipe []RecipeFile `json:"recipe,omitempty"`

	// ScriptBody is the source of a script shipped alongside the
	// tool — Python, Bash, jq, awk, whatever. Stored IN the tool
	// record (DB-side) so the script survives workspace wipes:
	// at every dispatch the framework idempotently ensures
	// {workspace_dir}/<ScriptName> matches ScriptBody, writing it
	// if missing or stale. Without this, deleting the workspace
	// dir silently breaks tools whose command_template references
	// a script_body file that was only ever on disk.
	//
	// Distinct from Recipe: Recipe deploys files into an EPHEMERAL
	// per-dispatch tmpdir; ScriptBody persists into sess.WorkspaceDir
	// so it shares the workspace with other tools (find_image's
	// outputs, etc.). Use ScriptBody for "this tool needs my
	// script in the active workspace"; use Recipe for "this tool
	// needs an isolated tmpdir staged from scratch."
	ScriptBody string `json:"script_body,omitempty"`

	// ScriptName is the LLM-facing filename — what the LLM
	// originally chose (or defaulted to) at tool authoring time.
	// Appears verbatim in CommandTemplate; the LLM sees this when
	// reading back its own tool record. Defaults to "script.py".
	ScriptName string `json:"script_name,omitempty"`

	// CanonicalScriptName is the framework-assigned on-disk
	// filename: "<tool_name>_<content_hash>.<ext>". Always distinct
	// per (tool, content) so collisions are impossible. Hidden from
	// the LLM's view of the tool record; the dispatcher translates
	// every CommandTemplate reference to ScriptName into a reference
	// to CanonicalScriptName at dispatch time.
	CanonicalScriptName string `json:"canonical_script_name,omitempty"`

	// WorkspaceFiles carries HELPER files a shell tool's primary script
	// depends on — a Python module the script imports, a bash file it
	// sources, a lookup table it reads. Like ScriptBody they persist IN
	// the tool record (so the tool is self-contained: it exports whole
	// and survives a workspace wipe), but plural: ScriptBody is the one
	// entry script the command runs, WorkspaceFiles are everything it
	// pulls in. Each is redeployed to {workspace_dir}/<Path> at dispatch
	// under its LITERAL path (NOT canonicalized — the primary imports it
	// by that exact name). Empty for single-file tools. This is the
	// "a tool may bundle a few scripts underneath" case; distinct from
	// Recipe, which stages an ISOLATED tmpdir from scratch rather than
	// sharing the session workspace.
	WorkspaceFiles []RecipeFile `json:"workspace_files,omitempty"`

	// StatePath, when set, names a relative subdirectory inside the
	// deployed sandbox whose contents are preserved across dispatches.
	// Use for tools that legitimately need runtime state (counters,
	// accumulating logs, cached lookup DBs). Everything else outside
	// state_path is rebuilt fresh from the recipe each fire.
	StatePath string `json:"state_path,omitempty"`

	// HookCapabilities lists the SandboxHook methods this tool's
	// script is allowed to invoke. When non-empty, the dispatcher
	// starts a per-dispatch UDS hook server inside the workspace,
	// exposes its path via the GOHORT_HOOK_PATH env var, and the
	// shipped `gohort.py` helper module lets the script call back
	// into gohort for those narrow operations (fetch, log, secret,
	// fetch_via) WITHOUT opening the sandbox's network namespace.
	// Empty list means no hook is wired — zero surface area, same
	// posture as before the hook existed. Recognized methods:
	// "fetch", "log", "secret:<name>", "fetch_via:<name>".
	HookCapabilities []string `json:"hook_capabilities,omitempty"`

	// RawNetwork, when true, leaves the sandbox's network namespace
	// JOINED with the host's (no --unshare-net). The default is
	// false: shell-mode + persistent-mode tools run with the network
	// namespace cut, so a script that does urllib.request /
	// socket.connect / curl from inside the sandbox fails. Such
	// tools must declare hook_capabilities=["fetch"] and call
	// gohort.fetch(...) instead — gohort proxies HTTP on their
	// behalf with auditing.
	//
	// Reserve RawNetwork=true for the narrow cases where the tool
	// genuinely needs raw TCP from the sandbox (persistent-mode
	// psql / redis-cli / ssh-like REPLs that connect to a
	// non-HTTP protocol; legacy shell tools that haven't been
	// re-authored yet). Every new shell-mode tool that does HTTP
	// should use the hook, not RawNetwork.
	//
	// The session-level NetworkConnector still acts as a hard
	// upper bound — RawNetwork=true on a tool dispatched within a
	// private-mode session still gets no network. RawNetwork only
	// matters when the session would otherwise permit it.
	RawNetwork bool `json:"raw_network,omitempty"`

	// --- Pipeline-mode fields (Mode == "pipeline") ----------------------
	// Pipeline-mode tools are mini-agents exposed as a single tool.
	// On dispatch the framework spawns a sub-agent loop via the host
	// session's SubAgentRunner, builds the system prompt from
	// PipelinePrompt, gives the sub-agent PipelineTools as its tool
	// catalog, and returns the final text as the tool's result. Lets
	// admins compose multi-step flows ("research a company: search +
	// fetch + summarize") as a single LLM-callable tool without
	// writing code.

	// PipelinePrompt is the system prompt for the sub-agent loop.
	// `{arg_name}` placeholders are substituted with the dispatch args
	// (string-cast, no quoting needed since this lands in a prompt).
	PipelinePrompt string `json:"pipeline_prompt,omitempty"`

	// PipelineTools lists the tool names the sub-agent may call.
	// Subset of the parent session's catalog. Recursive pipeline calls
	// (a pipeline tool calling another pipeline tool) work but are
	// capped by the host runner to prevent infinite descent.
	PipelineTools []string `json:"pipeline_tools,omitempty"`

	// PipelineMaxRounds caps the sub-agent loop. Default 6 — enough
	// for a small multi-step flow without runaway cost.
	PipelineMaxRounds int `json:"pipeline_max_rounds,omitempty"`

	// Cache, when non-nil, enables persistent memoization of this
	// tool's result text keyed by the rendered Cache.Key (defaults to
	// the SHA-256 of all args). The canonical use is wrapping an
	// expensive remote fetch (download_video, transcribe_audio,
	// document conversion) so a re-call with the same args returns
	// the prior result instead of re-spending bandwidth or compute.
	// Cache entries are stored in RootDB; TTL bounds entry lifetime;
	// InvalidateWhen lets a side-effect-producing tool say "drop the
	// cache if the workspace artifact I previously wrote is gone."
	Cache *TempToolCache `json:"cache,omitempty"`

	// PipelineSteps is the structured alternative to PipelinePrompt.
	// When non-empty, the pipeline runs DETERMINISTICALLY: each step's
	// tool is called in order with its (template-substituted) args, no
	// sub-agent / no per-step LLM. Cheap, fast, predictable — right for
	// linear chains like "search → fetch → summarize" where no
	// reasoning is needed between steps. PipelinePrompt is ignored when
	// PipelineSteps is set. Mutually exclusive in practice: set one or
	// the other based on whether the chain needs adaptive logic.
	PipelineSteps []PipelineStep `json:"pipeline_steps,omitempty"`

	// --- Persistent-shell-mode fields (Mode == "persistent") ------------
	// Persistent shells host a long-lived process inside the sandbox
	// (bash, psql, ssh, etc.) and accept commands across multiple LLM
	// turns. The bwrap process AND the inner shell both stay alive
	// from the first send until close or session end. State (env vars,
	// working directory, mounted FS, login session) persists between
	// calls.

	// PersistentOpenCmd is the shell command that launches the long-
	// lived process inside the sandbox. Examples: "bash", "psql -h
	// dev-db -U app", "ssh user@host" (when keys are reachable). The
	// command runs through the same bwrap as one-shot shell mode but
	// the bwrap process itself is kept alive across tool calls.
	PersistentOpenCmd string `json:"persistent_open_cmd,omitempty"`

	// PersistentPromptPattern is a regex matched against the trailing
	// bytes of the shell's output to decide when "the shell is ready
	// for the next command." When the pattern matches, the send action
	// returns (complete=true). Default patterns are mode-dependent but
	// authors should set this explicitly for known shells:
	//   bash:  `[\$#] $`
	//   psql:  `\w+=> $`
	//   ssh:   depends on the remote shell's PS1
	PersistentPromptPattern string `json:"persistent_prompt_pattern,omitempty"`

	// PersistentSendTimeoutSec caps how long a send action waits for
	// the prompt to reappear before returning with complete=false (the
	// LLM should call read for more). Default 5s when unset.
	PersistentSendTimeoutSec int `json:"persistent_send_timeout_sec,omitempty"`

	// --- Toolbox-mode fields (Mode == TempToolModeToolbox) ----------------
	// Toolbox-mode tools bundle multiple api-mode endpoints under one
	// tool name. The catalog shows ONE entry; the LLM picks a sub-
	// endpoint via action="<name>". Same shape as the framework's
	// built-in grouped tools (tool_def / agents / workspace). Useful
	// when wrapping an API surface with several related endpoints
	// (GitHub: get_user / get_repo / list_issues; Stripe: list_charges /
	// create_invoice / etc.) — keeps the catalog clean and shares one
	// Credential across all endpoints.
	Actions []TempToolAction `json:"actions,omitempty"`
}

// TempToolCache is the declarative memoization spec attached to a
// TempTool. All fields optional; an empty struct caches everything
// forever under user scope, which is fine for tools where every
// argument combination genuinely produces the same result.
type TempToolCache struct {
	// Key is a {param}-templated string that produces the cache key.
	// Empty defaults to a stable hash of all args, so semantically-
	// identical calls land on the same entry regardless of arg order.
	Key string `json:"key,omitempty"`

	// TTL is a duration string (e.g. "30d", "12h", "10m"). Empty =
	// no expiry (entry lives until an InvalidateWhen check drops it
	// or the table is wiped).
	TTL string `json:"ttl,omitempty"`

	// Scope determines the cache partition. Choices:
	//   "user"    — keyed by sess.Username (default; cross-session
	//               dedup for one user).
	//   "session" — keyed by sess.ChatSessionID (per-conversation).
	//   "global"  — shared across all users / sessions (use only
	//               when the result is content-addressable and
	//               privacy-safe, e.g. public URL → video bytes).
	// When the required identifier is missing on the session
	// (sessionless run, anonymous CLI invocation, etc.) caching is
	// silently skipped rather than broadening to a less-restrictive
	// scope.
	Scope string `json:"scope,omitempty"`

	// InvalidateWhen is a list of post-hit verification checks. Each
	// string has the form "kind:expression". Today one kind is
	// supported:
	//   "file_exists:<path-template>"  — the rendered path must
	//   exist on disk (templated against args + {workspace_dir}).
	// Use to keep the cache honest when the tool's side effect (a
	// workspace file) might have been reaped between the original
	// run and the hit.
	InvalidateWhen []string `json:"invalidate_when,omitempty"`
}

// PipelineStep is one rung of a structured (deterministic) pipeline.
// Args values are strings or other JSON-encodable values; string args
// undergo template substitution before the tool fires:
//
//   - {param_name}    → value of the caller-supplied parameter
//   - $N              → full output of step N (1-indexed)
//   - $N.field.path   → JSON field path into step N's output
//     (returns empty string if step N's output isn't
//     JSON or the path doesn't resolve)
//
// Optional Name lets later steps reference outputs by name instead
// of by index: $name.field works the same as $N.field. Mostly a
// readability convenience for longer pipelines.
type PipelineStep struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
	Name string         `json:"name,omitempty"`
}

// RecipeFile is one file in a TempTool's deployment recipe. Path is
// relative to the deployed sandbox dir. Mode defaults to 0700 if
// unset (sufficient for scripts and data files).
type RecipeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode,omitempty"`
}

// AppendTempTool registers a temp tool on the session. Returns an error
// if the name conflicts with an existing temp tool (caller is expected
// to have already validated against the static catalog).
func (s *ToolSession) AppendTempTool(t *TempTool) error {
	if s == nil || t == nil {
		return fmt.Errorf("nil session or tool")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.TempTools {
		if existing.Name == t.Name {
			return fmt.Errorf("temp tool %q already exists in this session", t.Name)
		}
	}
	s.TempTools = append(s.TempTools, t)
	return nil
}

// HasTempTool returns true when a temp tool with the given name is
// already registered on this session. Callers that load temp tools
// from multiple layered sources (e.g. user pool + agent-scoped +
// session drafts) use this to skip a redundant append silently
// instead of trying to append and treating the "already exists"
// error as a real failure.
func (s *ToolSession) HasTempTool(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.TempTools {
		if existing.Name == name {
			return true
		}
	}
	return false
}

// LookupTempTool returns the LIVE temp-tool record registered on the
// session under name, or nil when absent. Returns the actual pointer (not
// a copy) so a caller dispatching a tool sees mutations applied mid-turn by
// tool_def update/recreate — the fix for a stale, frozen turn-start
// snapshot continuing to dispatch after the record changed. Read-only use
// only; do not mutate the returned record without holding the session lock.
func (s *ToolSession) LookupTempTool(name string) *TempTool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.TempTools {
		if t.Name == name {
			return t
		}
	}
	return nil
}

// CredentialDenied reports whether the running agent is barred from
// dispatching through the named SecureAPI credential (its scope pill turned
// this credential OFF for the agent). nil-safe; false when unrestricted.
func (s *ToolSession) CredentialDenied(name string) bool {
	if s == nil || len(s.DeniedCredentials) == 0 {
		return false
	}
	return s.DeniedCredentials[name]
}

// RemoveTempTool deletes a temp tool from the session by name. Returns
// true if a tool was actually removed.
func (s *ToolSession) RemoveTempTool(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.TempTools {
		if t.Name == name {
			s.TempTools = append(s.TempTools[:i], s.TempTools[i+1:]...)
			return true
		}
	}
	return false
}

// CopyTempTools returns a snapshot of the session's temp tools. Used by
// the agent loop's DynamicTools hook to convert them to AgentToolDef
// without holding the lock during the conversion.
func (s *ToolSession) CopyTempTools() []*TempTool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.TempTools) == 0 {
		return nil
	}
	out := make([]*TempTool, len(s.TempTools))
	copy(out, s.TempTools)
	return out
}
