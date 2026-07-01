// Package enginseer is a code-investigation app: point it at a git
// repository (GitHub/GitLab/any git URL) and ask questions about the codebase
// — "how does X do Y?", "where is X stored?", "what generates this log line?" —
// the same way servitor answers questions about a live system.
//
// It reuses the orchestrate engine (the co-author pattern, like guides): a
// registered investigator app agent runs with repo-specific tools that search
// and read the code. The source is cloned once into a tmpfs, ingested into the
// dedicated, encrypted RepoFilesDB, and the plaintext clone is discarded — so
// nothing but encrypted, derived content persists at rest.
package enginseer

import (
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

func init() {
	RegisterApp(new(Enginseer))
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID:          repoInvestigatorAgentID,
		OwningApp:   "Enginseer",
		Name:        "Repo Investigator",
		Description: "Answers questions about a git repository by searching and reading its code.",
		Prompt:      repoInvestigatorPrompt(),
		Hidden:      true,
		// Task-focused: narrow Explicit Memory. Structured facts about the code
		// live in the graph; working knowledge in Reference Memory.
		MemoryMode: "shortcuts",
	})
}

// Enginseer is the app. Web-only in practice; the CLI side is a stub.
type Enginseer struct {
	AppCore
}

// --- core.Agent (registration / CLI side) ---

func (T Enginseer) Name() string         { return "enginseer" }
func (T Enginseer) Desc() string         { return "Apps: Investigate a git repository — ask questions about the codebase." }
func (T Enginseer) SystemPrompt() string { return "" }
func (T *Enginseer) Init() error         { return T.Flags.Parse() }
func (T *Enginseer) Main() error {
	Log("Enginseer is a web app — open /enginseer in the dashboard.")
	return nil
}

// --- core.SimpleWebApp (web side) ---

func (T *Enginseer) WebPath() string { return "/enginseer" }
func (T *Enginseer) WebName() string { return "Enginseer" }
func (T *Enginseer) WebDesc() string { return "Ask questions about a git repository's codebase." }

func (T *Enginseer) Routes() {
	T.HandleFunc("/", T.handleChatPage)
	T.HandleFunc("/api/repos", T.handleRepos)    // GET list / POST create
	T.HandleFunc("/api/repos/", T.handleRepoOne) // DELETE <id> / POST <id>/refresh / memory sub-routes
	T.HandleFunc("/api/map", T.handleMapRepo)    // POST — kick a background repo-mapping run
	// Chat — the investigator runs via orchestrate's chat path (guides pattern).
	T.HandleFunc("/api/chat/send", T.handleChatSend)
	T.HandleFunc("/api/chat/cancel", func(w http.ResponseWriter, r *http.Request) { T.dispatchChat(w, r, "cancel", "") })
	T.HandleFunc("/api/chat/sessions", func(w http.ResponseWriter, r *http.Request) { T.dispatchChat(w, r, "sessions", "") })
	T.HandleFunc("/api/chat/sessions/", func(w http.ResponseWriter, r *http.Request) {
		T.dispatchChat(w, r, "session-one", strings.TrimPrefix(r.URL.Path, "/api/chat/sessions/"))
	})
}

// clearRepoScopedMemory wipes a repo's orchestrate-scoped memory (on delete),
// so a removed repo leaves no orphaned scope behind.
func clearRepoScopedMemory(repoID string) {
	orch := findOrchestrate()
	if orch == nil {
		return
	}
	_ = orch.WipeScopedMemory(orchestrate.AgentScope{
		AgentID:   repoInvestigatorAgentID,
		ScopeUser: repoScope(repoID),
	})
}

// repoInvestigatorAgentID is the stable ID of the orchestrate app agent that
// backs every repo investigation. ID convention: app-<app>-<role>.
const repoInvestigatorAgentID = "app-enginseer-investigator"

// cachedOrch holds the registered orchestrate app (the registry is fixed at
// runtime, so cache after the first hit).
var cachedOrch *orchestrate.OrchestrateApp

func findOrchestrate() *orchestrate.OrchestrateApp {
	if cachedOrch != nil {
		return cachedOrch
	}
	a, ok := FindAgent("orchestrate")
	if !ok {
		return nil
	}
	o, ok := a.(*orchestrate.OrchestrateApp)
	if !ok {
		return nil
	}
	cachedOrch = o
	return cachedOrch
}
