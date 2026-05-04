package servitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	docWorkspaceTable = "doc_workspaces"
	suppChunkTable    = "ws_supp_chunks"

	// suppChunkSize is the target char length of each embedded chunk.
	suppChunkSize = 800
	// suppChunkBudget is the max chars of supplement text injected per document per query.
	suppChunkBudget = 6000
)

// DocWorkspace is a persistent documentation context tied to an appliance.
// The mental model is "working document backed by live system access +
// attached supplements":
//
//   Draft       — the working document (markdown). The artifact you
//                 export. Edited directly OR grown by promoting Q&A
//                 answers into it.
//   Supplements — uploaded reference docs the LLM uses as context for
//                 answering questions. Optional appendix on export.
//   Entries     — Q&A session log. Browseable in the UI for historical
//                 reference; NOT included in export. The doc is the
//                 artifact, not the transcript.
//   Revisions   — point-in-time snapshots of Draft taken on every save.
//                 50-cap, oldest dropped FIFO. Mirrors techwriter's
//                 article-revision pattern so edits are undoable.
type DocWorkspace struct {
	ID          string                `json:"id"`
	ApplianceID string                `json:"appliance_id"`
	Name        string                `json:"name"`        // the initial question, used as title
	Entries     []WorkspaceEntry      `json:"entries"`     // session log; NOT exported (browseable in UI only)
	Supplements []WorkspaceSupplement `json:"supplements"` // attached reference documents
	Draft       string                `json:"draft"`       // working document (markdown) — the export artifact
	Revisions   []WorkspaceRevision   `json:"revisions"`   // snapshot history of Draft, oldest-first, capped
	Created     string                `json:"created"`
	Updated     string                `json:"updated"`
}

// WorkspaceRevision is a point-in-time snapshot of Draft, captured on
// every save where Draft actually changed. Lets the operator revert
// to a prior version without losing intermediate edits.
type WorkspaceRevision struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	Date string `json:"date"`
}

// maxWorkspaceRevisions caps the per-workspace revision history.
// Mirrors techwriter's maxArticleRevisions (50). Oldest-first ordering
// so trimming is a simple slice from the front.
const maxWorkspaceRevisions = 50

// WorkspaceEntry is one Q&A pair saved into a workspace.
type WorkspaceEntry struct {
	Question  string `json:"question"`
	Answer    string `json:"answer"`
	Timestamp string `json:"timestamp"`
}

// WorkspaceSupplement is a reference document attached to a workspace.
// Content holds the extracted plain text; SubPrompt tells the LLM how and
// when to reference the document during a session.
type WorkspaceSupplement struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SubPrompt  string `json:"sub_prompt"`
	Content    string `json:"content"`
	Processing bool   `json:"processing"` // true while vision extraction is running
	Added      string `json:"added"`
}

// --- DB helpers ---

func saveWorkspace(udb Database, ws DocWorkspace) {
	if udb == nil || ws.ID == "" {
		return
	}
	ws.Updated = time.Now().Format(time.RFC3339)
	udb.Set(docWorkspaceTable, ws.ID, ws)
}

func loadWorkspace(udb Database, id string) (DocWorkspace, bool) {
	var ws DocWorkspace
	if udb == nil {
		return ws, false
	}
	return ws, udb.Get(docWorkspaceTable, id, &ws)
}

func listWorkspaces(udb Database, applianceID string) []DocWorkspace {
	if udb == nil {
		return nil
	}
	var out []DocWorkspace
	for _, k := range udb.Keys(docWorkspaceTable) {
		var ws DocWorkspace
		if udb.Get(docWorkspaceTable, k, &ws) && ws.ApplianceID == applianceID {
			out = append(out, ws)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated > out[j].Updated })
	return out
}

func deleteWorkspace(udb Database, id string) {
	if udb == nil {
		return
	}
	udb.Unset(docWorkspaceTable, id)
}

// --- HTTP handlers ---

