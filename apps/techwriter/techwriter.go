package techwriter

import (
	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterApp(new(TechWriterAgent))
	// Private route stage — admin UI hides the "Lead" option in the
	// routing dropdown for this key, and IsPrivateStage(req.Key)
	// rejects any attempt to set it to "lead". Combined with
	// T.Private() in Init() (which sets NoLead on the AppCore), this
	// gives techwriter the same hard privacy guarantee as servitor:
	// article bodies stay on the local worker LLM.
	RegisterRouteStage(RouteStage{
		Key:     "app.techwriter",
		Label:   "TechWriter",
		Default: "worker (thinking)",
		Group:   "Apps",
		Private: true,
	})
}

// ArticleRecord stores a saved article.
type ArticleRecord struct {
	ID       string `json:"ID"`
	Subject  string `json:"Subject"`
	Body     string `json:"Body"`
	Date     string `json:"Date"`
	// ImageURL is the optional header image URL associated with the
	// article. Set by the Generate Image flow, persisted across saves
	// and included in HTML export and preview output. Either a remote
	// URL (DALL-E) or a base64 data URL (Gemini) — both render in
	// browsers without further work.
	ImageURL string `json:"ImageURL,omitempty"`
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
	AppCore
}

func (T TechWriterAgent) Name() string { return "techwriter" }

func (T TechWriterAgent) Desc() string {
	return "Technical article writer with LLM co-editing for documentation and instructions."
}

func (T TechWriterAgent) SystemPrompt() string { return "" }

func (T *TechWriterAgent) Init() (err error) {
	// HARD GUARD: techwriter never escalates to the lead (remote) LLM.
	// Article bodies are sensitive (internal docs, drafts, customer
	// data) and shouldn't leak to a remote provider. Mirrors the
	// servitor pattern. Image generation is the only remaining
	// externally-reachable path and is opt-in per click.
	T.Private()
	return T.Flags.Parse()
}

// Main is a no-op — techwriter only runs inside the dashboard now.
// The standalone --web flag was redundant with `gohort serve`.
func (T *TechWriterAgent) Main() (err error) {
	Log("TechWriter is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}
