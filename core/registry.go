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
// flag is set accordingly.
func ChatToolToAgentToolDef(ct ChatTool) AgentToolDef {
	confirm := false
	if c, ok := ct.(ConfirmableTool); ok {
		confirm = c.NeedsConfirm()
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        ct.Name(),
			Description: ct.Desc(),
			Parameters:  ct.Params(),
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
