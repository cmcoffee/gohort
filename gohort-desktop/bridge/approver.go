package bridge

// daemonApprover gates server-initiated tool calls. The agent has no
// Wails window, so it asks via the platform prompt (a native osascript
// dialog on macOS; deny-by-default elsewhere — see promptApproval in
// platform_*.go). Satisfies wsbridge.Approver.
type daemonApprover struct{}

func (daemonApprover) RequestApprovalBlocking(id, name string, args map[string]any) bool {
	return promptApproval(name, args)
}
