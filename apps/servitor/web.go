package servitor

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterApp(new(Servitor))
	registerServitorMCPTools()
	RegisterRouteStage(RouteStage{
		Key:     "app.servitor",
		App:     "/servitor",
		Label:   "Servitor (worker — runs SSH commands)",
		Default: "worker",
		Group:   "Servitor",
		Private: true,
	})
	RegisterRouteStage(RouteStage{
		Key:     "app.servitor.orchestrator",
		App:     "/servitor",
		Label:   "Servitor: Orchestrator",
		Default: "worker (thinking)",
		Group:   "Servitor",
		Private: true,
	})
}

// probeEvent is one event emitted by a running session goroutine.
type probeEvent struct {
	Kind   string         `json:"kind"` // status | cmd | output | message | confirm | reply | error | done | watch | notes_consumed | intent | plan_set | plan_step
	Text   string         `json:"text,omitempty"`
	Reason string         `json:"reason,omitempty"` // destructive reason for confirm events
	IDs    []string       `json:"ids,omitempty"`    // notes_consumed: which queued notes the orchestrator just drained
	Plan   []WorkPlanStep `json:"plan,omitempty"`   // plan_set / plan_step: snapshot of the current plan for the UI to render
	// PlanID identifies WHICH investigation's plan this snapshot belongs to.
	// The UI keys blocks by id, and the plan block used to use a constant one —
	// so a second investigation in the same session updated the first
	// investigation's checklist in place instead of posting its own. One id per
	// plan instance (per buildPlanTools call) gives each investigation its own
	// card, which is what makes a follow-up investigation legible.
	PlanID string `json:"plan_id,omitempty"`
}

// toInt extracts an int from an arbitrary JSON-decoded value. JSON numbers
// arrive as float64 in Go's stdlib; tool-arg maps may also have int or
// strings depending on the LLM's serialization. Returns the parsed value
// and true on success.
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		var n int
		_, err := fmt.Sscanf(x, "%d", &n)
		return n, err == nil
	}
	return 0, false
}

const alwaysAllowTable = "ssh_always_allow"
const notesTable = "ssh_notes"

// pendingConfirm is one session's operator-approval channel plus the two facts
// needed to route an answer INTO it safely: whose session it is, and whether a
// person is the one answering.
//
// Both exist because the answer arrives on a shared endpoint that cannot say
// which session it belongs to. The confirm card's id is generated per event by
// the bridge (see translateProbeEvent) and is not the session id, so the
// handler has to select a channel rather than address one. Selecting without
// these two fields is what let any servitor user answer any other user's
// pending command.
type pendingConfirm struct {
	// ch carries the decision to the waiting runSession. Buffered (size 1), so
	// a click that lands before the run blocks on the read still delivers.
	ch chan bool
	// owner is the user whose session this is. Empty NEVER matches, which is
	// the same fail-closed posture LiveEntry.MaskedLabel takes with an untagged
	// session: a channel nobody can be shown to own is a channel nobody may
	// answer.
	owner string
	// interactive marks a channel a PERSON answers. The read-only paths (guide
	// investigations, workspace drills) register a channel too, but a goroutine
	// feeds theirs a standing denial to keep the run from mutating anything. An
	// operator's "allow" must never land in one of those, and sync.Map.Range
	// visits in unspecified order, so excluding them by construction is the
	// only reliable way to keep a click on one session's card from flipping a
	// different session's auto-deny to allow.
	interactive bool
}

var (
	probeSessions     = NewLiveSessionMap[probeEvent](0)
	confirmChans      sync.Map // session_id -> pendingConfirm
	pendingCmds       sync.Map // session_id -> command string currently awaiting confirmation
	termBuffers       sync.Map // "userID:applianceID" -> *termBuffer; persistent command+output log mirrored to any connected terminal WebSocket
	sessionAppliances sync.Map // session_id -> applianceID (for building resume URLs in the dashboard live-sessions panel)
)

// Mid-flight injection-queue types + registry were lifted into
// core/injection.go so phantom and orchestrate can share the same
// machinery. Aliases keep existing call sites in this file terse;
// the registry helpers (RegisterInjectionQueue / LookupInjectionQueue
// / ReleaseInjectionQueue) sit on top of a shared sync.Map in core.
type injectionQueue = InjectionQueue
type injectionNote = InjectionNote

func (T *Servitor) WebPath() string { return "/servitor" }
func (T *Servitor) WebName() string { return "Servitor" }
func (T *Servitor) WebDesc() string {
	return "Dispatch AI agents to remote appliances (SSH) or local commands to investigate, map, and operate systems."
}

