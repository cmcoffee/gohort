package servitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// withHeartbeat runs fn in the foreground while a background goroutine emits a
// "still working" status event every 25 seconds. This gives the user a signal
// that long LLM calls (reasoning models, large context) are still progressing.
func withHeartbeat(ctx context.Context, id, label string, fn func()) {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-ticker.C:
				elapsed := time.Since(start).Round(time.Second)
				emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("%s… (%s)", label, elapsed)})
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	fn()
	close(done)
}

// runMapAppSession connects to the appliance and runs a focused CLI exploration pass.
// The agent enumerates the given command's subcommands and flags, then the reply is
// persisted as a knowledge doc under "cli:<command>" for injection into future sessions.
// saveProfile additionally writes the reference into appliance.Profile — set by the
// command-appliance Map path, where the CLI reference IS the profile (the Profile
// panel otherwise stays empty after a successful map). Map App runs on SSH appliances
// pass false: their profile is the system reconnaissance, not one CLI's reference.
// ownerUser routes the profile write to the appliance owner's store (shared appliances
// keep one profile regardless of who mapped); only used when saveProfile is set.
func (T *Servitor) runMapAppSession(ctx context.Context, id, userID, ownerUser string, appliance Appliance, command string, confirm chan bool, udb Database, saveProfile bool) {
	scratch := ""
	var scratchCleanup func()
	defer func() {
		if scratchCleanup != nil {
			scratchCleanup()
		}
		confirmChans.Delete(id)
		pendingCmds.Delete(id)
		sessionAppliances.Delete(id)
		probeSessions.AppendEvent(id, probeEvent{Kind: "done"}, true)
		probeSessions.ScheduleCleanup(id)
	}()

	a := &Servitor{}
	a.AppCore = T.AppCore

	var execFn func(string) (string, error)
	if strings.TrimSpace(appliance.PeerName) != "" {
		// Peer first, exactly as in runSession: the command lives on the far
		// side, so mapping it here would enumerate a CLI on the WRONG machine —
		// silently, since a local `ls` of a path that only exists on the peer
		// just comes back empty. The scratch setup below already routed through
		// the peer; only this seam did not.
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf(
			"Mapping %s through %s.", appliance.Command, appliance.PeerName)})
		execFn = peerExecFor(ctx, appliance)
	} else if appliance.Type == "command" {
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Running locally: %s", appliance.Command)})
		execFn = func(cmd string) (string, error) {
			return a.exec_local_ctx(ctx, cmd, appliance.WorkDir, appliance.EnvVars)
		}
	} else {
		client, err := acquireConn(userID, appliance)
		if err != nil {
			probeSessions.AppendEvent(id, probeEvent{Kind: "error", Text: "Connection failed: " + err.Error()}, true)
			return
		}
		a.input.host = appliance.Host
		a.input.port = appliance.Port
		if a.input.port == 0 {
			a.input.port = 22
		}
		a.input.user = appliance.User
		if a.input.user == "" {
			a.input.user = "root"
		}
		a.input.password = appliance.Password
		a.conn = client
		execFn = func(cmd string) (string, error) {
			return a.exec_command_ctx(ctx, cmd)
		}
		emit(id, probeEvent{Kind: "status", Text: "Connected."})
	}

	// Same contract as runSession: a private place to write, removed on exit,
	// created and torn down through the raw exec so the gate below cannot refuse
	// its own cleanup. Mapping a CLI tool legitimately stages scratch files.
	{
		rawExec := func(c context.Context, cmd string) (string, error) {
			if strings.TrimSpace(appliance.PeerName) != "" {
				return peerExecFor(c, appliance)(cmd)
			}
			if appliance.Type == "command" {
				return a.exec_local_ctx(c, cmd, appliance.WorkDir, appliance.EnvVars)
			}
			return a.exec_command_ctx(c, cmd)
		}
		dir := scratch_dir(id)
		if err := scratch_setup(ctx, rawExec, dir); err != nil {
			emit(id, probeEvent{Kind: "status", Text: "Scratch directory unavailable — writes will need approval: " + err.Error()})
		} else {
			scratch = dir
			scratchCleanup = func() { scratch_teardown(rawExec, dir) }
		}
	}

	// gateCommand is the risk gate for this session. Mapping a CLI tool means
	// running that tool with arguments the model chose, so it needs the same
	// classification, allowances and confirmation prompt that a probe command
	// gets — it previously had none at all.
	gateCommand := func(cmd string) error {
		cat, reason := classify_command_scoped(cmd, scratch)
		if cat == RiskNone {
			return nil
		}
		if udb != nil {
			var alwaysOK bool
			if udb.Get(alwaysAllowTable, cmd, &alwaysOK) && alwaysOK {
				emit(id, probeEvent{Kind: "status", Text: "Auto-allowed: " + cmd})
				return nil
			}
			// Resolved per (agent, appliance): this agent on this box, else this
			// agent anywhere, else the operator's own auto-run settings. A human
			// at the console has no acting agent and lands on the last of those,
			// so the console behaves exactly as it did before grants existed.
			//
			// The scope is named in the status line because "why did that run
			// without asking me" is the question anyone reads this for, and a
			// bare "auto-allowed" cannot answer it.
			if ok, scope := autoRunAllowed(udb, ActingAgent(ctx), appliance.ID, cat); ok {
				emit(id, probeEvent{Kind: "status", Text: "Auto-allowed (" + string(cat) + " via " + string(scope) + "): " + cmd})
				return nil
			}
		}
		// An acting agent has nobody watching this stream, so parking the
		// command here would block for five minutes and time out. Refuse now,
		// legibly — see agent_confirm.go for why this is a refusal rather than
		// a queued approval.
		if acting := ActingAgent(ctx); acting != "" {
			emit(id, probeEvent{Kind: "status", Text: "Needs approval (" + string(cat) + "): " + cmd})
			Log("[servitor] agent %s refused %q on %s: no standing permission for %s", acting, cmd, appliance.ID, cat)
			return agentCommandRefusal(cmd, cat, reason, applianceLabel(appliance.Name, appliance.ID))
		}
		pendingCmds.Store(id, cmd)
		defer pendingCmds.Delete(id)
		emit(id, probeEvent{Kind: "confirm", Text: cmd, Reason: reason})
		select {
		case allowed := <-confirm:
			if !allowed {
				emit(id, probeEvent{Kind: "status", Text: "Command denied."})
				return fmt.Errorf("command denied by user")
			}
			return nil
		case <-time.After(5 * time.Minute):
			return fmt.Errorf("confirmation timed out")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	termPrompt := terminalPrompt(appliance)
	termEcho := func(label, output string) {
		var buf strings.Builder
		buf.WriteString(strings.ReplaceAll(label, "\n", " "))
		buf.WriteString("\r\n")
		if output != "" {
			buf.WriteString(strings.ReplaceAll(output, "\n", "\r\n"))
			if !strings.HasSuffix(output, "\n") {
				buf.WriteString("\r\n")
			}
		}
		buf.WriteString(termPrompt)
		mirrorToTerm(userID, appliance.ID, []byte(buf.String()))
	}

	// cmdCount keys off the PARSED command base (first non-wrapper
	// word after stripping sudo/nice/timeout/etc.), not the verbatim
	// command string. This catches the "20 variants of grep" failure
	// mode where the LLM rapidly mutates args but keeps hammering the
	// same tool — previously the counter only triggered on byte-exact
	// repeats. baseKey returns "?" for an unparseable line; we still
	// rate-limit those by treating them as a single counter.
	cmdCount := make(map[string]int)
	var cmdMu sync.Mutex // protects cmdCount — agent may issue parallel tool calls
	baseKey := func(cmd string) string {
		segs := shell_segments(cmd)
		if len(segs) == 0 {
			return "?"
		}
		name, _ := parse_cmd(segs[0])
		if name == "" {
			return "?"
		}
		return name
	}

	run_tool := AgentToolDef{
		Tool: Tool{
			Name:        "run_command",
			Description: "Execute a shell command and return combined stdout+stderr. Output is capped at 10,000 characters.",
			Parameters: map[string]ToolParam{
				"command": {Type: "string", Description: "Shell command to run."},
			},
			Required: []string{"command"},
		},
		Handler: func(args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			if cmd == "" {
				return "", fmt.Errorf("command is required")
			}
			key := baseKey(cmd)
			cmdMu.Lock()
			cmdCount[key]++
			count := cmdCount[key]
			cmdMu.Unlock()
			// Higher threshold (5) when keying by base since legit
			// exploration of one tool's surface (e.g. several grep
			// variants narrowing down a search) is common. The pivot
			// nudge in the agent loop catches genuine error streaks
			// independently.
			if count > 5 {
				return fmt.Sprintf("Note: %s has been called %d times this session. Recommending checking other vectors first before continuing — a different tool or angle may move faster than more variants of %s. If %s really is the right tool here, try narrowing the scope (smaller path, more specific pattern) and continue.", key, count-1, key, key), nil
			}
			emit(id, probeEvent{Kind: "cmd", Text: cmd})
			if err := gateCommand(cmd); err != nil {
				return "", err
			}
			result, err := execFn(cmd)
			if stripANSI(result) != "" {
				emit(id, probeEvent{Kind: "output", Text: result})
			}
			termEcho(cmd, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	note_lesson_tool := AgentToolDef{
		Tool: Tool{
			Name:        "note_lesson",
			Description: "Record a system-specific quirk about this CLI tool for future sessions.",
			Parameters: map[string]ToolParam{
				"lesson": {Type: "string", Description: "What was non-obvious or system-specific."},
			},
			Required: []string{"lesson"},
		},
		Handler: func(args map[string]any) (string, error) {
			lesson, _ := args["lesson"].(string)
			if lesson == "" {
				return "", fmt.Errorf("lesson is required")
			}
			if udb != nil {
				var existing string
				udb.Get(notesTable, appliance.ID, &existing)
				udb.Set(notesTable, appliance.ID, existing+"\n- "+strings.TrimSpace(lesson))
			}
			emit(id, probeEvent{Kind: "status", Text: "Note: " + lesson})
			return "noted", nil
		},
		NeedsConfirm: false,
	}

	taskMsg := fmt.Sprintf(
		"Map the `%s` command on this system. Enumerate all subcommands to depth 2, their flags and arguments, and produce a complete structured reference document a future session can use to operate this tool correctly without guessing.",
		command,
	)

	emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("Mapping %s…", command)})

	var resp *Response
	var err error
	withHeartbeat(ctx, id, fmt.Sprintf("Mapping %s", command), func() {
		resp, _, err = a.RunAgentLoop(ctx,
			[]Message{{Role: "user", Content: taskMsg}},
			AgentLoopConfig{
				SystemPrompt:    buildMapAppSystemPrompt(appliance, command, scratch),
				Tools:           []AgentToolDef{run_tool, note_lesson_tool},
				MaxRounds:       60,
				RouteKey:        "app.servitor",
				TierOverride:    applianceTierOverride(appliance.WorkerTier),
				MaskDebugOutput: true,
				ChatOptions:     []ChatOption{WithThink(false)},
			},
		)
	})

	if err != nil && ctx.Err() == nil {
		emit(id, probeEvent{Kind: "error", Text: err.Error()})
		return
	}

	var reply string
	if resp != nil {
		reply = strings.TrimSpace(resp.Content)
	}
	if reply == "" {
		// Previously a silent return: the user watched the exploration in the
		// chat feed and then nothing saved, with no explanation. Say so.
		if ctx.Err() == nil {
			emit(id, probeEvent{Kind: "error", Text: "Mapping ended without a final reference document — nothing was saved. Re-run Map."})
		}
		return
	}

	emit(id, probeEvent{Kind: "reply", Text: reply})

	if udb != nil {
		writeDoc(udb, appliance.ID, "cli:"+command, reply)
		emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("CLI map saved: %s", command)})
	}

	// Command-appliance profile write. Same owner-store routing + thin-reply
	// guards as runSession's saveProfile block: never clobber a real profile
	// with a stub, and don't save from a cancelled run.
	if saveProfile && ctx.Err() == nil {
		ownerUDB := udb
		if ownerUser != "" && ownerUser != userID {
			if o := UserDB(T.DB, ownerUser); o != nil {
				ownerUDB = o
			}
		}
		if ownerUDB != nil {
			var existing Appliance
			if ownerUDB.Get(applianceTable, appliance.ID, &existing) {
				prior := strings.TrimSpace(existing.Profile)
				tooThin := len(reply) < minMapProfileChars ||
					(prior != "" && len(reply) < len(prior)/3)
				if tooThin {
					emit(id, probeEvent{Kind: "status", Text: "Map didn't produce a complete profile — keeping the previous one."})
					Log("[servitor.map] kept prior profile for %q (new=%d chars, prior=%d chars)",
						appliance.Name, len(reply), len(prior))
				} else {
					existing.Profile = reply
					existing.Scanned = time.Now().Format(time.RFC3339)
					ownerUDB.Set(applianceTable, appliance.ID, existing)
					emit(id, probeEvent{Kind: "status", Text: "Profile updated."})
				}
			}
		}
	}
}