// handleWorkspaceCreate creates a new workspace seeded with one Q&A entry.
// The workspace name is the question text (trimmed to 120 chars).
func (T *Servitor) handleWorkspaceCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
		Question    string `json:"question"`
		Answer      string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	req.Answer = strings.TrimSpace(req.Answer)
	if req.ApplianceID == "" || req.Question == "" {
		http.Error(w, "appliance_id and question are required", http.StatusBadRequest)
		return
	}

	name := req.Question
	if len(name) > 120 {
		if idx := strings.LastIndexByte(name[:120], ' '); idx > 60 {
			name = name[:idx]
		} else {
			name = name[:120]
		}
		name += "…"
	}

	ws := DocWorkspace{
		ID:          UUIDv4(),
		ApplianceID: req.ApplianceID,
		Name:        name,
		Created:     time.Now().Format(time.RFC3339),
	}
	if req.Question != "" {
		ws.Entries = append(ws.Entries, WorkspaceEntry{
			Question:  req.Question,
			Answer:    req.Answer,
			Timestamp: ws.Created,
		})
	}
	saveWorkspace(udb, ws)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": ws.ID, "name": ws.Name})
}

// handleWorkspaceSave appends a Q&A entry to an existing workspace.
func (T *Servitor) handleWorkspaceSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		Question    string `json:"question"`
		Answer      string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, req.WorkspaceID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	ws.Entries = append(ws.Entries, WorkspaceEntry{
		Question:  strings.TrimSpace(req.Question),
		Answer:    strings.TrimSpace(req.Answer),
		Timestamp: time.Now().Format(time.RFC3339),
	})
	saveWorkspace(udb, ws)
	w.WriteHeader(http.StatusNoContent)
}

