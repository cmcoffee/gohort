// Package hello is a minimal reference app showing the gohort
// framework's app shape end-to-end: agent registration, the WebApp
// interface, a routed sub-mux, declarative ui.Page surfaces, and
// SSE-driven AgentLoopPanel wiring.
//
// Read this alongside core/ui/AUTHORING.md. Copy this package as a
// starting point when bootstrapping a new app — replace "Hello" /
// "hello" with the new app's name and grow from there.
//
// Not enabled by default. To turn it on, add a blank import to
// agents.go:
//
//	_ "github.com/cmcoffee/gohort/apps/hello"
//
// Two surfaces are mounted:
//
//   - /hello/        — a tiny FormPanel echoing a name back
//   - /hello/agent   — an AgentLoopPanel demo. Sends fake activity
//                      events, an operator-confirm prompt, and a
//                      streamed assistant reply so you can see the
//                      primitive's wire protocol end to end.
package hello

import (
	"encoding/json"
	"net/http"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

func init() { RegisterApp(new(HelloAgent)) }

// HelloAgent is the app entry point. Embedding AppCore wires in the
// framework's shared state (DB, LLM handles, flag set, cost tracker).
type HelloAgent struct {
	AppCore
}

// --- core.Agent interface ----------------------------------------------------

func (T HelloAgent) Name() string         { return "hello" }
func (T HelloAgent) SystemPrompt() string { return "" }
func (T HelloAgent) Desc() string {
	return "Apps: Reference scaffold — a minimal hello-world app."
}

func (T *HelloAgent) Init() error { return T.Flags.Parse() }

func (T *HelloAgent) Main() error {
	// HelloAgent does not implement core.CLIApp, so it's dashboard-only.
	// Main() is unreachable through the CLI; included only because the
	// Agent interface requires it.
	Log("Hello is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}

// --- core.WebApp interface ---------------------------------------------------

func (T *HelloAgent) WebPath() string { return "/hello" }
func (T *HelloAgent) WebName() string { return "Hello" }
func (T *HelloAgent) WebDesc() string { return "A minimal hello-world reference app." }

// Routes — new SimpleWebApp interface. Framework wires the sub-mux
// behind the scenes; we just register handlers via T.HandleFunc.
// No more NewWebUI / MountSubMux / mux-prefix plumbing.
func (T *HelloAgent) Routes() {
	T.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		T.handlePage(w, r)
	})
	T.HandleFunc("/api/echo", T.handleEcho)
	registerAgentRoutes(T)
}

// --- page --------------------------------------------------------------------

func (T *HelloAgent) handlePage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	page := ui.Page{
		Title:     "Hello",
		ShowTitle: true,
		BackURL:   "/",
		MaxWidth:  "900px",
		Sections: []ui.Section{
			{
				Title:    "Greeting",
				Subtitle: "Type your name and submit — server echoes it back.",
				Body: ui.FormPanel{
					// No Source — this is a write-only form (no
					// existing record to load). PostURL is the only
					// write destination. Field keys map to JSON body
					// keys on POST.
					PostURL: "api/echo",
					Fields: []ui.FormField{
						{
							Field:       "name",
							Label:       "Your name",
							Type:        "text",
							Placeholder: "Alex",
						},
					},
				},
			},
		},
		Footer:    "AgentLoopPanel demo →",
		FooterURL: "agent",
	}
	page.ServeHTTP(w, r)
}

// --- endpoint ----------------------------------------------------------------

func (T *HelloAgent) handleEcho(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		body.Name = "world"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": "hello, " + body.Name,
	})
}
