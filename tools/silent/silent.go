package silent

import . "github.com/cmcoffee/gohort/core"

func init() { RegisterChatTool(new(StaySilentTool)) }

// StaySilentTool lets the LLM signal that no text reply should be sent.
// When called, it sets ToolSession.Silenced = true; the caller suppresses
// the text reply but still flushes any attachments (images, videos) the
// LLM gathered during the turn. So pairing stay_silent with download_video
// delivers the file with no caption.
type StaySilentTool struct{}

func (t *StaySilentTool) Name() string { return "stay_silent" }
func (t *StaySilentTool) Caps() []Capability { return nil } // control-flow signal — no side effects
func (t *StaySilentTool) Desc() string {
	return "Suppress your text reply for this turn — the user receives NO MESSAGE TEXT. Use only when (a) you've produced an attachment (download_video, generate_image) and the file IS the message, or (b) you decided to take no action and there's nothing meaningful to say. DO NOT use after completing an action the user asked for (creating a watcher, sending a message, modifying state) — they want confirmation it happened. DO NOT use to avoid 'chitchat' on a successful operation. When in doubt, send a short text reply instead. Calling this closes the turn — any text or further tool calls afterward are discarded."
}
func (t *StaySilentTool) Params() map[string]ToolParam { return map[string]ToolParam{} }

// silenceConfirmation is what the LLM sees as the tool result. It is phrased
// as a hard stop signal so the model halts immediately rather than calling
// stay_silent again or producing additional content that will be discarded.
const silenceConfirmation = "Silence acknowledged. This turn is now closed — your text reply will be suppressed (attachments you've already gathered will still be delivered). Do not produce any further text and do not call any more tools, including stay_silent. Stop now."

// Run is the fallback for callers without a ToolSession — no-op.
func (t *StaySilentTool) Run(args map[string]any) (string, error) {
	return silenceConfirmation, nil
}

// RunWithSession sets the silenced flag on the session.
func (t *StaySilentTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	sess.Silenced = true
	return silenceConfirmation, nil
}
