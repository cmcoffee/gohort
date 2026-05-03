package silent

import . "github.com/cmcoffee/gohort/core"

func init() { RegisterChatTool(new(StaySilentTool)) }

// StaySilentTool lets the LLM signal that no reply should be sent.
// When called, it sets ToolSession.Silenced = true; the caller is responsible
// for checking that flag after RunAgentLoop and skipping delivery.
type StaySilentTool struct{}

func (t *StaySilentTool) Name() string { return "stay_silent" }
func (t *StaySilentTool) Caps() []Capability { return nil } // control-flow signal — no side effects
func (t *StaySilentTool) Desc() string {
	return "When called, the user will receive no reply message from you for this turn. Calling this tool closes the turn — any text or further tool calls produced afterward are discarded."
}
func (t *StaySilentTool) Params() map[string]ToolParam { return map[string]ToolParam{} }

// silenceConfirmation is what the LLM sees as the tool result. It is phrased
// as a hard stop signal so the model halts immediately rather than calling
// stay_silent again or producing additional content that will be discarded.
const silenceConfirmation = "Silence acknowledged. This turn is now closed — no reply will be sent. Do not produce any further text and do not call any more tools, including stay_silent. Stop now."

// Run is the fallback for callers without a ToolSession — no-op.
func (t *StaySilentTool) Run(args map[string]any) (string, error) {
	return silenceConfirmation, nil
}

// RunWithSession sets the silenced flag on the session.
func (t *StaySilentTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	sess.Silenced = true
	return silenceConfirmation, nil
}
