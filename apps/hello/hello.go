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
	T.HandleFunc("/chart", T.handleChartPage)
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
			{
				Title:    "Client action (SDK demo)",
				Subtitle: "Button, action, and modal are all framework primitives — no hand-written HTML/JS.",
				Body: ui.Toolbar{Actions: []ui.ToolbarAction{
					{Label: "Say hello", Method: "client", URL: "hello_greet", Variant: "primary"},
				}},
			},
		},
		// Head wires the browser behavior in pure Go. The framework assembles
		// the <script> + window.uiRegisterClientAction call + readiness guard;
		// the handler body opens the shared ui.uiOpenModal primitive. No app
		// writes a <script> blob or an overlay by hand.
		Head: ui.NewHead().
			CSS(`.hello-greet-note{color:var(--text-mute);font-size:0.85rem;line-height:1.5}`).
			ClientAction("hello_greet", helloGreetJS),
		Footer:    "AgentLoopPanel demo →",
		FooterURL: "agent",
	}
	page.ServeHTTP(w, r)
}

// helloGreetJS is the browser handler for the "Say hello" toolbar button. It's
// a JS function expression registered by ui.Head.ClientAction; the runtime
// calls it with a context object when the Method:"client" button is clicked.
// Everything it touches (the button, the registration, the modal) is a
// framework primitive — this is the reference for wiring app behavior without
// writing HTML/CSS/DOM JS by hand.
const helloGreetJS = `function(ctx){
  window.uiOpenModal({
    title: 'Hello from a client action',
    subtitle: 'This button + modal were wired entirely in Go via ui.Toolbar, ui.Head, and window.uiOpenModal.',
    mount: function(body){
      var p = document.createElement('p');
      p.className = 'hello-greet-note';
      p.textContent = 'No hand-written <script> blob, no overlay, no Escape/close boilerplate — the framework assembled all of it.';
      body.appendChild(p);
    }
  });
}`

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

// --- chart demo --------------------------------------------------------------

// handleChartPage demonstrates the generic ui.ChartPanel primitive:
// bar / line / area / pie declared inline (no HTML/JS). Each section's
// Body is a ChartPanel; the runtime renders it as theme-aware inline
// SVG. This is the reference for how an app (or a Builder-authored
// app_def "chart" section) draws data.
func (T *HelloAgent) handleChartPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	pv := func(v float64) *float64 { return &v }
	page := ui.Page{
		Title:     "Charts",
		ShowTitle: true,
		BackURL:   "/hello",
		MaxWidth:  "760px",
		Sections: []ui.Section{
			{
				Title: "Revenue by Quarter",
				Body: ui.ChartPanel{
					ChartType: "bar",
					Labels:    []string{"Q1", "Q2", "Q3", "Q4"},
					Series: []ui.ChartSeries{
						{Name: "Product A", Points: []float64{12, 19, 15, 22}},
						{Name: "Product B", Points: []float64{8, 11, 14, 9}},
					},
				},
			},
			{
				Title: "Signups",
				Body: ui.ChartPanel{
					ChartType: "line",
					Labels:    []string{"Jan", "Feb", "Mar", "Apr", "May"},
					Series: []ui.ChartSeries{
						{Name: "Free", Points: []float64{30, 45, 42, 60, 71}},
						{Name: "Paid", Points: []float64{5, 9, 14, 18, 26}},
					},
				},
			},
			{
				Title: "Traffic",
				Body: ui.ChartPanel{
					ChartType: "area",
					Labels:    []string{"Mon", "Tue", "Wed", "Thu", "Fri"},
					Series: []ui.ChartSeries{
						{Name: "Web", Points: []float64{40, 55, 48, 62, 70}},
						{Name: "App", Points: []float64{20, 25, 30, 28, 35}},
					},
				},
			},
			{
				Title: "Browser Share",
				Body: ui.ChartPanel{
					ChartType: "pie",
					Series: []ui.ChartSeries{
						{Name: "Chrome", Value: pv(63)},
						{Name: "Safari", Value: pv(20)},
						{Name: "Firefox", Value: pv(9)},
						{Name: "Edge", Value: pv(8)},
					},
				},
			},
		},
	}
	page.ServeHTTP(w, r)
}
