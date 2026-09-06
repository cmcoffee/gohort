package orchestrate

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// agentMutationTools is the set of agent-CRUD tools whose successful
// invocation invalidates the agent dropdown's option list. Kept here
// (next to the wrapper that reads it) rather than spread across the
// individual tool files because the wrapper is what owns the SSE.
var agentMutationTools = map[string]bool{
	"create_agent": true,
	"update_agent": true,
	"delete_agent": true,
	"clone_agent":  true,
	// add_tool mutates AgentRecord.Tools (the bundled-custom-tools
	// list). The Tools button label reads that count, so include the
	// signal here too — the page-side listener refreshes the count
	// without the user having to reopen the agent dropdown.
	"add_tool": true,
	// tool_def writes to session_temp_tools. The Tools button label
	// also reflects session tools, so its grouped surface gets the
	// same refresh signal. Fires on every tool_def action, including
	// list/help/delete — cheap to refresh, no harm in over-firing.
	"tool_def": true,
}

// hiddenToolChips lists tools whose invocation should NOT render a
// tool_call / tool_result chip in the conversation pane (and should
// not produce activity-pane rows either). These are tools whose
// effect is already visible to the user through a richer surface —
// respond_directly's "result" is the final reply text the user reads;
// send_status's status line shows up via the framework's status
// channel. A chip alongside the better surface is just noise.
//
// The wrapper still runs the handler, drains any attachments the
// tool produced, and records the call into the per-turn tool log so
// follow-up step prompts can reference it. Only the user-facing
// emissions are skipped.
var hiddenToolChips = map[string]bool{
	"respond_directly": true,
	"send_status":      true,
	"plan_set":         true,
	"ask_user_form":    true,
}

// sessAttachmentCounts snapshots the session's attachment slice lengths
// so wrapToolsForActivity can detect new entries the tool appended
// during its run. nil-safe — control tools may pass nil sess when they
// have no attachment surface. Reads are race-free because tools run
// synchronously within the agent loop goroutine (mirrors chat's
// flushNewImages assumption).
func sessAttachmentCounts(sess *ToolSession) (int, int, int) {
	if sess == nil {
		return 0, 0, 0
	}
	return len(sess.Images), len(sess.Videos), len(sess.Files)
}

// flushNewAttachments emits SSE events for any image / video / file
// entries the tool appended to sess past the snapshot lengths. Each
// payload carries msgID so the runtime renders them under the bubble
// the tool fired from. Mirrors chat's flushNewImages pattern but
// targets a specific assistant bubble instead of "the current one".
func (t *chatTurn) flushNewAttachments(sess *ToolSession, msgID string, imgN, vidN, fileN int) {
	if sess == nil {
		return
	}
	// Use the session's claim-API: each call atomically claims an
	// exclusive range of new attachments and advances the per-
	// session flushed marker. This prevents the parallel-goroutine
	// race where two tool-call goroutines each captured len=0
	// before either appended, then each flushed the FULL slice
	// (causing 2× delivery on 2 distinct attach calls, 3× on 3, etc).
	// imgN/vidN/fileN are kept in the signature for backward-compat
	// callers but ignored — the session-level markers are
	// authoritative.
	_ = imgN
	_ = vidN
	_ = fileN
	for _, b64 := range sess.ClaimUnflushedImages() {
		t.sse.Send(map[string]any{
			"kind":   "image",
			"msg_id": msgID,
			"data":   b64,
		})
		// The SSE event paints it now; keeping the bytes is what lets the thread
		// paint it again after a reload. The claim above is the only place every
		// image a turn delivers passes through, whichever of the turn's step
		// sessions attached it.
		t.keepDelivered(b64)
	}
	for _, b64 := range sess.ClaimUnflushedVideos() {
		t.sse.Send(map[string]any{
			"kind":   "video",
			"msg_id": msgID,
			"data":   b64,
		})
	}
	for _, f := range sess.ClaimUnflushedFiles() {
		t.sse.Send(map[string]any{
			"kind":      "file",
			"msg_id":    msgID,
			"name":      f.Name,
			"mime_type": f.MimeType,
			"data":      f.Data,
			"size":      f.Size,
		})
	}
}

