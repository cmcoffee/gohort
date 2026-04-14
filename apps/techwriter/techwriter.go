package techwriter

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const access_table = "access_control"

var twAllowedIPs string

func init() {
	RegisterApp(new(TechWriterAgent))

	RegisterSetupSection(SetupSection{
		Name:  "Access Control (TechWriter IP allowlist)",
		Order: 110,
		Build: func(db Database) *Options {
			db.Get(access_table, "techwriter_allowed_ips", &twAllowedIPs)
			access := NewOptions(" [Access Control] ", "(selection or 'q' to return to previous)", 'q')
			access.StringVar(&twAllowedIPs, "TechWriter Allowed IPs", twAllowedIPs, "Comma-separated CIDR/IP allowlist for TechWriter and Push-to-TechWriter (empty = open to all).")
			return access
		},
		Save: func(db Database) {
			db.Set(access_table, "techwriter_allowed_ips", strings.TrimSpace(twAllowedIPs))
		},
	})
}

// ArticleRecord stores a saved article.
type ArticleRecord struct {
	ID      string `json:"ID"`
	Subject string `json:"Subject"`
	Body    string `json:"Body"`
	Date    string `json:"Date"`
}

func (r ArticleRecord) GetDate() string { return r.Date }

const HistoryTable = "techwriter_history"

type TechWriterAgent struct {
	input struct {
		web string
	}
	FuzzAgent
}

func (T TechWriterAgent) Name() string { return "techwriter" }

func (T TechWriterAgent) Desc() string {
	return "Technical article writer with LLM co-editing for documentation and instructions."
}

func (T TechWriterAgent) SystemPrompt() string { return "" }

func (T *TechWriterAgent) Init() (err error) {
	T.Flags.StringVar(&T.input.web, "web", "", "Start web UI on this address (e.g. ':8080').")
	T.Flags.Order("web")
	return T.Flags.Parse()
}

func (T *TechWriterAgent) Main() (err error) {
	if T.input.web != "" {
		return T.serveWeb()
	}
	Log("Usage: techwriter --web :8080")
	return nil
}

func (T *TechWriterAgent) serveWeb() error {
	mux := http.NewServeMux()
	T.RegisterRoutes(mux, "")
	scheme := "http"
	if TLSEnabled() {
		scheme = "https"
	}
	Log("TechWriter Web UI: %s://%s", scheme, T.input.web)
	return ListenAndServeTLS(T.input.web, mux)
}
