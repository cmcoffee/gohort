package phantom

import (
	"fmt"
	"math/rand"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// followUpToolDef returns a tool that lets the AI schedule a brief follow-up
// message to send after a short delay (1–5 seconds). The AI can use this when
// it has additional context to add after its main reply — like a "P.S." or
// "by the way" thought that feels more natural with a slight pause.
func followUpToolDef(db Database, chatID, handle string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "follow_up",
			Description: "Schedule a follow-up message to send after a short delay. " +
				"Use this when you have additional thoughts to share — like a P.S. note, " +
				"a gentle reminder, or something you just realized. The delay makes it " +
				"feel natural rather than bot-like. Default delay is 1–5 seconds randomized.",
			Parameters: map[string]ToolParam{
				"message": {
					Type:        "string",
					Description: "The follow-up message text.",
				},
				"delay_seconds": {
					Type:        "integer",
					Description: "Optional delay in seconds (1–5). Defaults to a random delay between 1 and 5.",
				},
			},
			Required: []string{"message"},
		},
		Handler: func(args map[string]any) (string, error) {
			msg, _ := args["message"].(string)
			if msg == "" {
				return "", fmt.Errorf("message is required")
			}

			delay := 0
			if d, ok := args["delay_seconds"]; ok {
				switch v := d.(type) {
				case int:
					delay = v
				case float64:
					delay = int(v)
				}
			}
			if delay < 1 || delay > 5 {
				delay = 1 + rand.Intn(5)
			}

			go func() {
				time.Sleep(time.Duration(delay) * time.Second)
				enqueueOutbox(db, OutboxItem{
					ID:     newID(),
					ChatID: chatID,
					Handle: handle,
					Text:   msg,
					Type:   "follow_up",
				})
				Log("[phantom] follow-up queued for %s (%s): %q", chatID, newID(), msg)
			}()

			return fmt.Sprintf("Follow-up message scheduled (%ds delay)", delay), nil
		},
	}
}
