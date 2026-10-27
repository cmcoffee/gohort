package codewriter

// The chat beside the editor is the Code Writer: an app agent in the same
// chat every agent has (agenthost.Chat), so it streams, keeps its
// conversations, stops, edits and retries a message, takes a note mid-turn,
// and shows its tool calls, like every other app's assistant.
//
// What is codewriter's own is the send. Each one carries the editor as it is
// on screen (the panel's send context, below), which rides the newest message
// for that turn only: the model always reads the file as it is now, and the
// conversation never stores a copy of it. A change to the file comes back as
// a propose_code call, which the panel opens as a diff against the editor;
// nothing changes until the user applies it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/appagents"
)

const codeWriterAgentID = "app-codewriter"

func init() {
	appagents.RegisterAppAgent(appagents.AppAgentSpec{
		ID:          codeWriterAgentID,
		OwningApp:   "CodeWriter",
		Name:        "Code Writer",
		Description: "Writes and changes the code open in CodeWriter, proposing each change as a diff to apply.",
		Hidden:      true, // reached through CodeWriter, not the agent picker
		// The turn is the editor's: propose_code and whatever the picked
		// sources lend, plus the two ask tools.
		ExactTools:   true,
		AllowedTools: []string{"ask_user", "ask_user_form"},
		Prompt:       codeWriterPrompt,
	})
}

const codeWriterPrompt = `You are the Code Writer, the assistant beside the CodeWriter editor. You help with any coding task: writing scripts, queries, configs and code in any language; transforming, converting or reformatting code and data (an export into an import, CSV into SQL, reshaping JSON); fixing, changing or refactoring code; one-liners, pipelines and admin tasks.

THE EDITOR: each message from the user carries the editor as it is right now, under "Editor (as it is now)": the file's name, its language, its contents, and the reference context the user keeps beside it. That is the file you are working on. A copy in an earlier turn is out of date.

CHANGING THE FILE: call propose_code with the WHOLE new file and a one-line summary of the change. Never a fragment or a diff: the user sees your file as a diff against the editor and applies it or not. After proposing, say in a sentence or two what changed and why; do not paste the file into your reply as well.

WHEN TO PROPOSE: when the user asks for a change, propose it. When they ask a question, want something explained, or want to talk an approach through, answer in prose; short inline snippets are fine. When it is unclear which they want, answer, and if a change to the file would help, propose that too: a proposal is only a suggestion, and nothing changes until the user applies it.

When a request is ambiguous, take the most common or useful reading and say what you assumed. Follow the user's instructions: if they ask you to transform, rewrite or convert something, do it.`

// codeWriterChat is where the chat's endpoints live, under the app.
var codeWriterChat = agenthost.Chat{Prefix: "chat/", Settings: true, Back: "/codewriter"}

// chatEditor is the editor a send carries: the panel's send context
// (CodeEditorPanel), read at the moment the message was sent.
type chatEditor struct {
	Name        string               `json:"name"`
	Lang        string               `json:"lang"`
	Code        string               `json:"code"`
	Context     string               `json:"context"`
	Collections []string             `json:"collections"`
	References  []ReferenceSelection `json:"references"`
	// Writer is the configured WRITER the turn runs under (writers.go). Wire
	// name is the panel's generic `profile`; the app's own name for the
	// concept is Writer.
	Writer string `json:"profile"`
}

// chatSendFields are the fields of a send that are codewriter's: the editor,
// and the message it is about.
type chatSendFields struct {
	Editor  chatEditor `json:"editor"`
	Message string     `json:"message"`
}

// dispatchChat answers the chat's endpoints other than the send: sessions,
// cancel, notes, confirmations, runs and the agent's settings.
func (T *CodeWriterAgent) dispatchChat(w http.ResponseWriter, r *http.Request) {
	orch := agenthost.Get()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := orch.LookupAppAgent(user, codeWriterAgentID)
	if !ok {
		http.Error(w, "code writer agent unavailable", http.StatusServiceUnavailable)
		return
	}
	if !orch.ServeAppChat(w, r, agent, codeWriterChat, strings.TrimPrefix(r.URL.Path, "/"), "") {
		http.NotFound(w, r)
	}
}

// handleChatSend runs one turn of the Code Writer on the editor the send
// carries.
func (T *CodeWriterAgent) handleChatSend(w http.ResponseWriter, r *http.Request) {
	orch := agenthost.Get()
	if orch == nil {
		http.Error(w, "orchestrate not initialized", http.StatusServiceUnavailable)
		return
	}
	user, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agent, ok := orch.LookupAppAgent(user, codeWriterAgentID)
	if !ok {
		http.Error(w, "code writer agent unavailable", http.StatusServiceUnavailable)
		return
	}
	// Read the editor from the body and put it back: the chat reads the
	// same body for the message itself.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var send chatSendFields
	_ = json.Unmarshal(raw, &send)
	ed := send.Editor

	// The route stage the admin sets for CodeWriter still decides the tier.
	if RouteToLead("app.codewriter") {
		agent.LeadModel = true
	}
	var writer WriterRecord
	if strings.TrimSpace(ed.Writer) != "" {
		writer, _ = loadWriter(udb, ed.Writer)
	}
	// Standing material rides the system prompt: it changes when the user
	// changes it, not every turn. The writer's note goes after the agent's
	// own rules on purpose: its sources govern WHAT the code says, the
	// rules above govern what a reply looks like.
	agent.OrchestratorPrompt += T.buildValuePrompt() + writer.sourceBlock() + DocRulesSection(udb, "codewriter")

	pub := &agenthost.Publisher{}
	turn, refTools := T.editorTurnContext(r.Context(), user, send, writer)
	tools := append(editTools(pub, send), refTools...)
	orch.PublicHandleSendWithAppToolsPublishing(w, agenthost.WithTurnContext(r, turn), agent, tools, pub)
}

