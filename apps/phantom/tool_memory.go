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

// saveMemoryToolDef returns a tool that lets the AI persist a note about the conversation.
func saveMemoryToolDef(db Database, chatID string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "save_memory",
			Description: "Save a note to your persistent memory for this conversation. " +
				"Use this to remember important facts about the person — their name, preferences, relationships, or anything worth recalling in future conversations.",
			Parameters: map[string]ToolParam{
				"note": {
					Type:        "string",
					Description: "The fact or detail to remember.",
				},
			},
			Required: []string{"note"},
		},
		Handler: func(args map[string]any) (string, error) {
			note, _ := args["note"].(string)
			if note == "" {
				return "", fmt.Errorf("note is required")
			}
			key := chatID + ":" + newID()
			db.Set(memoryTable, key, phantomMemory{
				Note:      note,
				CreatedAt: now(),
			})
			Log("[phantom/memory] saved for %s: %q", chatID, note)
			return "Memory saved.", nil
		},
	}
}