// recoverClaimedDelivery ships a file the reply claims to have sent but that
// nothing actually attached.
//
// The model produced the picture, wrote "here you go", and skipped the attach
// call that delivers it. The file is right there in the workspace; the only
// thing missing is the second step. So take it — and only when the reply makes
// an explicit delivery claim, because that claim is the model's own signal that
// this file was meant for the user rather than an intermediate it produced
// along the way.
//
// No-op when the turn already delivered something: a reply that says "here it
// is" alongside a real attachment is describing that attachment.
func (t *chatTurn) recoverClaimedDelivery(reply string) {
	t.attMu.Lock()
	already := len(t.deliveredAtt)
	t.attMu.Unlock()
	if already > 0 {
		return
	}
	sess := t.newToolSession()
	staged := recoverStagedDeliverable(sess, reply, turnProducedDeliverable(t.persistedToolCalls()))
	if staged == "" {
		return
	}
	b64s := resolveWorkspaceImages(sess, []string{staged})
	if len(b64s) == 0 {
		return
	}
	Log("[chat] reply claimed a delivery but attached nothing — backstop attaching staged %q", staged)
	// Paint it under the bubble the reply just streamed into, the same way a
	// tool-delivered image arrives.
	kind := "image"
	if isVideoAttachment(staged) {
		kind = "video"
	}
	msgID := t.getCurrentMsgID()
	for _, b64 := range b64s {
		t.sse.Send(map[string]any{"kind": kind, "msg_id": msgID, "data": b64})
		if kind == "image" {
			t.keepDelivered(b64)
		}
	}
}

// keepDelivered stores one delivered image and remembers its id for the
// message that will carry it. Best-effort: failing here costs the picture on
// reload, never the delivery — that already happened on the stream above.
func (t *chatTurn) keepDelivered(b64 string) {
	ids := keepDeliveredAttachments(t.user, []string{b64})
	if len(ids) == 0 {
		return
	}
	t.attMu.Lock()
	t.deliveredAtt = append(t.deliveredAtt, ids...)
	t.attMu.Unlock()
}

// takeDeliveredAttachments hands the ids to the message being persisted and
// clears them, so a later message in the same turn can't claim them twice.
func (t *chatTurn) takeDeliveredAttachments() []string {
	t.attMu.Lock()
	defer t.attMu.Unlock()
	out := t.deliveredAtt
	t.deliveredAtt = nil
	return out
}

// ensureBubbleForTool returns the active orchestrator bubble id,
// materializing an empty assistant bubble if no text has streamed in
// this round yet. That way a tool-only round still gives the inline
// tool chip somewhere to attach.
func (t *chatTurn) ensureBubbleForTool() string {
	id := t.getCurrentMsgID()
	if id != "" {
		return id
	}
	id = fmt.Sprintf("orch-%d", time.Now().UnixNano())
	t.sse.Send(map[string]any{
		"kind": "message",
		"role": "assistant",
		"id":   id,
		"text": "",
	})
	t.setCurrentMsgID(id)
	return id
}