// editTools is the turn's way to change the file: propose_code, unless the
// file is a regex, whose "code" is a pattern the diff can't usefully review
// and is answered in the reply instead. Whether a turn proposes is the
// agent's call: a proposal changes nothing until the user applies it.
func editTools(pub *agenthost.Publisher, send chatSendFields) []AgentToolDef {
	if send.Editor.Lang == "regex" {
		return nil
	}
	return []AgentToolDef{proposeCodeTool(pub, send.Editor.Lang)}
}

// editorTurnContext is what the turn's newest message carries about the
// editor, and the tools the picked sources lend the turn.
func (T *CodeWriterAgent) editorTurnContext(ctx context.Context, user string, send chatSendFields, writer WriterRecord) (string, []AgentToolDef) {
	ed := send.Editor
	regex := ed.Lang == "regex"
	var b strings.Builder
	b.WriteString("Editor (as it is now):\n")
	name := strings.TrimSpace(ed.Name)
	if name == "" {
		name = "(not named yet)"
	}
	lang := ed.Lang
	if lang == "" {
		lang = "code"
	}
	fmt.Fprintf(&b, "Name: %s\nLanguage: %s\n", name, lang)
	switch {
	case ed.Code == "" && regex:
		b.WriteString("(No test samples yet.)\n")
	case ed.Code == "":
		b.WriteString("(The editor is empty: this is a new file.)\n")
	case regex:
		f := DocFence(ed.Code)
		fmt.Fprintf(&b, "Test samples (one per line; lines prefixed `+` should match, `-` should not match, unprefixed are ambiguous):\n%s\n%s\n%s\n", f, ed.Code, f)
	default:
		f := DocFence(ed.Code)
		fmt.Fprintf(&b, "%s%s\n%s\n%s\n", f, lang, ed.Code, f)
	}
	if ed.Context != "" {
		label := "Reference context (table schemas, docs, notes)"
		if regex {
			label = "Target description (what the pattern should match)"
		}
		f := DocFence(ed.Context)
		fmt.Fprintf(&b, "\n%s:\n%s\n%s\n%s\n", label, f, ed.Context, f)
	}
	// Reference Collections: best-effort RAG over the picked Document
	// Collections. Degrades silently on any miss (no embedding backend, an
	// empty corpus) so the chat always works.
	if len(ed.Collections) > 0 {
		// Collections live in the shared collections home, NOT codewriter's
		// own bucket: reach them via CollectionsDB().
		b.WriteString(formatCollectionRefs(SearchCollections(ctx, CollectionsDB(), user, ed.Collections, send.Message, 6)))
	}
	// Reference sources: the writer's standing sources first, then this
	// turn's pick. The same item picked in both places is one reference,
	// fetched once and its tools built once: twice put every tool name in
	// the request twice, which some providers refuse outright.
	refs := uniqueReferences(append(append(ReferenceSelections{}, writer.Sources...), ed.References...))
	var refTools []AgentToolDef
	if len(refs) > 0 {
		if text := FetchReferences(ctx, user, send.Message, refs); text != "" {
			b.WriteString("\n" + text + "\n")
		}
		for _, sel := range refs {
			refTools = append(refTools, ReferenceItemTools(user, sel.Kind, sel.ItemID)...)
		}
		if len(refTools) > 0 {
			b.WriteString("\nThe attached reference source also provides tools. Use them when the reference above doesn't answer the question, or when the user asks about the CURRENT state of the system: the investigate tool runs a live session when gathered knowledge isn't enough.\n")
		}
	}
	switch {
	case regex:
		b.WriteString("\nThe user is building a regular expression: the editor holds the test samples and the context describes what the pattern should match. Answer in your reply with a single regex in a fenced ```regex block, then a short explanation naming each capture group and why each sample matches or doesn't. Default to PCRE-compatible syntax unless the user names a flavor (ERE, Go re2, JavaScript, ...). Do not wrap the regex in delimiters like /.../ unless the user asked for a specific language's literal syntax.\n")
	case ed.Lang != "":
		fmt.Fprintf(&b, "\nThe user is working in %s. Default to %s for code unless they say otherwise.\n", ed.Lang, ed.Lang)
	}
	return strings.TrimRight(b.String(), "\n"), refTools
}

// proposeCodeTool is propose_code: the whole new file, put in the
// conversation as a card that opens a diff against the editor.
func proposeCodeTool(pub *agenthost.Publisher, lang string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "propose_code",
			Description: "Propose a change to the file open in the editor. Give the WHOLE new file, never a fragment or a diff, and a one-line summary of what changed. The user sees it as a diff against the editor and applies it or not; nothing changes until they do.",
			Parameters: map[string]ToolParam{
				"code":    {Type: "string", Description: "The complete new contents of the file."},
				"summary": {Type: "string", Description: "One line: what the change does."},
			},
			Required: []string{"code", "summary"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			code, _ := args["code"].(string)
			summary, _ := args["summary"].(string)
			if strings.TrimSpace(code) == "" {
				return "", fmt.Errorf("code is empty: give the whole new file")
			}
			code = strings.TrimSuffix(code, "\n")
			pub.Publish(agenthost.Block{
				Type: "editor_proposal", ID: UUIDv4(),
				Text: strings.TrimSpace(summary),
				Data: map[string]string{"code": code, "lang": lang},
			})
			return "Proposed. The user sees it as a diff against the editor and applies it or not. Do not repeat the file in your reply: say briefly what changed.", nil
		},
	}
}
