package core

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// SetupSection represents a configuration section contributed by an app.
// Apps register these in init() so their settings appear in --setup
// without the framework needing to know about them.
type SetupSection struct {
	Name  string                     // display name for the setup menu
	Order int                        // display order (lower = earlier; framework uses 0-99, apps use 100+)
	Build func(db Database) *Options // build the interactive submenu
	Save  func(db Database)          // persist values after user exits setup
}

var (
	registryMu              sync.Mutex
	registeredAgents        []Agent
	registeredApps          []Agent
	registeredAdminAgents   []Agent
	registeredChatTools     []ChatTool
	registeredSetupSections []SetupSection
)

// RegisterAgent registers a standard agent.
func RegisterAgent(agent Agent) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registeredAgents = append(registeredAgents, agent)
}

// RegisterApp registers an app (structured pipeline with web UI).
func RegisterApp(agent Agent) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registeredApps = append(registeredApps, agent)
}

// RegisterAdminAgent registers an admin-only agent.
func RegisterAdminAgent(agent Agent) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registeredAdminAgents = append(registeredAdminAgents, agent)
}

// RegisterChatTool registers a chat tool. Also invalidates the
// classifier tool-index so the new tool enters the per-turn
// embedding-match pool on its next lookup. Most tools register via
// package init() (before the index has been built); this matters
// most for runtime registrations (e.g. mid-session temp-tool
// approval) so the new capability is immediately discoverable.
func RegisterChatTool(tool ChatTool) {
	registryMu.Lock()
	registeredChatTools = append(registeredChatTools, tool)
	registryMu.Unlock()
	InvalidateToolIndex()
}

// RegisterSetupSection registers a configuration section contributed by an app.
func RegisterSetupSection(s SetupSection) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registeredSetupSections = append(registeredSetupSections, s)
}

