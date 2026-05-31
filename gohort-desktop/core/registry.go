// Tool registry. Plugins register themselves at package init time
// via RegisterTool. The main app + Wails bridge + (eventually) the
// WebSocket announcer read the registered set via RegisteredTools.
//
// Registration is process-wide and one-way: tools added at startup
// stay for the life of the process. Mutex around the slice so a
// future hot-reload story or test parallelism doesn't race.

package core

import (
	"fmt"
	"sort"
	"sync"
)

var (
	registry_mu sync.RWMutex
	registry    []Tool
)

// RegisterTool appends a tool to the registry. Called from each
// tool package's init(). Refuses duplicates by name (panics — a
// duplicate name in init order is a programming error, surface it
// immediately rather than letting the second registration silently
// shadow the first).
func RegisterTool(t Tool) {
	if t == nil {
		return
	}
	registry_mu.Lock()
	defer registry_mu.Unlock()
	name := t.Name()
	for _, existing := range registry {
		if existing.Name() == name {
			panic(fmt.Sprintf("gohort-desktop: duplicate tool name %q registered", name))
		}
	}
	registry = append(registry, t)
}

// RegisteredTools returns every registered tool whose Enabled()
// returns true, sorted by name for stable iteration. Tools that
// report Enabled()==false are skipped — they remain registered
// (RegisterTool was called) but don't surface in catalogs and can't
// be invoked.
func RegisteredTools() []Tool {
	registry_mu.RLock()
	defer registry_mu.RUnlock()
	out := make([]Tool, 0, len(registry))
	for _, t := range registry {
		if t.Enabled() {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// FindTool returns the registered tool with the given name and a
// found flag. Honors Enabled() — disabled tools return (nil, false)
// the same as unregistered ones.
func FindTool(name string) (Tool, bool) {
	registry_mu.RLock()
	defer registry_mu.RUnlock()
	for _, t := range registry {
		if t.Name() == name && t.Enabled() {
			return t, true
		}
	}
	return nil, false
}

// InvokeTool resolves a tool by name and runs its handler with the
// given args. Returns the handler's result + error directly; wraps
// the not-found case with a clear message so callers (Wails bridge,
// WebSocket dispatch) don't have to format it themselves.
func InvokeTool(name string, args map[string]any) (string, error) {
	t, ok := FindTool(name)
	if !ok {
		return "", fmt.Errorf("tool %q not registered (or disabled). Registered: %v", name, registered_names())
	}
	return t.Handler()(args)
}

// registered_names returns just the names of currently-enabled
// tools — used in error messages so the caller (LLM) can see what
// IS available when they ask for something that isn't.
func registered_names() []string {
	registry_mu.RLock()
	defer registry_mu.RUnlock()
	out := make([]string, 0, len(registry))
	for _, t := range registry {
		if t.Enabled() {
			out = append(out, t.Name())
		}
	}
	sort.Strings(out)
	return out
}