func (T *Servitor) RegisterRoutes(mux *http.ServeMux, prefix string) {
	// The running instance, so the agent tool provider can reach the worker
	// model the mint step needs. Here rather than in init(): init() has no
	// instance, and a provider registered with a nil one would degrade to "no
	// model available" on every request forever.
	RegisterServitorInstance(T)
	// Bucket migration: if the "servitor" bucket is empty, fall back to the
	// previous bucket name ("sysprobe"), and then to the even older "ssh_probe".
	// Bucket() on a substore navigates to the sibling via the underlying store.
	if T.DB != nil && len(T.DB.Tables()) == 0 {
		for _, name := range []string{"sysprobe", "ssh_probe"} {
			older := T.DB.Bucket(name)
			if len(older.Tables()) > 0 {
				T.DB = older
				break
			}
		}
	}

	// Expose servitor's appliances as a generic reference source so writer
	// apps can ground drafts in gathered system knowledge. T.DB is final here.
	RegisterReferenceSource(servitorSource{app: T})

	sub := NewWebUI(T, prefix, AppUIAssets{})
	sub.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handleChatPage(w, r)
	})
	// AgentLoopPanel-facing endpoints — translator on top of the
	// existing probeSessions queue. See chat_bridge.go / chat_page.go.
	sub.HandleFunc("/api/chat/v2/events", T.handleChatEvents)
	sub.HandleFunc("/api/chat/v2/confirm", T.handleChatConfirm)
	sub.HandleFunc("/api/profile", T.handleProfile)
	sub.HandleFunc("/api/appliances", T.handleAppliances)
	sub.HandleFunc("/api/appliances/", T.handleApplianceMemory) // /api/appliances/<id>/{facts,graph,inferred,...} — shared agent-memory surface
	sub.HandleFunc("/api/appliance/", T.handleAppliance)
	// Capability proposals: the list the owner reviews, and the decision on one.
	sub.HandleFunc("/api/appliance-tools", T.handleApplianceTools)
	sub.HandleFunc("/api/appliance-tool", T.handleApplianceTool)
	// Which agents may work with which machines — the first link in the chain.
	sub.HandleFunc("/api/command-grants", T.handleCommandGrants)
	sub.HandleFunc("/api/access-agents", T.handleAccessAgents)
	sub.HandleFunc("/api/chat", T.handleChat)
	// Persisted chat sessions back the left rail (see sessions.go).
	sub.HandleFunc("/api/sessions", T.handleServitorSessionList)
	sub.HandleFunc("/api/sessions/", T.handleServitorSessionOne)
	sub.HandleFunc("/api/push-to-guide", T.handlePushToGuide)
	sub.HandleFunc("/api/inject", T.handleInject)
	sub.HandleFunc("/api/map", T.handleMap)
	sub.HandleFunc("/api/mapapp", T.handleMapApp)
	sub.HandleFunc("/api/terminal", T.handleTerminal)
	sub.HandleFunc("/api/facts", T.handleFacts)
	sub.HandleFunc("/api/knowledge/export", T.handleKnowledgeExport)
	sub.HandleFunc("/api/memory/clear", T.handleMemoryClear)
	sub.HandleFunc("/api/repo/refresh", T.handleRepoRefresh)
	// Evidence bundles: staging one file per request, then a single ingest
	// over everything staged — which doubles as the retry path after a failed
	// ingest, without re-sending a gigabyte. See bundle_upload.go.
	// Tools bindable to a toolset appliance — the owner's own pool.
	sub.HandleFunc("/api/bindable-tools", T.handleBindableTools)
	// Serve investigations to other instances, and list what registered peers
	// let US ask about. Wired here rather than in init because it needs T.DB.
	T.registerPeerInvestigation()
	T.registerPeerKnowledge()
	T.registerPeerExec()
	sub.HandleFunc("/api/peer-appliances", T.handlePeerAppliances)
	sub.HandleFunc("/api/appliance-peers", T.handleAppliancePeers)
	sub.HandleFunc("/api/bundle/upload", T.handleBundleUpload)
	sub.HandleFunc("/api/bundle/ingest", T.handleBundleIngest)
	sub.HandleFunc("/api/collections", T.handleCollectionsList)
	sub.HandleFunc("/api/cancel", probeSessions.HandleCancel("servitor"))
	sub.HandleFunc("/api/save_destinations", T.handleSaveDestinations)
	sub.HandleFunc("/api/save_article", T.handleSaveArticle)
	sub.HandleFunc("/api/save_snippet", T.handleSaveSnippet)
	sub.HandleFunc("/api/rules", T.handleRules)
	sub.HandleFunc("/api/rules/", T.handleRuleDelete)
	sub.HandleFunc("/api/permissions", T.handlePermissions)
	MountSubMux(mux, prefix, sub)
	go T.runWatchLoop(AppContext())
	RegisterLiveProvider(func() []LiveEntry {
		entries := probeSessions.ActiveSessions()
		for i := range entries {
			entries[i].App = "Servitor"
			entries[i].Path = prefix
			// New framework page reconnects via ?reconnect=<sid>;
			// the runtime taps the chat-events translator stream
			// for that session id on mount. The appliance picker
			// re-syncs to the saved active appliance from a
			// separate flow — no need to thread it through here.
			entries[i].URL = fmt.Sprintf("%s/?reconnect=%s",
				prefix, url.QueryEscape(entries[i].ID))
		}
		return entries
	})
}

// emit adds an event to a session's buffer.
func emit(id string, ev probeEvent) {
	probeSessions.AppendEvent(id, ev, false)
}