// RegisteredSetupSections returns all registered setup sections sorted by Order.
func RegisteredSetupSections() []SetupSection {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := make([]SetupSection, len(registeredSetupSections))
	copy(out, registeredSetupSections)
	sort.Slice(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// RegisteredAgents returns all registered standard agents.
func RegisteredAgents() []Agent { return registeredAgents }

// RegisteredApps returns all registered apps.
func RegisteredApps() []Agent { return registeredApps }

// RegisteredAdminAgents returns all registered admin agents.
func RegisteredAdminAgents() []Agent { return registeredAdminAgents }

// RegisteredChatTools returns all registered chat tools.
func RegisteredChatTools() []ChatTool { return registeredChatTools }

// LookupChatTool returns the registered chat tool with the given name,
// or nil + false. Static-registry only; dynamically generated tools
// (e.g. secure-API per-credential call_<name> tools) are not in this
// registry — callers that need to handle those route on the name
// prefix themselves.
func LookupChatTool(name string) (ChatTool, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, t := range registeredChatTools {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}

// FindAgent looks up a registered agent or app by name.
// Prefers agents that have a DB set (web mode initializes web app DBs, not CLI app DBs).
func FindAgent(name string) (Agent, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()

	var fallback Agent
	check := func(a Agent) bool {
		if a.Name() != name {
			return false
		}
		if a.Get().DB != nil {
			return true
		}
		if fallback == nil {
			fallback = a
		}
		return false
	}

	for _, a := range registeredAgents {
		if check(a) {
			return a, true
		}
	}
	for _, a := range registeredApps {
		if check(a) {
			return a, true
		}
	}
	for _, a := range registeredAdminAgents {
		if check(a) {
			return a, true
		}
	}
	webAppMu.Lock()
	defer webAppMu.Unlock()
	for _, wa := range registeredWebApps {
		if a, ok := wa.(Agent); ok && check(a) {
			return a, true
		}
	}
	if fallback != nil {
		return fallback, true
	}
	return nil, false
}

// FindChatTool looks up a registered chat tool by name.
func FindChatTool(name string) (ChatTool, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, t := range registeredChatTools {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}

// ChatToolToAgentToolDef converts a ChatTool into an AgentToolDef for use
// with RunAgentLoop. If the tool implements ConfirmableTool, the NeedsConfirm
// flag is set accordingly. If it implements CapabilityTool, declared caps
// flow into Tool.Caps for AllowedCaps filtering at the agent-loop level.
func ChatToolToAgentToolDef(ct ChatTool) AgentToolDef {
	confirm := false
	if c, ok := ct.(ConfirmableTool); ok {
		confirm = c.NeedsConfirm()
	}
	var caps []Capability
	if c, ok := ct.(CapabilityTool); ok {
		caps = c.Caps()
	}
	trusted := false
	if to, ok := ct.(TrustedOutputTool); ok {
		trusted = to.TrustedOutput()
	}
	return AgentToolDef{
		Tool: Tool{
			Name:          ct.Name(),
			Description:   ct.Desc(),
			Parameters:    ct.Params(),
			Caps:          caps,
			TrustedOutput: trusted,
		},
		Handler:      ct.Run,
		NeedsConfirm: confirm,
	}
}

// GetAgentTools looks up registered chat tools by name and returns them as
// AgentToolDefs. Returns an error if any named tool is not found.
func GetAgentTools(names ...string) ([]AgentToolDef, error) {
	var tools []AgentToolDef
	for _, name := range names {
		ct, ok := FindChatTool(name)
		if !ok {
			return nil, fmt.Errorf("tool %q not found in registry", name)
		}
		tools = append(tools, ChatToolToAgentToolDef(ct))
	}
	return tools, nil
}

// snapshotWorkspaceFiles returns a set of filenames currently in
// the session's workspace root. Used by the tool-call wrapper to
// detect new files written during the dispatch. Best-effort:
// returns an empty map on any error.
func snapshotWorkspaceFiles(sess *ToolSession) map[string]bool {
	if sess == nil || sess.WorkspaceDir == "" {
		return nil
	}
	entries, err := os.ReadDir(sess.WorkspaceDir)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Skip dotfiles + framework bookkeeping (workspaces, recipes).
		n := e.Name()
		if strings.HasPrefix(n, ".") || strings.HasPrefix(n, "_") {
			continue
		}
		out[n] = true
	}
	return out
}

// diffWorkspaceFiles returns filenames present in the current
// workspace that weren't in the `before` snapshot. Used to detect
// what a tool dispatch wrote, so the framework can tell the LLM
// what's available to attach.
func diffWorkspaceFiles(sess *ToolSession, before map[string]bool) []string {
	if sess == nil || sess.WorkspaceDir == "" {
		return nil
	}
	after := snapshotWorkspaceFiles(sess)
	if len(after) == 0 {
		return nil
	}
	var added []string
	for name := range after {
		if !before[name] {
			added = append(added, name)
		}
	}
	return added
}

// ChatToolToAgentToolDefWithSession converts a ChatTool into an AgentToolDef,
// binding a ToolSession so that tools implementing SessionChatTool receive it.
func ChatToolToAgentToolDefWithSession(ct ChatTool, sess *ToolSession) AgentToolDef {
	confirm := false
	if c, ok := ct.(ConfirmableTool); ok {
		confirm = c.NeedsConfirm()
	}
	var handler ToolHandlerFunc
	if sess != nil {
		if sct, ok := ct.(SessionChatTool); ok {
			handler = func(args map[string]any) (string, error) {
				return sct.RunWithSession(args, sess)
			}
		}
	}
	if handler == nil {
		handler = ct.Run
	}
	// Framework-level attachment-success signal. Any tool that
	// produces attachments (calls sess.AppendImage / AppendVideo /
	// AppendFile) gets its result text appended with an explicit
	// "FILE WAS ATTACHED" notice. This gives worker LLMs an
	// unambiguous, tool-agnostic success signal so they don't
	// chain a second attach call wondering if the first worked.
	// The signal is computed by diffing the session's attachment
	// counts BEFORE vs AFTER the handler runs — no per-tool
	// changes needed; any future tool that attaches gets the
	// same notice for free.
	if sess != nil {
		base := handler
		handler = func(args map[string]any) (string, error) {
			imgBefore := len(sess.Images)
			vidBefore := len(sess.Videos)
			fileBefore := len(sess.Files)
			// Workspace file snapshot before the call. Lets us
			// detect new files the tool wrote (e.g. a Builder-
			// authored shell tool's output) and tell the LLM
			// about them explicitly. Without this, tools that
			// return JSON or other unfriendly formats leave the
			// LLM guessing at filenames that may or may not
			// exist in the workspace.
			wsBefore := snapshotWorkspaceFiles(sess)
			out, err := base(args)
			// A result that is purely a data:image/...;base64,... URI is a
			// vision attachment, not text. Decode it into the session's
			// image list so vision LLMs see it, and replace the unwieldy
			// base64 with a short note. This lets string-only tools —
			// desktop tools over the WS bridge (screenshot), MCP image
			// tools, etc. — return an image despite the (string, error)
			// contract. The imgDelta logic below then emits the standard
			// attachment notice for free.
			if err == nil {
				if b64, ok := dataURIImage(out); ok {
					sess.AppendImage(b64)
					out = "Image captured and attached."
				}
			}
			imgDelta := len(sess.Images) - imgBefore
			vidDelta := len(sess.Videos) - vidBefore
			fileDelta := len(sess.Files) - fileBefore
			if imgDelta > 0 || vidDelta > 0 || fileDelta > 0 {
				var notice strings.Builder
				notice.WriteString("\n\n[ATTACHMENT REQUEST COMPLETED]\n")
				if imgDelta > 0 {
					fmt.Fprintf(&notice, "- %d image(s) attached.\n", imgDelta)
				}
				if vidDelta > 0 {
					fmt.Fprintf(&notice, "- %d video/audio file(s) attached.\n", vidDelta)
				}
				if fileDelta > 0 {
					fmt.Fprintf(&notice, "- %d file(s) attached.\n", fileDelta)
				}
				notice.WriteString("The user will receive the file(s) with your reply.")
				out = out + notice.String()
			}
			// New-file detection. Tools that write to the
			// session workspace produce files the LLM can attach
			// via workspace(action="attach"). We diff the
			// workspace before/after the call and tell the LLM
			// what's new + how to deliver.
			//
			// Diagnostic value: when a script CLAIMS to write a
			// file but actually wrote to /tmp (ephemeral) or an
			// unmounted path, the diff shows nothing new and the
			// LLM gets a clear "no new files appeared in your
			// workspace" signal instead of chasing phantom paths.
			if newFiles := diffWorkspaceFiles(sess, wsBefore); len(newFiles) > 0 {
				var notice strings.Builder
				notice.WriteString("\n\n[WORKSPACE FILES CREATED]\n")
				for _, f := range newFiles {
					fmt.Fprintf(&notice, "- %s\n", f)
				}
				notice.WriteString("Use workspace(action=\"attach\", path=\"<filename>\", cleanup=true) to deliver any of these to the user.")
				out = out + notice.String()
			}
			return out, err
		}
	}
	var caps []Capability
	if c, ok := ct.(CapabilityTool); ok {
		caps = c.Caps()
	}
	singleFire := false
	if sf, ok := ct.(SingleFireTool); ok {
		singleFire = sf.SingleFirePerBatch()
	}
	trusted := false
	if to, ok := ct.(TrustedOutputTool); ok {
		trusted = to.TrustedOutput()
	}
	return AgentToolDef{
		Tool: Tool{
			Name:          ct.Name(),
			Description:   ct.Desc(),
			Parameters:    ct.Params(),
			Caps:          caps,
			TrustedOutput: trusted,
		},
		Handler:            handler,
		NeedsConfirm:       confirm,
		SingleFirePerBatch: singleFire,
	}
}

// dataURIImage returns the base64 payload of a tool result that is
// nothing but a data:image/<type>;base64,<data> URI, and true. Any other
// result returns ("", false). Used so a string-only tool can hand back
// an image that becomes a vision attachment (see the wrapper above).
func dataURIImage(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "data:image/") {
		return "", false
	}
	i := strings.Index(s, ";base64,")
	if i < 0 {
		return "", false
	}
	b64 := s[i+len(";base64,"):]
	if b64 == "" {
		return "", false
	}
	return b64, true
}

// GetAgentToolsWithSession looks up registered chat tools by name and returns
// them as AgentToolDefs bound to sess. Session-aware tools (SessionChatTool)
// receive sess on each call; stateless tools fall back to Run as usual.
func GetAgentToolsWithSession(sess *ToolSession, names ...string) ([]AgentToolDef, error) {
	var tools []AgentToolDef
	var secureCache []AgentToolDef // built lazily, only when a name isn't in the static registry
	secureBuilt := false
	for _, name := range names {
		if ct, ok := FindChatTool(name); ok {
			tools = append(tools, ChatToolToAgentToolDefWithSession(ct, sess))
			continue
		}
		// Fallback: the dynamic secure-API tools (call_<credential>)
		// are generated per-session by Secure().BuildTools(sess) and
		// never registered globally. Pipeline steps + other resolvers
		// that look these up by name would otherwise fail with
		// "not found in registry" even though the credential exists
		// and the calling context (e.g. Builder) has the tool in its
		// effective catalog. Per-caller gating (PipelineTools list,
		// agent.AllowedTools, etc.) stays the responsibility of the
		// caller — this is just registry resolution.
		if !secureBuilt {
			secureCache = Secure().BuildTools(sess)
			secureBuilt = true
		}
		// Legacy alias: pre-0.3.1 the secure-API tools were named
		// call_<credential>. They're now fetch_url_<credential> for
		// shape parity with fetch_url. Old AllowedTools lists, old
		// pipeline_steps, and authored temp tools that reference the
		// legacy name still resolve here so the rename doesn't break
		// existing deployments. Drop this fallback when callers
		// have all been migrated.
		lookupName := name
		if strings.HasPrefix(lookupName, "call_") && lookupName != "call_no_auth" {
			lookupName = "fetch_url_" + strings.TrimPrefix(lookupName, "call_")
		}
		var found *AgentToolDef
		for i := range secureCache {
			if secureCache[i].Tool.Name == lookupName {
				found = &secureCache[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("tool %q not found in registry", name)
		}
		tools = append(tools, *found)
	}
	return tools, nil
}

// SecureToolLegacyAlias translates a legacy call_<credential> tool
// name to its current fetch_url_<credential> form. Returns name
// unchanged for anything that isn't a legacy call_* reference, or
// for the special call_no_auth case (which no longer has a direct
// tool — fetch_url covers it). Lookup paths that accept tool names
// from authored records / persisted AllowedTools should pass them
// through this before matching against the live catalog.
func SecureToolLegacyAlias(name string) string {
	if strings.HasPrefix(name, "call_") && name != "call_no_auth" {
		return "fetch_url_" + strings.TrimPrefix(name, "call_")
	}
	return name
}

// BlockedTools is the set of tool names that are never exposed in chat,
// regardless of mode. Tools that perform real-world side effects are
// blocked from the testing UI to keep it sandboxed.
//
// `workspace` is intentionally NOT blocked here: chat auto-mints a
// per-user workspace dir (EnsureWorkspaceDir) so the LLM rarely needs
// the lifecycle actions (create/use/pin/etc.), but the same grouped
// tool also exposes the file-ops surface (ls/cat/write/rm/run) which
// IS needed for any script-wrapping flow. Blocking the whole tool to
// hide the lifecycle would also strip the file ops; the lifecycle
// noise is a tolerable cost.
var BlockedTools = map[string]bool{
	"run_command": true, // shell execution — risky in a web UI
	// send_email is registered and available — its side effects are
	// gated by the per-agent allowlist + capability tiers like every
	// other tool with real-world consequences (download_video,
	// generate_image, attach_file, etc.). Blocking by name here was
	// historical noise from before those gates existed.
	//
	// run_local / write_file are no longer registered as standalone
	// tools — both live as actions on the `workspace` GroupedTool
	// (run / write). The shell + write gating happens via Caps on
	// those actions, not via blocklist by name. Keeping dead entries
	// here would suggest blocked names that are no longer reachable.
}

// FilterChatTools returns a copy of tools excluding those whose names are
// in the provided blocklist.
func FilterChatTools(blocklist map[string]bool) []ChatTool {
	var out []ChatTool
	for _, t := range RegisteredChatTools() {
		if blocklist[t.Name()] {
			continue
		}
		out = append(out, t)
	}
	return out
}

// FilterChatToolsPrivate returns tools that do not contact the internet.
// Tools implementing InternetTool with IsInternetTool() returning true are
// excluded. The global BlockedTools set is always respected.
func FilterChatToolsPrivate() []ChatTool {
	var out []ChatTool
	for _, t := range RegisteredChatTools() {
		if BlockedTools[t.Name()] {
			continue
		}
		if it, ok := t.(InternetTool); ok && it.IsInternetTool() {
			continue
		}
		out = append(out, t)
	}
	return out
}
