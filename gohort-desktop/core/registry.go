// Tool registry. Two tiers share one namespace:
//
//   - NATIVE tools register at package init() via RegisterTool. They are
//     compile-time, permanent, and never change for the life of the process
//     (filesystem, contacts, …).
//
//   - DYNAMIC tools register at runtime via ReplaceDynamicTools, grouped by a
//     "source" key (an MCP server, a declared command). A source's tools can be
//     swapped or dropped wholesale — this is the reload path that lets the
//     daemon's capability surface grow WITHOUT reshipping the binary. A native
//     tool always wins a name clash, so a dynamic source can never shadow a
//     built-in.
//
// The main app + Wails bridge + WebSocket announcer read the merged, enabled
// set via RegisteredTools. When the dynamic set changes, the registry fires the
// change hook (SetRegistryChangeHook) so the WS bridge re-announces the fresh
// catalog to the server without waiting for a reconnect.

package core

import (
	"fmt"
	"sort"
	"sync"
)

var (
	registry_mu          sync.RWMutex
	registry             []Tool                // native tools (init-time, permanent)
	dynamic              = map[string][]Tool{} // runtime tools, keyed by source
	registry_change_hook func()                // fired after a dynamic-set change
)

// RegisterTool appends a NATIVE tool to the registry. Called from each tool
// package's init(). Refuses duplicates by name (panics — a duplicate name in
// init order is a programming error, surface it immediately rather than letting
// the second registration silently shadow the first).
//
// Panics on a name outside ValidToolName's class for the same reason: a native
// name is a compile-time constant we author, and an unusable one means the tool
// is dropped from the model's catalog with no error anywhere. Better to fail on
// the first run in dev than to ship a capability that is never offered.
func RegisterTool(t Tool) {
	if t == nil {
		return
	}
	registry_mu.Lock()
	defer registry_mu.Unlock()
	name := t.Name()
	if !ValidToolName(name) {
		panic(fmt.Sprintf("gohort-desktop: tool name %q is not usable by the model APIs (must match ^[a-zA-Z0-9_-]{1,128}$)", name))
	}
	for _, existing := range registry {
		if existing.Name() == name {
			panic(fmt.Sprintf("gohort-desktop: duplicate tool name %q registered", name))
		}
	}
	registry = append(registry, t)
}

// ReplaceDynamicTools atomically replaces the runtime tools registered under
// `source` (e.g. "mcp:calendar", "command:screencap"). Passing an empty slice
// removes the source entirely. Unlike RegisterTool, dynamic tools can be
// swapped at runtime — the reload path for MCP servers and declared-command
// capabilities. Fires the registry-change hook (if set) AFTER the swap, so the
// WS bridge re-announces the new catalog.
func ReplaceDynamicTools(source string, tools []Tool) {
	registry_mu.Lock()
	if len(tools) == 0 {
		delete(dynamic, source)
	} else {
		cp := make([]Tool, len(tools))
		copy(cp, tools)
		dynamic[source] = cp
	}
	hook := registry_change_hook
	registry_mu.Unlock()
	// Fire outside the lock: the hook re-reads the registry (RegisteredTools),
	// which takes the read lock.
	if hook != nil {
		hook()
	}
}

// SetRegistryChangeHook installs a callback fired whenever the dynamic tool set
// changes via ReplaceDynamicTools. The WS bridge sets this to re-announce its
// catalog to the server without waiting for a reconnect. Passing nil clears it.
func SetRegistryChangeHook(fn func()) {
	registry_mu.Lock()
	registry_change_hook = fn
	registry_mu.Unlock()
}

// merged_locked returns native + dynamic tools as one slice — native first
// (winning any name clash), then dynamic sources in sorted order for a
// deterministic catalog. Caller must hold registry_mu (R or W).
func merged_locked() []Tool {
	out := make([]Tool, 0, len(registry)+len(dynamic))
	seen := make(map[string]bool, len(registry))
	for _, t := range registry {
		seen[t.Name()] = true
		out = append(out, t)
	}
	srcs := make([]string, 0, len(dynamic))
	for s := range dynamic {
		srcs = append(srcs, s)
	}
	sort.Strings(srcs)
	for _, s := range srcs {
		for _, t := range dynamic[s] {
			if !seen[t.Name()] {
				seen[t.Name()] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// RegisteredTools returns every registered tool (native + dynamic) whose
// Enabled() returns true, sorted by name for stable iteration. Tools that
// report Enabled()==false are skipped — they remain registered but don't
// surface in catalogs and can't be invoked (e.g. an MCP tool whose server died).
func RegisteredTools() []Tool {
	registry_mu.RLock()
	all := merged_locked()
	registry_mu.RUnlock()
	out := make([]Tool, 0, len(all))
	for _, t := range all {
		if !t.Enabled() {
			continue
		}
		// Backstop for DYNAMIC tools, whose names come from outside (an MCP
		// server, a declared command) and are sanitized at mint — native ones
		// already panicked in RegisterTool. Announcing a name the model APIs
		// reject doesn't cost one tool: the server drops it, and older servers
		// had the whole request rejected, taking every other tool down with it.
		if !ValidToolName(t.Name()) {
			warn_unusable_tool_name(t.Name())
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// FindTool returns the registered tool with the given name and a found flag.
// Honors Enabled() — disabled tools return (nil, false) the same as
// unregistered ones. Native tools take precedence over dynamic ones on a clash.
func FindTool(name string) (Tool, bool) {
	registry_mu.RLock()
	defer registry_mu.RUnlock()
	for _, t := range merged_locked() {
		if t.Name() == name && t.Enabled() {
			return t, true
		}
	}
	return nil, false
}

// InvokeTool resolves a tool by name and runs its handler with the given args.
// Returns the handler's result + error directly; wraps the not-found case with
// a clear message so callers (Wails bridge, WebSocket dispatch) don't have to
// format it themselves.
func InvokeTool(name string, args map[string]any) (string, error) {
	t, ok := FindTool(name)
	if !ok {
		return "", fmt.Errorf("tool %q not registered (or disabled). Registered: %v", name, registered_names())
	}
	return t.Handler()(args)
}

// registered_names returns just the names of currently-enabled tools — used in
// error messages so the caller (LLM) can see what IS available when they ask
// for something that isn't.
func registered_names() []string {
	registry_mu.RLock()
	all := merged_locked()
	registry_mu.RUnlock()
	out := make([]string, 0, len(all))
	for _, t := range all {
		if t.Enabled() {
			out = append(out, t.Name())
		}
	}
	sort.Strings(out)
	return out
}

// warn_unusable_tool_name logs a dropped name once per name, so a broken
// dynamic source doesn't spam the log on every catalog read (RegisteredTools
// runs on every announce and every invoke).
var unusable_names_logged sync.Map

func warn_unusable_tool_name(name string) {
	if _, seen := unusable_names_logged.LoadOrStore(name, true); seen {
		return
	}
	Warn("[registry] tool %q has a name the model APIs reject (must match ^[a-zA-Z0-9_-]{1,128}$); not announcing it", name)
}
