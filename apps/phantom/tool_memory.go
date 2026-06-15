package phantom

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// Phantom's per-conversation Explicit Memory now rides on the shared core
// fact store (core.StoreMemoryFact / ListMemoryFacts / ForgetMemoryFactByIndex)
// under the namespace "phantom:<chatID>". This gives phantom the same dedup
// AND the save-time supersession (a changed fact replaces the stale one)
// orchestrate uses, instead of the prior fork that only deduped. Legacy rows
// in the old phantom_memory table are folded in once at startup by
// MigratePhantomMemory.

// memoryTable is retained only so MigratePhantomMemory can read and drain the
// legacy rows. New memories never write here.
const memoryTable = "phantom_memory"

// phantomMemory is the legacy stored shape, kept for migration decoding.
type phantomMemory struct {
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

// phantomMemoryNS scopes a conversation's memories in the shared core fact
// store. Per-chat isolation comes from the chatID in the namespace.
func phantomMemoryNS(chatID string) string { return "phantom:" + chatID }

// loadMemories returns all remembered facts for a conversation, oldest first.
// Superseded facts are filtered out by ListMemoryFacts. When core has nothing
// for the chat, it lazily (and safely) pulls any legacy phantom_memory rows in
// first — so a chat the startup migration missed self-heals on first read.
func loadMemories(db Database, chatID string) []MemoryFact {
	ns := phantomMemoryNS(chatID)
	facts := ListMemoryFacts(db, ns)
	if len(facts) == 0 && lazyMigratePhantomChat(db, chatID) {
		facts = ListMemoryFacts(db, ns)
	}
	return facts
}

// migrateOnePhantomRow moves one legacy note into core under phantom:<chatID>
// and drains the legacy row ONLY after confirming the store landed. Returns
// true when the legacy row was drained (migrated, or already represented).
// This is the safe primitive both the startup and lazy migrations use — the
// earlier version drained unconditionally, which could lose a row if the store
// silently failed.
func migrateOnePhantomRow(db Database, chatID, key, note string) bool {
	note = strings.TrimSpace(note)
	if note == "" {
		db.Unset(memoryTable, key) // empty junk row, nothing to preserve
		return true
	}
	ns := phantomMemoryNS(chatID)
	f, _, _ := StoreMemoryFact(db, ns, note)
	// Drain only after confirming THIS note is actually present in core. We
	// check by reading the fact back by id rather than trusting the return
	// value (StoreMemoryFact returns a populated struct even if the underlying
	// write failed) or a chat-level "has any data" check (which would pass for
	// a failed row once an earlier row succeeded). On dedup f is the existing
	// fact, so its id is already present — correct, the content is represented.
	persisted := false
	for _, g := range ListMemoryFacts(db, ns) {
		if g.ID == f.ID {
			persisted = true
			break
		}
	}
	if !persisted {
		Log("[phantom/memory] migrate: note did not persist for chat=%s — keeping legacy row", chatID)
		return false
	}
	db.Unset(memoryTable, key)
	return true
}

// lazyMigratePhantomChat moves any legacy phantom_memory rows for one chat into
// core, safely. Returns true if it drained at least one row.
func lazyMigratePhantomChat(db Database, chatID string) bool {
	prefix := chatID + ":"
	moved := false
	for _, k := range db.Keys(memoryTable) {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		var m phantomMemory
		if !db.Get(memoryTable, k, &m) {
			continue
		}
		if migrateOnePhantomRow(db, chatID, k, m.Note) {
			moved = true
		}
	}
	return moved
}

// memoryBlock builds the system-prompt injection for a conversation's memories.
// Returns an empty string when there are no memories.
func memoryBlock(db Database, chatID string) string {
	mems := loadMemories(db, chatID)
	if len(mems) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThings you remember about this conversation:\n")
	for _, m := range mems {
		b.WriteString("- " + m.Note + "\n")
	}
	return b.String()
}

// memoryGroupedToolDef returns an AgentToolDef that consolidates the
// per-conversation memory operations (save, list, delete) into one
// catalog entry with action discriminator. chat is the worker LLM used
// for save-time supersession; pass T.WorkerChat.
func memoryGroupedToolDef(db Database, chatID string, chat FactChatFunc) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "memory",
			Description: `EXPLICIT memory — short facts YOU decide to save, always in your prompt every turn. Use for stable info you want stamped on top of every future conversation: the person's name, preferences, relationships, recurring topics. Counterpart to the IMPLICIT knowledge tool (bulkier, semantic-search-driven, only surfaces when relevant). Rule of thumb: if a fact deserves to be visible to you on EVERY turn forever, use memory; if it's recall-worthy but situational, use knowledge. Call with action="help" for full usage.`,
			Parameters: map[string]ToolParam{
				"action": {Type: "string", Description: `Which sub-action: save | list | delete | help.`},
				"note":   {Type: "string", Description: "(save) The fact or detail to remember."},
				"index":  {Type: "integer", Description: "(delete) 1-based index from the most recent list call."},
			},
			Required: []string{"action"},
		},
		Handler: func(args map[string]any) (string, error) {
			action := strings.TrimSpace(StringArg(args, "action"))
			switch action {
			case "", "help":
				return memoryHelp(), nil
			case "save":
				return memorySave(db, chatID, args, chat)
			case "list":
				return memoryList(db, chatID)
			case "delete":
				return memoryDelete(db, chatID, args)
			default:
				return "", fmt.Errorf("unknown action %q. valid: save, list, delete, help", action)
			}
		},
	}
}

