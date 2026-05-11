// Package answer provides a quick-answer web app backed by a single
// landscape + synthesis pass — no decomposition or debates.
package answer

import . "github.com/cmcoffee/gohort/core"

func init() { RegisterApp(new(AnswerAgent)) }

// KnowledgeDB is the central vector-index bucket. Set by the main
// package at startup so handleAsk can ingest finalized answers into
// search_knowledge.
var KnowledgeDB Database

// SharedDB holds the deployment-wide answer-body table (Question +
// Answer + Sources, keyed by record ID). Set by the main package at
// startup. Per-user listings go elsewhere — see userDB.
var SharedDB Database

// SharedTable is the kvlite table holding deployment-wide answer
// bodies. Mirrors knowledge.AnswerSharedTable; duplicated here to
// avoid an import cycle with the private package.
const SharedTable = "answer_shared"

// SharedRecord is the deployment-wide answer-body row. The same ID
// also keys the per-user index entry. Followups live per-user, not
// here — they're conversational extensions that don't belong in the
// shared knowledge index.
type SharedRecord struct {
	ID       string         `json:"ID"`
	Question string         `json:"Question"`
	Answer   string         `json:"Answer"`
	Sources  map[int]string `json:"Sources,omitempty"`
	Date     string         `json:"Date"`
}

// AnswerRecord is the per-user index row. The user's sidebar / history
// list reads this. Question is duplicated here (also lives in
// SharedRecord) so the listing renders without a cross-table join;
// the cost is a few KB per record at gohort scale. Answer/Sources
// are deliberately NOT stored here — fetched from SharedDB on demand.
type AnswerRecord struct {
	ID        string         `json:"ID"`
	Question  string         `json:"Question"`
	Topic     string         `json:"Topic,omitempty"`     // orchestrator-chosen topic for fact namespacing
	Date      string         `json:"Date"`
	Archived  bool           `json:"Archived,omitempty"`
	Followups []FollowupTurn `json:"Followups,omitempty"` // user/assistant pairs of follow-up Q&A on the original answer
}

// FollowupTurn is one user-assistant exchange after the initial answer.
// The follow-up chat sees the original answer + relevant facts + prior
// follow-ups as context, so each new turn can build on what's already
// been established.
type FollowupTurn struct {
	Role    string `json:"role"`    // "user" | "assistant"
	Content string `json:"content"`
	Date    string `json:"date"`    // RFC3339
}

func (r AnswerRecord) GetDate() string { return r.Date }

const answerTable = "answer_history"

// AnswerAgent is the app entry point.
type AnswerAgent struct {
	AppCore
}

func (T AnswerAgent) Name() string        { return "answer" }
func (T AnswerAgent) SystemPrompt() string { return "" }
func (T AnswerAgent) Desc() string {
	return "Quick: Web-researched answers for technical questions and mini how-tos."
}

func (T *AnswerAgent) Init() (err error) { return T.Flags.Parse() }

func (T *AnswerAgent) Main() (err error) {
	Log("Answer is a dashboard-only app. Start with:\n  gohort serve :8080")
	return nil
}
