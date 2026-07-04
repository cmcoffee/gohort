// Watch-tool invocation + hashing helpers.
//
// The standalone "watcher" engine (a parallel change-detection system with its
// own store, poll loop, evaluators, and admin UI) was retired once its one
// distinct capability — capture a tool, hash its output, wake on change — was
// folded into the event-monitor engine as the "watch" kind (see
// event_monitor.go). What survives here is the shared machinery that kind
// needs: invoking a captured tool (globally-registered or owner-scoped) and
// hashing its output for change detection.
//
// Why "captured tool call" instead of "URL"? Because the LLM already knows how
// to call tools correctly — descriptions, allowed URL patterns, credential
// auth, response shape. Capturing the invocation the LLM proved works lets a
// watch monitor inherit all that correctness for free; the only thing it does
// is "invoke a registered tool, hash the result, fire on change."

package core

import (
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

func init() {
	// kvlite encodes records via gob; a monitor's ToolArgs is map[string]any
	// and gob refuses to encode unregistered concrete types inside an
	// interface field. Register the shapes the LLM actually passes in tool
	// args so save round-trips work. (EventMonitor.ToolArgs relies on this.)
	gob.Register(map[string]any{})
	gob.Register([]any{})
	gob.Register(map[string]string{})
	gob.Register([]string{})
}

// InvokeWatcherTool runs a captured tool call. Routes by tool-name prefix:
// call_<name> goes through the secure-API dispatcher (so per-credential auth +
// URL allowlist + method allowlist + daily cap all apply); everything else is
// looked up in the static chat-tool registry. Exported so non-watcher code can
// re-use the same dispatch path.
func InvokeWatcherTool(toolName string, toolArgs map[string]any) (string, error) {
	if toolName == "" {
		return "", fmt.Errorf("empty tool name")
	}
	if strings.HasPrefix(toolName, "call_") {
		credName := strings.TrimPrefix(toolName, "call_")
		urlStr := StringArg(toolArgs, "url")
		method := StringArg(toolArgs, "method")
		body := StringArg(toolArgs, "body")
		return Secure().DispatchToolCall(nil, credName, urlStr, method, body)
	}
	t, ok := LookupChatTool(toolName)
	if !ok {
		return "", fmt.Errorf("tool %q is not registered", toolName)
	}
	if st, ok := t.(SessionChatTool); ok {
		return st.RunWithSession(toolArgs, nil)
	}
	return t.Run(toolArgs)
}

// ErrWatchToolNotHandled signals that a registered WatchToolInvoker doesn't own
// the named tool, so the caller should fall back to the global invocation path.
var ErrWatchToolNotHandled = errors.New("watch tool not handled by invoker")

// WatchToolInvoker lets an app resolve OWNER-SCOPED tools a watch monitor wants
// to poll — ones that only exist as per-session closures and so aren't in the
// global chat-tool registry (e.g. read_phantom_chat, which needs the owner to
// reach the right bridge). The app registers one at startup; it returns
// ErrWatchToolNotHandled for tools it doesn't own so the global path can try.
// agentID scopes tools that only exist per-agent — the channel tools (read_chat
// etc.) are built from a specific agent's bound channels, so a watch on one
// (e.g. an await_result on read_chat) can't be resolved from owner alone. It's
// the monitor's WakeAgent; "" when the watch isn't agent-scoped.
type WatchToolInvoker func(owner, agentID, toolName string, toolArgs map[string]any) (string, error)

var watchToolInvoker WatchToolInvoker

// RegisterWatchToolInvoker installs the owner-aware invoker. Call once at
// startup. Optional — without it, watch monitors can only poll globally
// registered tools + call_<cred> secure APIs.
func RegisterWatchToolInvoker(fn WatchToolInvoker) { watchToolInvoker = fn }

// InvokeWatchTool resolves a watch monitor's captured tool WITH owner + agent
// context: the registered owner-aware invoker first (session/agent-scoped tools
// like read_chat), then the global InvokeWatcherTool (registered chat tools +
// call_<cred> secure APIs). agentID is the monitor's WakeAgent, needed to build
// per-agent channel tools; pass "" when not agent-scoped.
func InvokeWatchTool(owner, agentID, toolName string, toolArgs map[string]any) (string, error) {
	if watchToolInvoker != nil {
		out, err := watchToolInvoker(owner, agentID, toolName, toolArgs)
		if !errors.Is(err, ErrWatchToolNotHandled) {
			return out, err
		}
	}
	return InvokeWatcherTool(toolName, toolArgs)
}

func sha256Sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// HashWatcherBody is the exported wrapper around the change-detection hash, so
// callers that pre-seed a watch monitor's baseline (the create handler, using
// its probe response) compute the hash the same way the poll loop will.
func HashWatcherBody(s string) string { return sha256Sum(s) }