// handleWorkspaceList returns all workspaces for a given appliance.
func (T *Servitor) handleWorkspaceList(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	applianceID := r.URL.Query().Get("appliance_id")
	workspaces := listWorkspaces(udb, applianceID)
	type summary struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Entries int    `json:"entries"`
		Updated string `json:"updated"`
	}
	out := make([]summary, len(workspaces))
	for i, ws := range workspaces {
		out[i] = summary{ID: ws.ID, Name: ws.Name, Entries: len(ws.Entries), Updated: ws.Updated}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// handleWorkspace handles GET (fetch) and DELETE for a single workspace.
func (T *Servitor) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/workspace/")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ws, found := loadWorkspace(udb, id)
		if !found {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ws)
	case http.MethodDelete:
		deleteWorkspace(udb, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// chunkSupplementText splits text into ~suppChunkSize-char chunks at paragraph
// boundaries. Adjacent paragraphs are grouped until the size budget is reached.
func chunkSupplementText(text string) []string {
	paras := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	var chunks []string
	var cur strings.Builder
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if cur.Len() > 0 && cur.Len()+len(p) > suppChunkSize {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// ingestSupplementChunks embeds a supplement's text into the workspace chunk
// table so retrieveSupplementContext can do semantic lookup at query time.
// Runs asynchronously after upload; silently no-ops if embeddings are disabled.
func ingestSupplementChunks(ctx context.Context, udb Database, supp WorkspaceSupplement) {
	if udb == nil || supp.ID == "" {
		return
	}
	deleteSupplementChunks(udb, supp.ID)
	cfg := GetEmbeddingConfig()
	if !cfg.Enabled {
		return
	}
	now := time.Now().Format(time.RFC3339)
	for i, chunk := range chunkSupplementText(supp.Content) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		vec, err := Embed(ctx, chunk)
		if err != nil {
			continue
		}
		row := EmbeddedChunk{
			ID:       fmt.Sprintf("%s:%d", supp.ID, i),
			ReportID: supp.ID,
			Source:   "supplement",
			Section:  supp.Name,
			Text:     chunk,
			Vector:   vec,
			Model:    cfg.Model,
			Date:     now,
		}
		udb.Set(suppChunkTable, row.ID, row)
	}
}

// deleteSupplementChunks removes all stored chunks for a given supplement ID.
func deleteSupplementChunks(udb Database, suppID string) {
	if udb == nil {
		return
	}
	prefix := suppID + ":"
	for _, k := range udb.Keys(suppChunkTable) {
		if strings.HasPrefix(k, prefix) {
			udb.Unset(suppChunkTable, k)
		}
	}
}

// retrieveSupplementContext returns the most relevant portion of a supplement
// for the given query. Uses semantic chunk search when embeddings are available;
// falls back to a head-truncation with a paragraph-boundary cut.
func retrieveSupplementContext(ctx context.Context, udb Database, supp WorkspaceSupplement, query string, charBudget int) string {
	if charBudget <= 0 {
		charBudget = suppChunkBudget
	}
	cfg := GetEmbeddingConfig()
	if cfg.Enabled && udb != nil && query != "" {
		queryVec, err := Embed(ctx, query)
		if err == nil && len(queryVec) > 0 {
			type scored struct {
				text  string
				score float32
			}
			var hits []scored
			prefix := supp.ID + ":"
			for _, k := range udb.Keys(suppChunkTable) {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				var c EmbeddedChunk
				if !udb.Get(suppChunkTable, k, &c) || len(c.Vector) != len(queryVec) {
					continue
				}
				if s := Cosine(queryVec, c.Vector); s > 0 {
					hits = append(hits, scored{c.Text, s})
				}
			}
			if len(hits) > 0 {
				sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
				var b strings.Builder
				for _, h := range hits {
					if b.Len()+len(h.text)+2 > charBudget {
						break
					}
					if b.Len() > 0 {
						b.WriteString("\n\n")
					}
					b.WriteString(h.text)
				}
				if b.Len() > 0 {
					return b.String()
				}
			}
		}
	}
	// Fallback: head truncation at a paragraph boundary.
	if len(supp.Content) <= charBudget {
		return supp.Content
	}
	cut := supp.Content[:charBudget]
	if idx := strings.LastIndex(cut, "\n\n"); idx > charBudget/2 {
		cut = cut[:idx]
	}
	return cut + "\n\n[… document truncated — configure an embeddings model for full semantic retrieval]"
}

// buildSupplementContext retrieves relevant excerpts from all supplements for
// the current query (last user message) and assembles them into a single string
// ready for injection into the lead prompt.
//
// contextSize is the lead LLM's context window. Retrieval budget scales
// linearly with it — a 256K lead pulls in ~4× more supplement text than a
// 64K lead — but total supplement text is capped at 30K chars to keep lead
// token costs predictable.
func buildSupplementContext(ctx context.Context, udb Database, supplements []WorkspaceSupplement, messages []Message, contextSize int) string {
	if len(supplements) == 0 {
		return ""
	}
	// Extract the last user message as the retrieval query.
	var query string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			query = messages[i].Content
			break
		}
	}
	// Scale budget with context window.  Anchored at 64K → 6000 chars/doc.
	// Since supplement text eats lead-model tokens (which you pay for),
	// cap total at 30K chars (~5K tokens) regardless of context size.
	const (
		baseBudget  = 6000
		baseContext = 64000
		maxTotal    = 30000
	)
	if contextSize <= 0 {
		contextSize = baseContext
	}
	perDoc := baseBudget * contextSize / baseContext
	totalBudget := perDoc * len(supplements)
	if totalBudget > maxTotal {
		totalBudget = maxTotal
		perDoc = totalBudget / len(supplements)
	}
	var parts []string
	for _, s := range supplements {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		excerpt := retrieveSupplementContext(rctx, udb, s, query, perDoc)
		cancel()
		if excerpt == "" {
			continue
		}
		var b strings.Builder
		b.WriteString(fmt.Sprintf("### %s\n\n", s.Name))
		if s.SubPrompt != "" {
			b.WriteString(fmt.Sprintf("**Usage instruction:** %s\n\n", s.SubPrompt))
		}
		b.WriteString(excerpt)
		parts = append(parts, b.String())
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// extractPDFText extracts plain text from PDF bytes using pdftotext.
func extractPDFText(data []byte) (string, error) {
	path, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", Error("pdftotext not found — install poppler-utils to enable PDF extraction")
	}
	tmp, err := os.MkdirTemp("", "gohort-pdftext-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	pdfPath := filepath.Join(tmp, "input.pdf")
	if err := os.WriteFile(pdfPath, data, 0600); err != nil {
		return "", err
	}
	var out bytes.Buffer
	cmd := exec.Command(path, pdfPath, "-")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// extractPDFScreenshots renders each PDF page as a PNG and asks the vision LLM
// to describe pages that show UI elements (menus, dialogs, configuration screens).
// Returns a markdown section with described pages, or empty string if none found.
// At most maxVisionPages pages are processed to bound latency.
func extractPDFScreenshots(ctx context.Context, llm LLM, data []byte) string {
	if llm == nil {
		return ""
	}
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		return ""
	}
	tmp, err := os.MkdirTemp("", "gohort-pdf-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(tmp)

	// pdftoppm requires seekable input — write PDF to a temp file.
	pdfPath := filepath.Join(tmp, "input.pdf")
	if err := os.WriteFile(pdfPath, data, 0600); err != nil {
		return ""
	}

	const maxVisionPages = 30
	cmd := exec.CommandContext(ctx, pdftoppm,
		"-png", "-r", "120", "-l", fmt.Sprintf("%d", maxVisionPages),
		pdfPath, filepath.Join(tmp, "page"),
	)
	if err := cmd.Run(); err != nil {
		return ""
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		return ""
	}

	const visionPrompt = `Look at this page from a technical document or product manual.

If this page contains a user interface screenshot, configuration screen, dialog box, menu, form, table of settings, or any visual element showing software controls or options: describe it thoroughly. Include all visible field names, button labels, menu items, toggle states, column headers, and any values shown. Be specific enough that someone could reproduce the UI layout from your description alone.

If this page is plain text (prose, lists, code blocks, or headers with no visual UI elements): respond with exactly the word SKIP and nothing else.`

	var descriptions []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		imgData, err := os.ReadFile(filepath.Join(tmp, e.Name()))
		if err != nil {
			continue
		}
		resp, err := llm.Chat(ctx, []Message{
			{
				Role:    "user",
				Content: visionPrompt,
				Images:  [][]byte{imgData},
			},
		}, WithThink(false))
		if err != nil || resp == nil {
			continue
		}
		desc := strings.TrimSpace(resp.Content)
		if desc == "" || strings.EqualFold(desc, "SKIP") || strings.HasPrefix(strings.ToUpper(desc), "SKIP") {
			continue
		}
		// Strip page filename to a human-readable label (page-001 → Page 1).
		label := e.Name()
		label = strings.TrimSuffix(label, ".png")
		label = strings.TrimPrefix(label, "page-")
		if n := strings.TrimLeft(label, "0"); n != "" {
			label = "Page " + n
		}
		descriptions = append(descriptions, fmt.Sprintf("#### %s\n\n%s", label, desc))
	}

	if len(descriptions) == 0 {
		return ""
	}
	return "## UI Screenshots\n\n" + strings.Join(descriptions, "\n\n---\n\n")
}

// extractPDFTextOrPlain extracts plain text from a PDF or returns file bytes
// as a string for non-PDF files. No vision processing.
func extractPDFTextOrPlain(filename string, data []byte) (string, error) {
	if strings.ToLower(filepath.Ext(filename)) == ".pdf" {
		return extractPDFText(data)
	}
	return string(data), nil
}

// enrichSupplementWithScreenshots runs PDF vision extraction asynchronously,
// prepends the screenshot descriptions to the supplement's Content, saves
// the workspace, then triggers chunk embedding.
func enrichSupplementWithScreenshots(ctx context.Context, llm LLM, udb Database, wsID, suppID string, pdfData []byte) {
	// Mark processing=true so the UI can show a spinner.
	if ws, found := loadWorkspace(udb, wsID); found {
		for i := range ws.Supplements {
			if ws.Supplements[i].ID == suppID {
				ws.Supplements[i].Processing = true
				saveWorkspace(udb, ws)
				break
			}
		}
	}

	screenshots := extractPDFScreenshots(ctx, llm, pdfData)
	Log("[servitor] vision extraction for supplement %s: %d chars of screenshot descriptions", suppID, len(screenshots))

	ws, found := loadWorkspace(udb, wsID)
	if !found {
		return
	}
	for i := range ws.Supplements {
		if ws.Supplements[i].ID != suppID {
			continue
		}
		if screenshots != "" {
			ws.Supplements[i].Content = screenshots + "\n\n## Extracted Text\n\n" + ws.Supplements[i].Content
		}
		ws.Supplements[i].Processing = false
		saveWorkspace(udb, ws)
		ingestSupplementChunks(ctx, udb, ws.Supplements[i])
		return
	}
}

// handleSupplementAdd uploads a document and attaches it to a workspace.
// Accepts multipart/form-data: workspace_id, sub_prompt, file.
func (T *Servitor) handleSupplementAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	wsID := strings.TrimSpace(r.FormValue("workspace_id"))
	subPrompt := strings.TrimSpace(r.FormValue("sub_prompt"))
	if wsID == "" {
		http.Error(w, "workspace_id required", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, wsID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8<<20)) // 8 MB cap
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	// Extract text synchronously (fast).
	text, err := extractPDFTextOrPlain(fh.Filename, data)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	supp := WorkspaceSupplement{
		ID:        UUIDv4(),
		Name:      fh.Filename,
		SubPrompt: subPrompt,
		Content:   text,
		Added:     time.Now().Format(time.RFC3339),
	}
	ws.Supplements = append(ws.Supplements, supp)
	saveWorkspace(udb, ws)
	// Vision screenshot extraction and chunk embedding run asynchronously.
	// Screenshots are prepended to Content when ready; embedding follows.
	if strings.ToLower(filepath.Ext(fh.Filename)) == ".pdf" {
		go enrichSupplementWithScreenshots(AppContext(), T.LLM, udb, wsID, supp.ID, data)
	} else {
		go ingestSupplementChunks(AppContext(), udb, supp)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": supp.ID, "name": supp.Name})
}

// handleSupplementDelete removes a supplement from a workspace.
// Accepts JSON: workspace_id, supplement_id.
func (T *Servitor) handleSupplementDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		WorkspaceID  string `json:"workspace_id"`
		SupplementID string `json:"supplement_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, req.WorkspaceID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	out := ws.Supplements[:0]
	for _, s := range ws.Supplements {
		if s.ID != req.SupplementID {
			out = append(out, s)
		}
	}
	ws.Supplements = out
	saveWorkspace(udb, ws)
	deleteSupplementChunks(udb, req.SupplementID)
	w.WriteHeader(http.StatusNoContent)
}

// handleSupplementPrompt updates the sub_prompt of an existing supplement.
// Accepts JSON: workspace_id, supplement_id, sub_prompt.
func (T *Servitor) handleSupplementPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		WorkspaceID  string `json:"workspace_id"`
		SupplementID string `json:"supplement_id"`
		SubPrompt    string `json:"sub_prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, req.WorkspaceID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	for i := range ws.Supplements {
		if ws.Supplements[i].ID == req.SupplementID {
			ws.Supplements[i].SubPrompt = strings.TrimSpace(req.SubPrompt)
			break
		}
	}
	saveWorkspace(udb, ws)
	w.WriteHeader(http.StatusNoContent)
}

// synthesisSystemPrompt is the system prompt used when generating a documentation guide.
const synthesisSystemPrompt = `You are a senior technical writer producing an original documentation guide.

Your inputs are:
1. Q&A session notes — what users actually asked and the answers given. This drives the guide's structure.
2. UI screenshots — descriptions of product screens. Embed these inside the relevant procedures as you would in a real manual.
3. Reference text — extracted from admin/product documentation. Mine it for specific facts; do not reproduce its structure.

Rules:
- The guide structure must come from the user needs revealed in the Q&A, not from the reference document's table of contents.
- Embed UI screenshot descriptions in-context, inside the step or section where the user would see that screen — not in a separate screenshots appendix.
- Do NOT include credentials, passwords, API keys, or authentication tokens.
- Be direct and authoritative. No hedging, no preamble, no meta-commentary.
- Output clean markdown: # title, ## sections, ### subsections. Begin immediately with the # title.`

// splitSupplementContent separates the vision-extracted screenshot descriptions from
// the plain text body. enrichSupplementWithScreenshots stores them as:
//
//	## UI Screenshots\n\n<descriptions>\n\n## Extracted Text\n\n<body>
//
// Returns (screenshots, text). If no screenshot section is present, screenshots is "".
func splitSupplementContent(content string) (screenshots, text string) {
	const screenshotHeader = "## UI Screenshots"
	const textHeader = "## Extracted Text"
	si := strings.Index(content, screenshotHeader)
	ti := strings.Index(content, textHeader)
	if si < 0 {
		return "", strings.TrimSpace(content)
	}
	if ti < 0 || ti < si {
		return strings.TrimSpace(content[si:]), ""
	}
	return strings.TrimSpace(content[si:ti]), strings.TrimSpace(content[ti+len(textHeader):])
}

// buildSynthesisPrompt assembles the user-turn content for the LLM synthesis call.
// Screenshots are separated from reference text and presented as a distinct resource
// so the LLM embeds them in-context rather than treating them as document structure.
func buildSynthesisPrompt(ws DocWorkspace) string {
	var b strings.Builder

	b.WriteString("Topic: ")
	b.WriteString(ws.Name)
	b.WriteString("\n\n")

	// Q&A — just the answers; questions are listed lightly as context for what was asked.
	b.WriteString("## What users asked and the answers (drives guide structure)\n\n")
	if len(ws.Entries) == 0 {
		b.WriteString("(No Q&A entries recorded.)\n\n")
	} else {
		for _, e := range ws.Entries {
			if q := strings.TrimSpace(e.Question); q != "" {
				b.WriteString("Asked: ")
				b.WriteString(q)
				b.WriteString("\n")
			}
			if a := strings.TrimSpace(e.Answer); a != "" {
				b.WriteString(a)
				b.WriteString("\n\n")
			}
		}
	}

	// Separate screenshots from reference text across all supplements.
	var allScreenshots []string
	var refParts []string
	const perDocCap = 60000
	for _, s := range ws.Supplements {
		c := strings.TrimSpace(s.Content)
		if c == "" {
			continue
		}
		shots, body := splitSupplementContent(c)
		if shots != "" {
			allScreenshots = append(allScreenshots, fmt.Sprintf("From %s:\n\n%s", s.Name, shots))
		}
		if body != "" {
			if len(body) > perDocCap {
				body = body[:perDocCap] + "\n\n[… truncated]"
			}
			var rb strings.Builder
			rb.WriteString("From ")
			rb.WriteString(s.Name)
			rb.WriteString(":\n\n")
			if s.SubPrompt != "" {
				rb.WriteString("Usage note: ")
				rb.WriteString(strings.TrimSpace(s.SubPrompt))
				rb.WriteString("\n\n")
			}
			rb.WriteString(body)
			refParts = append(refParts, rb.String())
		}
	}

	if len(allScreenshots) > 0 {
		b.WriteString("## UI screenshots — embed these inside the relevant procedures, not in a separate section\n\n")
		b.WriteString(strings.Join(allScreenshots, "\n\n---\n\n"))
		b.WriteString("\n\n")
	}

	if len(refParts) > 0 {
		b.WriteString("## Reference text — extract specific facts, do not reproduce the document's structure\n\n")
		b.WriteString(strings.Join(refParts, "\n\n---\n\n"))
		b.WriteString("\n\n")
	}

	b.WriteString("Write the complete documentation guide now. Begin with # title.")
	return b.String()
}

// handleWorkspaceSynthesize runs an LLM synthesis pass over all Q&A entries and
// supplement content to produce a complete documentation guide. The result is
// streamed via SSE and saved to ws.Draft on completion.
func (T *Servitor) handleWorkspaceSynthesize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.WorkspaceID == "" {
		http.Error(w, "workspace_id required", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, req.WorkspaceID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	if T.LLM == nil {
		http.Error(w, "LLM not configured", http.StatusServiceUnavailable)
		return
	}

	sid := UUIDv4()
	ctx, cancel := context.WithCancel(AppContext())
	label := "Synthesizing: " + ws.Name
	if len(label) > 80 {
		label = label[:80]
	}
	probeSessions.Register(sid, label, cancel)

	go func() {
		defer func() {
			probeSessions.AppendEvent(sid, probeEvent{Kind: "done"}, true)
			probeSessions.ScheduleCleanup(sid)
		}()

		emit(sid, probeEvent{Kind: "status", Text: "Building synthesis prompt…"})
		userPrompt := buildSynthesisPrompt(ws)

		emit(sid, probeEvent{Kind: "status", Text: "Generating documentation guide…"})
		resp, err := T.WorkerChat(ctx, []Message{
			{Role: "system", Content: synthesisSystemPrompt},
			{Role: "user", Content: userPrompt},
		}, WithThink(true))
		if err != nil {
			probeSessions.AppendEvent(sid, probeEvent{Kind: "error", Text: err.Error()}, true)
			return
		}
		if resp == nil || strings.TrimSpace(resp.Content) == "" {
			probeSessions.AppendEvent(sid, probeEvent{Kind: "error", Text: "LLM returned empty response"}, true)
			return
		}

		draft := strings.TrimSpace(resp.Content)
		// Save draft to the workspace.
		if fresh, ok := loadWorkspace(udb, req.WorkspaceID); ok {
			fresh.Draft = draft
			saveWorkspace(udb, fresh)
		}
		// Emit draft event — the UI will populate the draft textarea.
		emit(sid, probeEvent{Kind: "draft", Text: draft})
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sid})
}

// workspaceViewPage is the standalone HTML page returned by handleWorkspaceView.
// It renders the draft markdown using marked.js so the user can select-all and
// paste rich text directly into Confluence or any other rich-text editor.
const workspaceViewPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>__TITLE__</title>
<script src="https://cdn.jsdelivr.net/npm/marked@11.2.0/marked.min.js"></script>
<style>
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;font-size:15px;line-height:1.6;color:#1a1a1a;background:#fff;padding:2rem}
  #toolbar{position:sticky;top:0;background:#fff;border-bottom:1px solid #e0e0e0;padding:0.5rem 0 0.75rem;margin-bottom:1.5rem;display:flex;gap:0.75rem;align-items:center;z-index:10}
  #toolbar button{padding:0.35rem 1rem;border:1px solid #bbb;border-radius:4px;background:#f5f5f5;cursor:pointer;font-size:0.85rem}
  #toolbar button:hover{background:#e8e8e8}
  #toolbar .hint{font-size:0.8rem;color:#777}
  #content{max-width:860px;margin:0 auto}
  #content h1{font-size:1.8rem;margin:1.5rem 0 0.75rem;border-bottom:2px solid #e0e0e0;padding-bottom:0.4rem}
  #content h2{font-size:1.4rem;margin:1.5rem 0 0.5rem;border-bottom:1px solid #e8e8e8;padding-bottom:0.25rem}
  #content h3{font-size:1.15rem;margin:1.2rem 0 0.4rem}
  #content h4{font-size:1rem;margin:1rem 0 0.3rem}
  #content p{margin:0.6rem 0}
  #content ul,#content ol{margin:0.5rem 0 0.5rem 1.5rem}
  #content li{margin:0.2rem 0}
  #content code{background:#f4f4f4;padding:0.1em 0.35em;border-radius:3px;font-family:monospace;font-size:0.88em}
  #content pre{background:#f4f4f4;padding:1rem;border-radius:4px;overflow-x:auto;margin:0.75rem 0}
  #content pre code{background:none;padding:0}
  #content table{border-collapse:collapse;width:100%;margin:0.75rem 0}
  #content th,#content td{border:1px solid #d0d0d0;padding:0.45rem 0.75rem;text-align:left}
  #content th{background:#f5f5f5;font-weight:600}
  #content blockquote{border-left:3px solid #ccc;margin:0.5rem 0;padding:0.25rem 1rem;color:#555}
  #content hr{border:none;border-top:1px solid #e0e0e0;margin:1.5rem 0}
  #content strong{font-weight:600}
</style>
</head>
<body>
<div id="toolbar">
  <button onclick="selectAll()">Select All</button>
  <button onclick="copyContent(this)">Copy</button>
  <span class="hint">Select All → Copy → paste into Confluence</span>
</div>
<div id="content"></div>
<script>
var md = __MARKDOWN__;
marked.setOptions({breaks:true,gfm:true});
document.getElementById('content').innerHTML = marked.parse(md);
function selectAll(){
  var el=document.getElementById('content');
  var r=document.createRange();r.selectNodeContents(el);
  var s=window.getSelection();s.removeAllRanges();s.addRange(r);
}
function copyContent(btn){
  var el=document.getElementById('content');
  if(navigator.clipboard&&window.ClipboardItem){
    var html=el.innerHTML;
    var blob=new Blob([html],{type:'text/html'});
    navigator.clipboard.write([new ClipboardItem({'text/html':blob})]).then(function(){
      btn.textContent='Copied!';setTimeout(function(){btn.textContent='Copy';},1500);
    }).catch(function(){selectAll();document.execCommand('copy');btn.textContent='Copied!';setTimeout(function(){btn.textContent='Copy';},1500);});
  } else {
    selectAll();document.execCommand('copy');
    btn.textContent='Copied!';setTimeout(function(){btn.textContent='Copy';},1500);
  }
}
</script>
</body>
</html>`

// handleWorkspaceView renders the workspace draft as a standalone HTML page.
// The page uses marked.js to display the markdown and provides Select All / Copy
// buttons so the content can be pasted as rich text into Confluence.
func (T *Servitor) handleWorkspaceView(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, id)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	content := strings.TrimSpace(ws.Draft)
	if content == "" {
		// View page falls through to draft + supplement appendix when
		// the draft is genuinely empty — gives the operator something
		// to look at instead of "no content."
		content = buildExportContent(ws, true)
	}
	if content == "" {
		http.Error(w, "workspace has no content", http.StatusUnprocessableEntity)
		return
	}
	contentJSON, _ := json.Marshal(content)
	page := strings.ReplaceAll(workspaceViewPage, "__TITLE__", ws.Name)
	page = strings.ReplaceAll(page, "__MARKDOWN__", string(contentJSON))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprint(w, page)
}

// handleWorkspaceDraft saves the editable draft for a workspace.
func (T *Servitor) handleWorkspaceDraft(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		Draft       string `json:"draft"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, req.WorkspaceID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	// Snapshot the prior Draft into Revisions before overwriting, but
	// only when it actually changed AND was non-empty. Empty drafts
	// don't deserve a revision; identical re-saves don't either (would
	// fill the cap with duplicates of the same body).
	prior := strings.TrimSpace(ws.Draft)
	incoming := strings.TrimSpace(req.Draft)
	if prior != "" && prior != incoming {
		ws.Revisions = append(ws.Revisions, WorkspaceRevision{
			ID:   UUIDv4(),
			Body: ws.Draft,
			Date: time.Now().Format(time.RFC3339),
		})
		// FIFO trim — drop from the front when over cap.
		if len(ws.Revisions) > maxWorkspaceRevisions {
			ws.Revisions = ws.Revisions[len(ws.Revisions)-maxWorkspaceRevisions:]
		}
	}
	ws.Draft = req.Draft
	saveWorkspace(udb, ws)
	w.WriteHeader(http.StatusNoContent)
}

