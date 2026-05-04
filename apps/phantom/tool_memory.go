package phantom

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const memoryTable = "phantom_memory"

type phantomMemory struct {
	Note      string `json:"note"`
	CreatedAt string `json:"created_at"`
}

// loadMemories returns all remembered notes for a conversation, oldest first.
func loadMemories(db Database, chatID string) []phantomMemory {
	if db == nil {
		return nil
	}
	prefix := chatID + ":"
	var mems []phantomMemory
	for _, k := range db.Keys(memoryTable) {
		if strings.HasPrefix(k, prefix) {
			var m phantomMemory
			if db.Get(memoryTable, k, &m) {
				mems = append(mems, m)
			}
		}
	}
	// Sort oldest first by CreatedAt (RFC3339 sorts lexicographically).
	for i := 1; i < len(mems); i++ {
		for j := i; j > 0 && mems[j].CreatedAt < mems[j-1].CreatedAt; j-- {
			mems[j], mems[j-1] = mems[j-1], mems[j]
		}
	}
	return mems
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
// catalog entry with action discriminator. Replaces the individual
// save_memory tool. action="help" returns the full usage spec the
// brief description points the LLM at.
func memoryGroupedToolDef(db Database, chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "memory",
			Description: `Manage your persistent per-conversation memory: save notes you want to remember about the person, list what's saved, delete entries no longer relevant. Call with action="help" for the full usage.`,
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
				return memorySave(db, chatID, args)
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
    name, preferences, relationships, ongoing topics.

  action="list" — show all saved memories for this conversation,
    oldest first, with 1-based index numbers (use index N with
    action="delete" to remove a specific entry).

  action="delete" — remove a saved memory by index. Required: index
    (1-based, from the most recent list call).

  action="help" — show this usage spec.
`
}

func memorySave(db Database, chatID string, args map[string]any) (string, error) {
	note, _ := args["note"].(string)
	if strings.TrimSpace(note) == "" {
		return "", fmt.Errorf("note is required")
	}
	key := chatID + ":" + newID()
	db.Set(memoryTable, key, phantomMemory{
		Note:      note,
		CreatedAt: now(),
	})
	Log("[phantom/memory] saved for %s: %q", chatID, note)
	return "Memory saved.", nil
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
	target := int(idx) - 1 // convert 1-based to 0-based
	mems := loadMemories(db, chatID)
	if target < 0 || target >= len(mems) {
		return "", fmt.Errorf("index %d out of range (have %d memories)", int(idx), len(mems))
	}
	// loadMemories sorts oldest-first; find the matching key.
	prefix := chatID + ":"
	type kv struct {
		key string
		mem phantomMemory
	}
	var entries []kv
	for _, k := range db.Keys(memoryTable) {
		if strings.HasPrefix(k, prefix) {
			var m phantomMemory
			if db.Get(memoryTable, k, &m) {
				entries = append(entries, kv{k, m})
			}
		}
	}
	// Sort same as loadMemories (oldest first).
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].mem.CreatedAt < entries[j-1].mem.CreatedAt; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	if target >= len(entries) {
		return "", fmt.Errorf("index %d out of range", int(idx))
	}
	removed := entries[target]
	db.Unset(memoryTable, removed.key)
	Log("[phantom/memory] deleted for %s: %q", chatID, removed.mem.Note)
	return fmt.Sprintf("Deleted memory %d: %q", int(idx), removed.mem.Note), nil
}
