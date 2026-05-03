package techwriter

import (
	"net/http"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterApp(new(TechWriterAgent))
	RegisterRouteStage(RouteStage{Key: "app.techwriter", Label: "TechWriter", Default: "worker (thinking)", DefaultBudget: 16384, Group: "Apps"})
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
const mergeSourceTable = "techwriter_merge_sources"
const revisionTable = "techwriter_revisions"
const maxArticleRevisions = 50

// ArticleRevision is a point-in-time snapshot created on each save.
type ArticleRevision struct {
	ID        string `json:"id"`
	ArticleID string `json:"article_id"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Date      string `json:"date"`
}

// MergeSourceRecord stores a saved merge source document.
type MergeSourceRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Body string `json:"body"`
	Date string `json:"date"`
}

type TechWriterAgent struct {
	input struct {
		web string
	}
	AppCore
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