// handleWorkspaceRevisions returns the revision history for a workspace,
// newest first (UI-friendly ordering). Bodies are included so the UI
// can preview without a second roundtrip.
func (T *Servitor) handleWorkspaceRevisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, id)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	// Reverse the slice so newest is first — storage is oldest-first
	// for cheap FIFO trim, but the UI wants newest-first for "show me
	// recent edits."
	revs := make([]WorkspaceRevision, len(ws.Revisions))
	for i, r := range ws.Revisions {
		revs[len(ws.Revisions)-1-i] = r
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(revs)
}

// handleWorkspaceRevert restores a prior revision into Draft, also
// snapshotting the current Draft into Revisions first so the revert
// itself is undoable.
func (T *Servitor) handleWorkspaceRevert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		RevisionID  string `json:"revision_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ws, found := loadWorkspace(udb, req.WorkspaceID)
	if !found {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	var target *WorkspaceRevision
	for i := range ws.Revisions {
		if ws.Revisions[i].ID == req.RevisionID {
			target = &ws.Revisions[i]
			break
		}
	}
	if target == nil {
		http.Error(w, "revision not found", http.StatusNotFound)
		return
	}
	// Snapshot current Draft so the revert is itself undoable.
	if strings.TrimSpace(ws.Draft) != "" && ws.Draft != target.Body {
		ws.Revisions = append(ws.Revisions, WorkspaceRevision{
			ID:   UUIDv4(),
			Body: ws.Draft,
			Date: time.Now().Format(time.RFC3339),
		})
		if len(ws.Revisions) > maxWorkspaceRevisions {
			ws.Revisions = ws.Revisions[len(ws.Revisions)-maxWorkspaceRevisions:]
		}
	}
	ws.Draft = target.Body
	saveWorkspace(udb, ws)
	w.WriteHeader(http.StatusNoContent)
}
