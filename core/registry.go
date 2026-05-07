package core

import (
	"fmt"
	"sort"
	"sync"
)

// SetupSection represents a configuration section contributed by an app.
// Apps register these in init() so their settings appear in --setup
// without the framework needing to know about them.
type SetupSection struct {
	Name  string                   // display name for the setup menu
	Order int                      // display order (lower = earlier; framework uses 0-99, apps use 100+)
	Build func(db Database) *Options // build the interactive submenu
	Save  func(db Database)        // persist values after user exits setup
}

var (
	registryMu            sync.Mutex
	registeredAgents      []Agent
	registeredApps        []Agent
	registeredAdminAgents []Agent
	registeredChatTools   []ChatTool
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

// RegisterChatTool registers a chat tool.
func RegisterChatTool(tool ChatTool) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registeredChatTools = append(registeredChatTools, tool)
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
	return AgentToolDef{
		Tool: Tool{
			Name:        ct.Name(),
			Description: ct.Desc(),
			Parameters:  ct.Params(),
			Caps:        caps,
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
	var caps []Capability
	if c, ok := ct.(CapabilityTool); ok {
		caps = c.Caps()
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        ct.Name(),
			Description: ct.Desc(),
			Parameters:  ct.Params(),
			Caps:        caps,
		},
		Handler:      handler,
		NeedsConfirm: confirm,
	}
}

// GetAgentToolsWithSession looks up registered chat tools by name and returns
// them as AgentToolDefs bound to sess. Session-aware tools (SessionChatTool)
// receive sess on each call; stateless tools fall back to Run as usual.
func GetAgentToolsWithSession(sess *ToolSession, names ...string) ([]AgentToolDef, error) {
	var tools []AgentToolDef
	for _, name := range names {
		ct, ok := FindChatTool(name)
		if !ok {
			return nil, fmt.Errorf("tool %q not found in registry", name)
		}
		tools = append(tools, ChatToolToAgentToolDefWithSession(ct, sess))
	}
	return tools, nil
}

// BlockedTools is the set of tool names that are never exposed in chat,
// regardless of mode. Tools that perform real-world side effects are
// blocked from the testing UI to keep it sandboxed.
var BlockedTools = map[string]bool{
	"run_command": true, // shell execution — risky in a web UI
	"send_email":  true, // sends real email
	"run_local":   true, // sandboxed shell — opt-in only via AllowedCaps[CapExecute]
	"write_file":  true, // sandboxed file write — opt-in only via AllowedCaps[CapWrite]
	"workspace":   true, // managed via auto-mint — LLM never needs to think about it
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