// emitToolCall emits the inline tool-chip SSE event Agency renders
// in the conversation pane. namePrefix is optional — when set
// (variadic), it prepends to the chip's name field so sub-agent
// dispatches show as "↳ [Target Name] knowledge_search" instead
// of blending in with the parent's own tool calls. The canonical
// tool name passed to the LLM is unaffected — only the display
// chip changes.
func (t *chatTurn) emitToolCall(msgID, name string, args map[string]any, suffix string, namePrefix ...string) string {
	displayName := name
	if len(namePrefix) > 0 && namePrefix[0] != "" {
		displayName = namePrefix[0] + name
	}
	// Generate a per-dispatch call_id so emitToolResult can be paired
	// back to THIS exact tool_call regardless of arrival order. Without
	// this the client falls back to "last unmatched" positional pairing
	// (see core/ui/runtime.go tool_result case), which silently
	// mis-attributes when calls don't strictly settle in emission order
	// (async dispatch, parallel tool calls in one model response,
	// cached short-circuits emitted alongside live calls). The caller
	// passes the returned ID to emitToolResult; both events ride
	// together so the client can correlate by ID.
	callID := UUIDv4()
	// args is the COMPACT one-line summary shown in the inline chip
	// — values clipped at 60 chars so a row of tool calls stays
	// readable. args_full is the unclipped, structured view rendered
	// inside the expanded <details> body so the user can see the
	// actual command / script / arguments without re-running the
	// turn. Without args_full, debugging "what did the Builder
	// actually run?" required reading the server log.
	callArgs := args
	if callArgs == nil {
		callArgs = map[string]any{}
	}
	payload := map[string]any{
		"kind":      "tool_call",
		"msg_id":    msgID,
		"call_id":   callID,
		"name":      displayName,
		"args":      summarizeToolArgs(args) + suffix,
		"args_full": callArgs,
	}
	// Mark pipeline-mode temp tools so the client can decorate the
	// pill differently (today: an emoji prefix). Detected by walking
	// the session's TempTools list — cheap, the list is small.
	if kind := t.toolKindFor(name); kind != "" {
		payload["tool_kind"] = kind
	}
	t.sse.Send(payload)
	return callID
}

// toolKindFor returns "pipeline" when name belongs to a pipeline-mode
// TempTool in either the session-defined or persistent pool. Empty
// string for everything else (regular registered tools, shell/api
// temp tools).
func (t *chatTurn) toolKindFor(name string) string {
	if t.session == nil {
		return ""
	}
	// Walk session-resolved TempTools first (faster, hits the common
	// case). Persistent store is consulted only if not found inline.
	// Cheap either way at gohort scale.
	for _, attached := range LoadSessionTempTools(t.udb, t.session.ID) {
		if attached.Name == name && attached.Mode == TempToolModePipeline {
			return "pipeline"
		}
	}
	for _, p := range LoadPersistentTempTools(t.udb, t.user) {
		if p.Tool.Name == name && p.Tool.Mode == TempToolModePipeline {
			return "pipeline"
		}
	}
	return ""
}

func (t *chatTurn) emitToolResult(msgID, callID, name, out string, err error) {
	payload := map[string]any{
		"kind":    "tool_result",
		"msg_id":  msgID,
		"call_id": callID,
		"name":    name,
	}
	if err != nil {
		payload["result"] = "error: " + err.Error()
	} else {
		payload["result"] = truncate(out, 4000)
	}
	t.sse.Send(payload)
}

// summarizeToolArgs renders the args map into a single compact
// "key=val, key=val" string for the inline tool chip's summary row.
// Drops newlines, clips long values. Empty args produce empty string.
func summarizeToolArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		switch vv := args[k].(type) {
		case string:
			s = vv
		case []any:
			elems := make([]string, 0, len(vv))
			for _, e := range vv {
				elems = append(elems, compactArgElem(e))
			}
			s = "[" + strings.Join(elems, ", ") + "]"
		case map[string]any:
			s = "{…}"
		case nil:
			s = "null"
		default:
			s = fmt.Sprintf("%v", vv)
		}
		s = strings.ReplaceAll(s, "\n", " ")
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		parts = append(parts, fmt.Sprintf("%s=%q", k, s))
	}
	return strings.Join(parts, ", ")
}

// compactArgElem renders one element of an []any arg value for the tool-chip
// summary. Scalars keep the plain %v form; maps/slices (e.g. tool_def's
// actions array) render as compact JSON — %v on those produced Go-syntax
// "map[method:POST name:…]" in the chip and in copied transcripts, which
// reads like corrupted args to anyone (or any model) inspecting the paste.
func compactArgElem(e any) string {
	switch e.(type) {
	case map[string]any, []any:
		if b, err := json.Marshal(e); err == nil {
			return string(b)
		}
	}
	return fmt.Sprintf("%v", e)
}