func memoryHelp() string {
	return `memory — usage:

  action="save" — persist a note. Required: note (string).
    Use for facts about the person you'll want next conversation:
    name, preferences, relationships, ongoing topics. A note that
    UPDATES an earlier one (new number, moved, changed plans) replaces
    the stale memory automatically.

  action="list" — show all saved memories for this conversation,
    oldest first, with 1-based index numbers (use index N with
    action="delete" to remove a specific entry).

  action="delete" — remove a saved memory by index. Required: index
    (1-based, from the most recent list call).

  action="help" — show this usage spec.
`
}

func memorySave(db Database, chatID string, args map[string]any, chat FactChatFunc) (string, error) {
	note, _ := args["note"].(string)
	note = strings.TrimSpace(note)
	if note == "" {
		return "", fmt.Errorf("note is required")
	}
	// Dedup + supersession live in core.StoreMemoryFact. Passing chat enables
	// the supersession judge so a changed fact replaces the stale one rather
	// than coexisting — which matters more here than in chat, since the
	// phantom persona rarely calls delete on its own.
	f, isNew, superseded := StoreMemoryFact(db, phantomMemoryNS(chatID), note, chat)
	if !isNew {
		Log("[phantom/memory] dedupe for %s: %q ~ %q", chatID, note, f.Note)
		return fmt.Sprintf("Already remembered: %q. Skipping duplicate.", f.Note), nil
	}
	msg := "Memory saved."
	if len(superseded) > 0 {
		dropped := make([]string, len(superseded))
		for i, s := range superseded {
			dropped[i] = fmt.Sprintf("%q", s.Note)
		}
		msg += fmt.Sprintf(" Replaced %d now-stale memory(ies): %s.", len(dropped), strings.Join(dropped, ", "))
	}
	Log("[phantom/memory] saved for %s: %q", chatID, note)
	return msg, nil
}

func memoryList(db Database, chatID string) (string, error) {
	mems := loadMemories(db, chatID)
	if len(mems) == 0 {
		return "No memories saved for this conversation.", nil
	}
	var b strings.Builder
	for i, m := range mems {
		fmt.Fprintf(&b, "%d. %s\n", i+1, m.Note)
	}
	return b.String(), nil
}

func memoryDelete(db Database, chatID string, args map[string]any) (string, error) {
	idx, _ := args["index"].(float64) // JSON numbers come through as float64
	if idx < 1 {
		return "", fmt.Errorf("index is required and must be >= 1")
	}
	removed, ok := ForgetMemoryFactByIndex(db, phantomMemoryNS(chatID), int(idx))
	if !ok {
		mems := loadMemories(db, chatID)
		return "", fmt.Errorf("index %d out of range (have %d memories)", int(idx), len(mems))
	}
	Log("[phantom/memory] deleted for %s: %q", chatID, removed.Note)
	return fmt.Sprintf("Deleted memory %d: %q", int(idx), removed.Note), nil
}

// MigratePhantomMemory folds legacy phantom_memory rows ({Note, CreatedAt}
// keyed "<chatID>:<id>") into the shared core fact store under the
// "phantom:<chatID>" namespace, then drains the old rows. Runs once at
// startup. Idempotent — after migration phantom_memory is empty so a re-run
// is a no-op. The original CreatedAt ordering is not preserved (migrated
// facts take a fresh timestamp via StoreMemoryFact); only the note text and
// per-chat scoping matter for memory, and dedup applies on the way through.
func MigratePhantomMemory(db Database) {
	if db == nil {
		return
	}
	seen, moved := 0, 0
	for _, k := range db.Keys(memoryTable) {
		var m phantomMemory
		if !db.Get(memoryTable, k, &m) {
			continue
		}
		seen++
		// Key is "<chatID>:<id>". The id (newID) contains no ':', so the
		// last ':' splits chatID from id even when the chatID itself has
		// punctuation (e.g. "any;+;chat123:abcd").
		ci := strings.LastIndex(k, ":")
		if ci <= 0 {
			continue
		}
		if migrateOnePhantomRow(db, k[:ci], k, m.Note) {
			moved++
		}
	}
	// Always log what was seen, even 0, so a missed migration is visible
	// rather than silent.
	Log("[phantom/memory] startup migration: saw %d legacy row(s), migrated %d into core_facts", seen, moved)
}
