// Package answer provides a quick-answer web app backed by a single
// landscape + synthesis pass — no decomposition or debates.
package answer

import . "github.com/cmcoffee/gohort/core"

func init() { RegisterApp(new(AnswerAgent)) }

// AnswerRecord is stored per answered question.
type AnswerRecord struct {
	ID       string         `json:"ID"`
	Question string         `json:"Question"`
	Answer   string         `json:"Answer"`
	Sources  map[int]string `json:"Sources,omitempty"`
	Date     string         `json:"Date"`
	Archived bool           `json:"Archived,omitempty"`
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
	Log("Answer is a web-only app. Start the dashboard with:\n  gohort --web :8080")
	return nil
}
