package localtime

import (
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(LocalTimeTool)) }

// LocalTimeTool returns the current local date and time.
type LocalTimeTool struct{}

func (t *LocalTimeTool) Name() string { return "get_local_time" }
func (t *LocalTimeTool) Desc() string { return "Get the current local date and time." }
func (t *LocalTimeTool) Caps() []Capability { return []Capability{CapRead} } // reads system clock

func (t *LocalTimeTool) Params() map[string]ToolParam { return nil }

func (t *LocalTimeTool) Run(args map[string]any) (string, error) {
	return time.Now().Format("Monday, January 2, 2006 3:04:05 PM MST"), nil
}
