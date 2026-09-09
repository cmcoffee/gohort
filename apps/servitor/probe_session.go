package servitor

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
	"golang.org/x/crypto/ssh"
)

// orchestratorThinkOpts returns a WithThinkBudget option using the configured
// budget for the orchestrator route stage, falling back to the stage default.
func orchestratorThinkOpts() []ChatOption {
	if b := RouteThinkBudget("app.servitor.orchestrator"); b != nil {
		return []ChatOption{WithThinkBudget(*b)}
	}
	return nil
}

// normalizeTask reduces a probe-tool task description to a content-token
// signature: lowercase, strip punctuation, drop short and stop words, sort,
// join. Two paraphrased delegations of the same task ("list mysql tables",
// "show tables in mysql") collapse to the same signature so the cache
// catches them as duplicates and the orchestrator stops grinding the
// same area through paraphrase.
func normalizeTask(s string) string {
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "with": true,
		"that": true, "this": true, "from": true, "into": true,
		"are": true, "you": true, "your": true, "any": true,
		"all": true, "use": true, "via": true, "show": true,
		"list": true, "find": true, "get": true, "see": true,
		"check": true, "look": true, "what": true, "which": true,
		"where": true, "when": true, "how": true,
	}
	s = strings.ToLower(s)
	var words []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	}) {
		if len(w) < 3 || stop[w] {
			continue
		}
		words = append(words, w)
	}
	sort.Strings(words)
	return strings.Join(words, " ")
}

// runSession acquires a pooled SSH connection, runs the agent loop, and streams events.
// The connection is NOT closed on return — it stays pooled for the next request.
// saveProfile=true writes the final LLM reply and extracted log map back to the appliance record.
// runSession runs an investigation. userID/udb are the REQUESTING user's
// (connection, terminal, chat sessions, per-user notes) while ownerUser is the
// appliance's owner — used for the shared repo clone/store so a shared repo is
// read from ONE place regardless of who opened it. For a non-shared appliance
// ownerUser == userID, so behavior is unchanged. Scoped memory is keyed by the
// appliance ID (global), so it's shared with no extra plumbing.

// runSession is one investigation on one appliance: connect (or refuse),
// build the exec seam and the tool kit, run the mode the caller asked for,
// then persist and report. Everything the run holds lives on probeRun; each
// stage is a method, and a stage that has to end the run early says so
// with actReturn, which is what its `return` used to mean when this was
// one function.
func (T *Servitor) runSession(ctx context.Context, id, userID, ownerUser string, appliance Appliance, confirm chan bool, messages []Message, udb Database, saveProfile bool) {
	pr := &probeRun{T: T, ctx: ctx, id: id, userID: userID, ownerUser: ownerUser, appliance: appliance, confirm: confirm, messages: messages, udb: udb, saveProfile: saveProfile}
	// scratch is this run's private write location on the target (see scratch.go).
	// Set once the transport is up; cleared if the directory can't be created, so
	// the classifier falls back to gating every write.
	defer func() {
		if pr.scratchCleanup != nil {
			pr.scratchCleanup()
		}
		confirmChans.Delete(id)
		pendingCmds.Delete(id)
		ReleaseInjectionQueue(id)
		sessionAppliances.Delete(id)
		probeSessions.AppendEvent(id, probeEvent{Kind: "done"}, true)
		probeSessions.ScheduleCleanup(id)
	}()
	if pr.connect() == actReturn {
		return
	}
	pr.execSeam()
	pr.execTools()
	pr.memoryTools()
	pr.readTools()
	pr.reportTools()
	pr.assembleToolkit()
	var act probeAction
	if pr.saveProfile {
		act = pr.mapMode()
	} else {
		act = pr.chatMode()
	}
	if act == actReturn {
		return
	}
	pr.finishTurn()
}

// sessionFailures collects commands that exited nonzero during this session.
// Emitted as a summary before the final reply so the user can see what the agent
// tried and couldn't complete.
type sessionFailure struct {
	Cmd    string
	Reason string // first non-empty line of the output
}

const loopLimit = 3

type probeRun struct {
	T                *Servitor
	ctx              context.Context
	id               string
	userID           string
	ownerUser        string
	appliance        Appliance
	confirm          chan bool
	messages         []Message
	udb              Database
	saveProfile      bool
	scratch          string
	scratchCleanup   func()
	ownerUDB         Database
	a                *Servitor
	termPrompt       string
	sessionFailures  []sessionFailure
	cmdCount         map[string]int
	cmdMu            sync.Mutex
	failCount        map[string]int
	failMu           sync.Mutex
	read_log_tool    AgentToolDef
	search_logs_tool AgentToolDef
	ptyCount         map[string]int
	note_lesson_tool AgentToolDef
	// techniqueMu serializes the techniques string between an append in the
	// tool handler and a prune in a background audit: both are read-modify-
	// write on one record, and without the lock a prune landing mid-append
	// would drop whichever write finished first. An audit outliving the
	// session is fine — it holds no session state, only the store — and is
	// bounded by its own timeout.
	techniqueMu           sync.Mutex
	record_technique_tool AgentToolDef
	record_discovery_tool AgentToolDef
	store_fact_tool       AgentToolDef
	link_entities_tool    AgentToolDef
	store_rule_tool       AgentToolDef
	count_lines_tool      AgentToolDef
	read_range_tool       AgentToolDef
	search_facts_tool     AgentToolDef
	search_knowledge_tool AgentToolDef
	// Cached recorded-knowledge blocks. These feed buildLeadSystemPrompt and
	// the investigator's first message; the old workerPrompt concatenation
	// that used to interleave here was dead (never sent to any model) and
	// was removed along with its three prompt builders.
	cachedFacts string
	// Cached recorded-knowledge blocks. These feed buildLeadSystemPrompt and
	// the investigator's first message; the old workerPrompt concatenation
	// that used to interleave here was dead (never sent to any model) and
	// was removed along with its three prompt builders.
	cachedNotes string
	// Cached recorded-knowledge blocks. These feed buildLeadSystemPrompt and
	// the investigator's first message; the old workerPrompt concatenation
	// that used to interleave here was dead (never sent to any model) and
	// was removed along with its three prompt builders.
	cachedTechniques string
	// Cached recorded-knowledge blocks. These feed buildLeadSystemPrompt and
	// the investigator's first message; the old workerPrompt concatenation
	// that used to interleave here was dead (never sent to any model) and
	// was removed along with its three prompt builders.
	cachedRules string
	// Cached recorded-knowledge blocks. These feed buildLeadSystemPrompt and
	// the investigator's first message; the old workerPrompt concatenation
	// that used to interleave here was dead (never sent to any model) and
	// was removed along with its three prompt builders.
	cachedDiscoveries       string
	watch_condition_tool    AgentToolDef
	list_watches_tool       AgentToolDef
	save_to_codewriter_tool AgentToolDef
	save_to_techwriter_tool AgentToolDef
	list_guides_tool        AgentToolDef
	record_finding_tool     AgentToolDef
	push_to_guide_tool      AgentToolDef
	ptyLocal                bool
	// workerTools holds a placeholder run_command entry. Every call site must use
	// withFreshRunTool(workerTools) so each invocation gets isolated counters.
	workerTools []AgentToolDef
	// Populated for toolset appliances only; carries the bound tools plus what
	// was withheld and why, and feeds both the allow-list check and the
	// orientation pass below.
	resolvedTools resolvedToolset
	reply         string
	consolidateFn func()
	// m and c are the map and chat modes' own state; see mapMode and chatMode.
	m mapState
	c chatState
	// ret carries an early return out of a phase method (see exit).
	ret probeResult
}

// probeAction is what a round method tells the driver to do next.
type probeAction int

const (
	actNone     probeAction = iota // carry on with the next phase of this round
	actContinue                    // next round
	actBreak                       // leave the loop and finish
	actReturn                      // return pr.ret from the function
)

// probeResult is the function's return, parked by exit until the driver returns it.
type probeResult struct {
	resp    *Response
	history []Message
	err     error
}

func (pr *probeRun) connect() probeAction {
	if pr.ownerUser == "" {
		pr.ownerUser = pr.userID
	}
	pr.ownerUDB = pr.udb
	if pr.ownerUser != pr.userID {
		pr.ownerUDB = UserDB(pr.T.DB, pr.ownerUser) // shared repo: clone/store live under the owner
	}

	pr.a = &Servitor{}
	pr.a.AppCore = pr.T.AppCore

	if pr.appliance.Type == "workspace" {
		// A workspace has no host, clone or credentials of its own — the
		// coordinator fans the question out to its members, each of which
		// re-enters this function in its own owner's context. See workspace.go.
		pr.T.runWorkspaceSession(pr.ctx, pr.id, pr.userID, pr.appliance, pr.messages, pr.udb)
		return actReturn
	}
	if strings.TrimSpace(pr.appliance.PeerName) != "" {
		// Reached through a peer: no connection to acquire here, because the
		// SSH session lives on the far side. Everything ELSE about this run is
		// ordinary — same prompts for the appliance's type, same tools, same
		// risk gate, same knowledge — because only the exec seam differs.
		emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf(
			"Working %s through %s.", applianceLabel(pr.appliance.Name, pr.appliance.ID), pr.appliance.PeerName)})
	} else if pr.appliance.Type == "command" {
		emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Running locally: %s", pr.appliance.Command)})
	} else if pr.appliance.Type == "repo" {
		// No connection to acquire — probes search/read the encrypted store.
		// A Map run (saveProfile) is the repo analogue of SSH reconnaissance:
		// it re-clones synchronously first so the map reflects CURRENT code and
		// self-heals an empty store (e.g. right after Clear Memory). Q&A runs
		// use whatever is already ingested.
		if pr.saveProfile {
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Cloning %s…", repoDisplayTarget(pr.appliance))})
			withHeartbeat(pr.ctx, pr.id, "Cloning repository", func() {
				pr.T.cloneAndIngestRepo(pr.ctx, pr.ownerUser, pr.ownerUDB, pr.appliance.ID)
			})
			if pr.ctx.Err() != nil {
				probeSessions.ScheduleCleanup(pr.id)
				return actReturn
			}
		}
		if repoFileCount(pr.ownerUser, pr.appliance.ID) == 0 {
			msg := "Repository not ingested yet — run Refresh to clone and map it."
			if pr.saveProfile {
				msg = "Clone failed — check the Git URL, branch, and access token, then try again. (Is git installed on the host?)"
			}
			probeSessions.AppendEvent(pr.id, probeEvent{Kind: "error", Text: msg}, true)
			probeSessions.ScheduleCleanup(pr.id)
			return actReturn
		}
		emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Reading repository %s", repoDisplayTarget(pr.appliance))})
	} else if pr.appliance.Type == "bundle" {
		// No connection and nothing to refresh: unlike a repo, a bundle
		// cannot be re-fetched. A Map run reads whatever was ingested — if
		// that is nothing, the fix is an upload, which is the user's move and
		// not something this session can perform on their behalf.
		if n := bundle.Open(pr.ownerUser, pr.appliance.ID).FileCount(); n == 0 {
			msg := "No evidence ingested yet — upload the bundle's files first."
			if pr.appliance.BundleState == bundleStateIngesting {
				msg = "The upload is still being expanded and ingested. Wait for it to finish, then ask again."
			} else if pr.appliance.BundleState == bundleStateFailed && pr.appliance.BundleError != "" {
				msg = "The last ingest failed: " + pr.appliance.BundleError
			}
			probeSessions.AppendEvent(pr.id, probeEvent{Kind: "error", Text: msg}, true)
			probeSessions.ScheduleCleanup(pr.id)
			return actReturn
		}
		emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Reading evidence bundle %s", bundleDisplayTarget(pr.appliance))})
	} else if pr.appliance.Type == "toolset" {
		// No connection and no filesystem: the target is reached only through
		// the bound tools. An appliance with nothing bound has no way to
		// investigate anything, which is a configuration gap rather than a
		// failure, so it says so instead of running an empty session.
		if len(pr.appliance.Toolset) == 0 {
			probeSessions.AppendEvent(pr.id, probeEvent{Kind: "error",
				Text: "No tools are bound to this system yet — edit it and pick the tools its investigations may use."}, true)
			probeSessions.ScheduleCleanup(pr.id)
			return actReturn
		}
		emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Working through %s", toolsetDisplayTarget(pr.appliance))})
	} else {
		client, err := acquireConn(pr.userID, pr.appliance)
		if err != nil {
			probeSessions.AppendEvent(pr.id, probeEvent{Kind: "error", Text: "Connection failed: " + err.Error()}, true)
			probeSessions.ScheduleCleanup(pr.id)
			return actReturn
		}
		pr.a.input.host = pr.appliance.Host
		pr.a.input.port = pr.appliance.Port
		if pr.a.input.port == 0 {
			pr.a.input.port = 22
		}
		pr.a.input.user = pr.appliance.User
		if pr.a.input.user == "" {
			pr.a.input.user = "root"
		}
		pr.a.input.password = pr.appliance.Password
		pr.a.conn = client
		emit(pr.id, probeEvent{Kind: "status", Text: "Connected."})
	}
	return actNone
}

func (pr *probeRun) termEcho(label, output string) {
	var buf strings.Builder
	buf.WriteString(strings.ReplaceAll(label, "\n", " "))
	buf.WriteString("\r\n")
	if output != "" {
		buf.WriteString(strings.ReplaceAll(output, "\n", "\r\n"))
		if !strings.HasSuffix(output, "\n") {
			buf.WriteString("\r\n")
		}
	}
	buf.WriteString(pr.termPrompt)
	mirrorToTerm(pr.userID, pr.appliance.ID, []byte(buf.String()))
}

// sshExec executes a command via the appropriate exec path: local for command-type
// appliances, SSH with transparent reconnect for ssh-type appliances. ctx is the
// session context so a cancelled session aborts in-flight commands.
func (pr *probeRun) sshExec(cmd string) (string, error) {
	if strings.TrimSpace(pr.appliance.PeerName) != "" {
		return peerExecFor(pr.ctx, pr.appliance)(cmd)
	}
	if pr.appliance.Type == "command" {
		return pr.a.exec_local_ctx(pr.ctx, cmd, pr.appliance.WorkDir, pr.appliance.EnvVars)
	}
	result, err := pr.a.exec_command_ctx(pr.ctx, cmd)
	if err == nil {
		return result, nil
	}
	msg := err.Error()
	isConnErr := strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "new SSH session")
	if !isConnErr {
		return result, err
	}
	emit(pr.id, probeEvent{Kind: "status", Text: "SSH connection lost — reconnecting…"})
	dropConn(pr.userID, pr.appliance.ID)
	newClient, rerr := acquireConn(pr.userID, pr.appliance)
	if rerr != nil {
		reconnMsg := fmt.Sprintf("[SSH DISCONNECTED — reconnect failed: %v. Stop issuing SSH commands; the session must be restarted.]", rerr)
		emit(pr.id, probeEvent{Kind: "error", Text: "Reconnect failed: " + rerr.Error()})
		return reconnMsg, nil
	}
	pr.a.conn = newClient
	emit(pr.id, probeEvent{Kind: "status", Text: "SSH reconnected — retrying command…"})
	return pr.a.exec_command_ctx(pr.ctx, cmd)
}

// gateCommand applies the risk gate to one command line: classify it against
// this run's scratch directory, honor the operator's per-command and
// per-category allowances, and block on a confirmation prompt when it is
// still risky. Returns an error when the command must not run.
//
// Every path that executes on the target goes through here. run_pty used to
// skip the gate entirely, which made it a way around every rule run_command
// enforces — including with its `input` lines, which are commands typed into
// an interactive session and are gated individually below.
func (pr *probeRun) gateCommand(cmd string) error {
	cat, reason := classify_command_scoped(cmd, pr.scratch)
	if cat == RiskNone {
		return nil
	}
	if pr.udb != nil {
		// Per-command always-allow (operator trusts this exact command).
		var alwaysOK bool
		if pr.udb.Get(alwaysAllowTable, cmd, &alwaysOK) && alwaysOK {
			emit(pr.id, probeEvent{Kind: "status", Text: "Auto-allowed: " + cmd})
			return nil
		}
		// Per-category allowance (operator trusts this whole class of command
		// — the web analog of the CLI --allow flag, set via the Permissions
		// modal). Resolved per (agent, appliance): this agent on this box, else this
		// agent anywhere, else the operator's own auto-run settings. A human
		// at the console has no acting agent and lands on the last of those,
		// so the console behaves exactly as it did before grants existed.
		//
		// The scope is named in the status line because "why did that run
		// without asking me" is the question anyone reads this for, and a
		// bare "auto-allowed" cannot answer it.
		if ok, scope := autoRunAllowed(pr.udb, ActingAgent(pr.ctx), pr.appliance.ID, cat); ok {
			emit(pr.id, probeEvent{Kind: "status", Text: "Auto-allowed (" + string(cat) + " via " + string(scope) + "): " + cmd})
			return nil
		}
	}
	// An acting agent has nobody watching this stream, so parking the
	// command here would block for five minutes and time out. Refuse now,
	// legibly — see agent_confirm.go for why this is a refusal rather than
	// a queued approval.
	if acting := ActingAgent(pr.ctx); acting != "" {
		emit(pr.id, probeEvent{Kind: "status", Text: "Needs approval (" + string(cat) + "): " + cmd})
		Log("[servitor] agent %s refused %q on %s: no standing permission for %s", acting, cmd, pr.appliance.ID, cat)
		return agentCommandRefusal(cmd, cat, reason, applianceLabel(pr.appliance.Name, pr.appliance.ID))
	}
	pendingCmds.Store(pr.id, cmd)
	defer pendingCmds.Delete(pr.id)
	emit(pr.id, probeEvent{Kind: "confirm", Text: cmd, Reason: reason})
	select {
	case allowed := <-pr.confirm:
		if !allowed {
			emit(pr.id, probeEvent{Kind: "status", Text: "Command denied."})
			return fmt.Errorf("command denied by user")
		}
		return nil
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("confirmation timed out")
	case <-pr.ctx.Done():
		return pr.ctx.Err()
	}
}

// Failure budget removed — sessionFailures still collected for the
// post-session summary, but no hard block on binary or global
// failure counts. Loop/topic-exhaustion guards (LOOP DETECTED,
// probeLoopSignalCount, probeTopicCount) handle runaway behavior;
// command failures are signal for the model to interpret, not a
// reason to short-circuit the worker.

// cmdBinary returns the effective binary from a shell command string,
// skipping sudo, env, nohup, and env-var assignments so that
// "sudo mysql -u root" and "mysql -u root -p" both map to "mysql".
func (pr *probeRun) cmdBinary(cmd string) string {
	skip := map[string]bool{"sudo": true, "env": true, "nohup": true, "nice": true, "time": true, "ionice": true}
	for _, f := range strings.Fields(cmd) {
		if strings.Contains(f, "=") {
			continue // env var assignment
		}
		if skip[f] {
			continue
		}
		if i := strings.LastIndex(f, "/"); i >= 0 {
			return f[i+1:]
		}
		return f
	}
	return cmd
}

func (pr *probeRun) execSeam() {
	// termEcho mirrors a worker command label and its output into the active terminal pane.
	pr.termPrompt = terminalPrompt(pr.appliance)
	// Give this run a private scratch directory on the target. Repo and bundle
	// appliances have no filesystem to write to, so they get none — their
	// workers only read an ingested store. Setup and teardown deliberately use
	// the RAW exec path: routing them through the gated tool would let the risk
	// gate refuse the very cleanup that keeps the host clean.
	if pr.appliance.Type != "repo" && pr.appliance.Type != "bundle" && pr.appliance.Type != "toolset" {
		rawExec := func(c context.Context, cmd string) (string, error) {
			// Peer first, same as sshExec above: setup and teardown must land on
			// the machine the session is actually working, or this run makes and
			// removes a scratch directory on the wrong host while the worker's
			// writes into it fail.
			if strings.TrimSpace(pr.appliance.PeerName) != "" {
				return peerExecFor(c, pr.appliance)(cmd)
			}
			if pr.appliance.Type == "command" {
				return pr.a.exec_local_ctx(c, cmd, pr.appliance.WorkDir, pr.appliance.EnvVars)
			}
			return pr.a.exec_command_ctx(c, cmd)
		}
		dir := scratch_dir(pr.id)
		if err := scratch_setup(pr.ctx, rawExec, dir); err != nil {
			// Non-fatal: the run proceeds with no sanctioned write location, which
			// only means writes gate as they otherwise would. Surfaced rather than
			// swallowed so an unexpected flurry of approval prompts is explicable.
			emit(pr.id, probeEvent{Kind: "status", Text: "Scratch directory unavailable — writes will need approval: " + err.Error()})
		} else {
			pr.scratch = dir
			pr.scratchCleanup = func() { scratch_teardown(rawExec, dir) }
		}
	}
}

// newRunTool returns a run_command tool wired to the probe-session
// shared counters above. The tool struct itself is created fresh
// per delegation (cheap); the counters persist across delegations.
func (pr *probeRun) newRunTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "run_command",
			Description: "Execute a shell command on the remote Linux system via SSH and return combined stdout+stderr. Output is capped at 10,000 characters.",
			Parameters: map[string]ToolParam{
				"command": {Type: "string", Description: "The shell command to run on the remote host."},
			},
			Required: []string{"command"},
		},
		Handler: func(args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			if cmd == "" {
				return "", fmt.Errorf("command is required")
			}
			pr.cmdMu.Lock()
			pr.cmdCount[cmd]++
			count := pr.cmdCount[cmd]
			pr.cmdMu.Unlock()
			if count > loopLimit {
				msg := fmt.Sprintf("[LOOP DETECTED] run_command(%q) has been called %d times in this session. Running it again will not produce a different result. Stop. Choose a different command, different arguments, or a different investigation strategy.", cmd, count-1)
				emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Loop detected: %q (%dx)", cmd, count-1)})
				return msg, nil
			}
			bin := pr.cmdBinary(cmd)
			emit(pr.id, probeEvent{Kind: "cmd", Text: cmd})
			if err := pr.gateCommand(cmd); err != nil {
				return "", err
			}
			result, err := pr.sshExec(cmd)
			if stripANSI(result) != "" {
				emit(pr.id, probeEvent{Kind: "output", Text: result})
			}
			pr.termEcho(cmd, result)
			if strings.Contains(result, "[exit code ") {
				// Record nonzero exits for the failure summary surfaced
				// at session end. No hard block — the model decides
				// when to pivot off a failing approach based on the
				// error text.
				reason := result
				if nl := strings.Index(reason, "\n"); nl > 0 {
					reason = reason[:nl]
				}
				if len(reason) > 120 {
					reason = reason[:120] + "…"
				}
				pr.sessionFailures = append(pr.sessionFailures, sessionFailure{Cmd: cmd, Reason: reason})
				pr.failMu.Lock()
				pr.failCount[bin]++
				pr.failMu.Unlock()
			} else {
				// Command succeeded — if this binary had prior failures this phase,
				// the agent just found a working approach after trying multiple things.
				// Force it to record the technique NOW before moving on.
				pr.failMu.Lock()
				priorFails := pr.failCount[bin]
				pr.failMu.Unlock()
				if priorFails > 0 {
					result += fmt.Sprintf("\n\n[TECHNIQUE FOUND] '%s' succeeded after %d failure(s) this phase. You MUST call record_technique NOW with the exact working command and why it worked, before doing anything else. Do not skip this step.", bin, priorFails)
				}
			}
			return result, err
		},
		NeedsConfirm: false,
	}
}

// newRunPtyTool returns a run_pty tool wired to the probe-session
// shared ptyCount. Tool struct cheap to recreate; counter persists.
func (pr *probeRun) newRunPtyTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "run_pty",
			Description: "Run a command via a PTY (pseudo-terminal) on the remote system. Use this for commands that require a TTY: password prompts (su, sudo, mysql -p), interactive programs (python3, irb, psql), or anything that checks isatty(). Output is captured with ANSI codes stripped. Provide the 'input' parameter to send responses to prompts (newline-separated).",
			Parameters: map[string]ToolParam{
				"command":     {Type: "string", Description: "The command to run on the remote host."},
				"input":       {Type: "string", Description: "Optional lines to send to stdin after the command starts (newline-separated). Use for passwords, menu selections, shell commands inside an interactive session, etc."},
				"timeout_sec": {Type: "integer", Description: "Seconds to wait for the command to finish (default 15, max 60)."},
			},
			Required: []string{"command"},
		},
		Handler: func(args map[string]any) (string, error) {
			cmd, _ := args["command"].(string)
			if cmd == "" {
				return "", fmt.Errorf("command is required")
			}
			pr.ptyCount[cmd]++
			if pr.ptyCount[cmd] > loopLimit {
				msg := fmt.Sprintf("[LOOP DETECTED] run_pty(%q) has been called %d times in this session. Stop. Use a different command or approach.", cmd, pr.ptyCount[cmd]-1)
				emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Loop detected (pty): %q (%dx)", cmd, pr.ptyCount[cmd]-1)})
				return msg, nil
			}
			inputText, _ := args["input"].(string)
			timeout := 15
			if t, ok := args["timeout_sec"].(float64); ok && t > 0 {
				timeout = int(t)
				if timeout > 60 {
					timeout = 60
				}
			}

			emit(pr.id, probeEvent{Kind: "cmd", Text: "pty: " + cmd})
			if err := pr.gateCommand(cmd); err != nil {
				return "", err
			}
			// The input lines are commands typed into the interactive session
			// the command above opened — `run_pty("bash", input: "rm -rf …")`
			// is a shell command by another route, so each line is gated too.
			// A password line classifies as benign and passes without ever
			// being shown in a confirmation prompt.
			for _, line := range strings.Split(inputText, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				if err := pr.gateCommand(line); err != nil {
					return "", err
				}
			}

			// Through newPTYSession, never the client directly: a
			// peer-reached appliance has no local ssh.Client, and this is
			// where the nil used to be dereferenced. The tool is also not
			// offered in that case (see ptyLocal below) — this is the
			// second line of defence.
			sess, err := newPTYSession(pr.a.conn)
			if err != nil {
				// Attempt reconnect on connection-level errors.
				errMsg := err.Error()
				isConnErr := strings.Contains(errMsg, "EOF") ||
					strings.Contains(errMsg, "connection reset") ||
					strings.Contains(errMsg, "broken pipe") ||
					strings.Contains(errMsg, "new SSH session")
				if isConnErr {
					emit(pr.id, probeEvent{Kind: "status", Text: "SSH connection lost — reconnecting…"})
					dropConn(pr.userID, pr.appliance.ID)
					newClient, rerr := acquireConn(pr.userID, pr.appliance)
					if rerr != nil {
						return fmt.Sprintf("[SSH DISCONNECTED — reconnect failed: %v. Stop issuing SSH commands; the session must be restarted.]", rerr), nil
					}
					pr.a.conn = newClient
					emit(pr.id, probeEvent{Kind: "status", Text: "SSH reconnected."})
					sess, err = newPTYSession(pr.a.conn)
				}
				if err != nil {
					return "", fmt.Errorf("new SSH session: %w", err)
				}
			}
			defer sess.Close()

			modes := ssh.TerminalModes{
				ssh.ECHO:          0,
				ssh.TTY_OP_ISPEED: 14400,
				ssh.TTY_OP_OSPEED: 14400,
			}
			if err := sess.RequestPty("xterm", 50, 220, modes); err != nil {
				return "", fmt.Errorf("PTY request failed: %w", err)
			}

			stdinPipe, err := sess.StdinPipe()
			if err != nil {
				return "", fmt.Errorf("stdin pipe: %w", err)
			}

			var outBuf bytes.Buffer
			sess.Stdout = &outBuf
			sess.Stderr = &outBuf

			if err := sess.Start(cmd); err != nil {
				return "", fmt.Errorf("start: %w", err)
			}

			// Send input lines with a short delay between each to let prompts appear.
			if inputText != "" {
				time.Sleep(400 * time.Millisecond)
				for _, line := range strings.Split(inputText, "\n") {
					fmt.Fprintln(stdinPipe, line)
					time.Sleep(200 * time.Millisecond)
				}
			}

			done := make(chan error, 1)
			go func() { done <- sess.Wait() }()

			select {
			case <-done:
			case <-time.After(time.Duration(timeout) * time.Second):
				stdinPipe.Write([]byte{3}) // Ctrl+C
				time.Sleep(200 * time.Millisecond)
				stdinPipe.Write([]byte{4}) // Ctrl+D
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			case <-pr.ctx.Done():
				sess.Close()
				return "", pr.ctx.Err()
			}
			stdinPipe.Close()

			result := stripANSI(outBuf.String())
			if len(result) > max_output {
				result = result[:max_output] + fmt.Sprintf("\n... [truncated — %d chars total]", len(result))
			}
			if result != "" {
				emit(pr.id, probeEvent{Kind: "output", Text: result})
			}
			pr.termEcho("pty: "+cmd, result)
			// If this PTY session succeeded after prior attempts, force technique recording.
			if pr.ptyCount[cmd] > 1 {
				result += fmt.Sprintf("\n\n[TECHNIQUE FOUND] run_pty(%q) succeeded after %d attempt(s). You MUST call record_technique NOW with the exact working command and input sequence, before doing anything else.", cmd, pr.ptyCount[cmd]-1)
			}
			return result, nil
		},
		NeedsConfirm: false,
	}
}

// withFreshRunTool clones a tools slice, replacing run_command and run_pty
// entries with fresh instances so each invocation gets isolated counters.
func (pr *probeRun) withFreshRunTool(base []AgentToolDef) []AgentToolDef {
	result := make([]AgentToolDef, len(base))
	copy(result, base)
	for i, t := range result {
		switch t.Tool.Name {
		case "run_command":
			result[i] = pr.newRunTool()
		case "run_pty":
			result[i] = pr.newRunPtyTool()
		}
	}
	return result
}

func (pr *probeRun) execTools() {
	// Probe-session shared state for loop / failure detection.
	// Previously these maps lived inside newRunTool() so each invocation
	// (each orchestrator delegation that spawned a worker session)
	// started with a fresh slate. That defeated cross-delegation loop
	// detection: orchestrator could re-delegate "query the database"
	// 10 times, each worker would run the same `mysql -e ...` once,
	// and the per-session loopLimit=3 check never fired across the
	// boundary. Lifting to probe-session scope means after 3 cumulative
	// runs of the same command — across ANY worker session in this
	// probe — the LOOP DETECTED message fires and forces the
	// orchestrator to pivot.
	//
	// Trade: phase-boundary "reset" semantics from before are gone.
	// If you ever want per-phase budgets, wire a reset hook from the
	// orchestrator side rather than reverting to per-tool maps.
	pr.cmdCount = make(map[string]int)
	pr.failCount = make(map[string]int)
	// read_log — safe, targeted log reader.
	pr.read_log_tool = AgentToolDef{
		Tool: Tool{
			Name:        "read_log",
			Description: "Read the last N lines from a log file on the remote system, with optional grep filter. Safer and faster than run_command for log inspection.",
			Parameters: map[string]ToolParam{
				"path":   {Type: "string", Description: "Absolute path to the log file."},
				"lines":  {Type: "integer", Description: "Number of lines to read from the end (default 100, max 500)."},
				"filter": {Type: "string", Description: "Optional grep pattern to filter output (case-insensitive)."},
			},
			Required: []string{"path"},
		},
		Handler: func(args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			lines := 100
			if n, ok := args["lines"].(float64); ok && n > 0 {
				lines = int(n)
				if lines > 500 {
					lines = 500
				}
			}
			filter, _ := args["filter"].(string)

			var label string
			var cmd string
			if filter != "" {
				label = fmt.Sprintf("read_log %s (last %d lines, filter: %s)", path, lines, filter)
				cmd = fmt.Sprintf("tail -n %d %s 2>/dev/null | grep -i %s 2>/dev/null", lines, shellQuote(path), shellQuote(filter))
			} else {
				label = fmt.Sprintf("read_log %s (last %d lines)", path, lines)
				cmd = fmt.Sprintf("tail -n %d %s 2>/dev/null", lines, shellQuote(path))
			}
			emit(pr.id, probeEvent{Kind: "cmd", Text: label})
			result, err := pr.sshExec(cmd)
			if stripANSI(result) != "" {
				emit(pr.id, probeEvent{Kind: "output", Text: result})
			}
			pr.termEcho(label, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// search_logs — cross-file pattern search.
	pr.search_logs_tool = AgentToolDef{
		Tool: Tool{
			Name:        "search_logs",
			Description: "Search one or more log files for a pattern. Returns matching lines with surrounding context.",
			Parameters: map[string]ToolParam{
				"pattern": {Type: "string", Description: "grep pattern to search for (case-insensitive)."},
				"paths": {
					Type:        "array",
					Description: "List of absolute log file paths to search. If empty, searches /var/log/ recursively.",
					Items:       &ToolParam{Type: "string"},
				},
				"context_lines": {Type: "integer", Description: "Lines of context around each match (default 2, max 5)."},
			},
			Required: []string{"pattern"},
		},
		Handler: func(args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			ctx_lines := 2
			if n, ok := args["context_lines"].(float64); ok && n >= 0 {
				ctx_lines = int(n)
				if ctx_lines > 5 {
					ctx_lines = 5
				}
			}
			var pathArgs string
			if raw, ok := args["paths"]; ok {
				if arr, ok := raw.([]any); ok && len(arr) > 0 {
					var parts []string
					for _, p := range arr {
						if s, ok := p.(string); ok && s != "" {
							parts = append(parts, shellQuote(s))
						}
					}
					pathArgs = strings.Join(parts, " ")
				}
			}
			if pathArgs == "" {
				pathArgs = "/var/log/"
			}

			label := fmt.Sprintf("search_logs %q in %s", pattern, pathArgs)
			cmd := fmt.Sprintf("grep -r -i -C %d %s %s 2>/dev/null | head -300",
				ctx_lines, shellQuote(pattern), pathArgs)
			emit(pr.id, probeEvent{Kind: "cmd", Text: label})
			result, err := pr.sshExec(cmd)
			if stripANSI(result) != "" {
				emit(pr.id, probeEvent{Kind: "output", Text: result})
			}
			pr.termEcho(label, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// Probe-session shared PTY loop counter — same rationale as
	// cmdCount above. Per-tool isolation defeated cross-delegation
	// detection.
	pr.ptyCount = make(map[string]int)
}

// auditTechniques asks the worker model which stored techniques the new one
// supersedes and removes those lines. It reads the CURRENT stored value
// under the lock rather than the snapshot it was given, so an append that
// raced ahead of it survives.
func (pr *probeRun) auditTechniques(udb Database, applianceID, existing, technique string) {
	auditCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	auditPrompt := "You are auditing a list of stored techniques for a specific system. " +
		"A new technique is about to be added. Identify any existing techniques that the new one " +
		"supersedes, contradicts, or makes redundant (e.g. an old auth method that is now wrong, " +
		"a path that has changed, an approach that the new one replaces). " +
		"Reply with ONLY the exact lines to remove, one per line. " +
		"If nothing should be removed, reply with exactly: NONE"
	auditMsg := fmt.Sprintf("Existing techniques:\n%s\n\nNew technique being added:\n- %s", existing, technique)
	auditResp, auditErr := pr.a.WorkerChat(auditCtx, []Message{{Role: "user", Content: auditMsg}},
		WithSystemPrompt(auditPrompt), WithMaxTokens(512))
	if auditErr != nil || auditResp == nil {
		return
	}
	removal := strings.TrimSpace(auditResp.Content)
	if removal == "" || removal == "NONE" {
		return
	}
	pr.techniqueMu.Lock()
	defer pr.techniqueMu.Unlock()
	pruned := pruneTechniqueLines(techniquesFor(udb, applianceID), removal)
	udb.Set(techniquesTable, applianceID, pruned)
}

func (pr *probeRun) memoryTools() {
	// note_lesson — append a correction or lesson to the persistent notes for this appliance.
	pr.note_lesson_tool = AgentToolDef{
		Tool: Tool{
			Name:        "note_lesson",
			Description: "Append a lesson or correction to the persistent notes for this appliance. Call this after discovering a mistake, a wrong assumption, or a non-obvious quirk about this system (e.g. 'sudo is not installed', 'mysql uses socket /tmp/mysql.sock not /var/run', 'journalctl requires sudo'). Notes are re-injected into every future session so the same mistake is not repeated.",
			Parameters: map[string]ToolParam{
				"note": {Type: "string", Description: "The lesson to record. Be concise and specific — one sentence per call."},
			},
			Required: []string{"note"},
		},
		Handler: func(args map[string]any) (string, error) {
			note, _ := args["note"].(string)
			if note == "" {
				return "", fmt.Errorf("note is required")
			}
			if pr.udb == nil {
				return "", fmt.Errorf("no database")
			}
			var existing string
			pr.udb.Get(notesTable, pr.appliance.ID, &existing)
			entry := fmt.Sprintf("- %s (%s)\n", note, time.Now().Format("2006-01-02"))
			pr.udb.Set(notesTable, pr.appliance.ID, existing+entry)
			recordScopedExplicit(pr.appliance, note) // gotcha -> Explicit Memory (always-in-prompt Shortcuts layer)
			emit(pr.id, probeEvent{Kind: "status", Text: "Noted: " + note})
			return "noted", nil
		},
		NeedsConfirm: false,
	}

	// record_technique — save a successful approach for future sessions.
	pr.record_technique_tool = AgentToolDef{
		Tool: Tool{
			Name: "record_technique",
			Description: "Record a technique that worked on this system — a successful approach, correct command syntax, " +
				"working auth method, or non-obvious way to accomplish something. " +
				"Call this whenever you figure out HOW to do something that wasn't obvious: " +
				"e.g. 'MySQL root login works without a password via unix socket: mysql -u root', " +
				"'PostgreSQL uses peer auth — connect as postgres user: sudo -u postgres psql', " +
				"'Redis requires AUTH token found in /etc/redis/redis.conf', " +
				"'Python app uses venv at /opt/app/venv/bin/python'. " +
				"Techniques are injected at the start of every future session so you know exactly how to access things. " +
				"DATABASE AUTH IS MANDATORY: the moment any database login succeeds, record_technique MUST be called with the exact working command — this prevents re-discovery on every future session.",
			Parameters: map[string]ToolParam{
				"technique": {Type: "string", Description: "Concise description of what works and exactly how. Include the specific command or path."},
			},
			Required: []string{"technique"},
		},
		Handler: func(args map[string]any) (string, error) {
			technique, _ := args["technique"].(string)
			if technique == "" {
				return "", fmt.Errorf("technique is required")
			}
			if pr.udb == nil {
				return "", fmt.Errorf("no database")
			}
			// The new technique is stored FIRST and the audit of older entries runs
			// in the background. The audit is an LLM call, and it used to sit
			// inline: every record_technique cost the worker a full model
			// round-trip before its tool result came back, on the same backend
			// the worker itself was waiting on. Nothing downstream needs the
			// prune to have happened — a superseded line lingers for one probe at
			// most, and the next read sees the pruned list.
			existing := techniquesFor(pr.udb, pr.appliance.ID)
			pr.techniqueMu.Lock()
			recordTechnique(pr.udb, pr.appliance.ID, technique)
			pr.techniqueMu.Unlock()
			recordScopedExplicit(pr.appliance, technique) // working command -> Explicit Memory (always-in-prompt Shortcuts layer)
			emit(pr.id, probeEvent{Kind: "status", Text: "Technique saved: " + technique})
			if existing != "" {
				go pr.auditTechniques(pr.udb, pr.appliance.ID, existing, technique)
			}
			return "technique recorded", nil
		},
		NeedsConfirm: false,
	}

	// record_discovery — capture a key breakthrough finding.
	pr.record_discovery_tool = AgentToolDef{
		Tool: Tool{
			Name: "record_discovery",
			Description: "Record a key breakthrough that directly solves a goal or constitutes a major finding. " +
				"Call this when you: successfully authenticated to a database or service and confirmed what's inside, " +
				"fully traced a request routing chain, found credentials or secrets that unlock further access, " +
				"identified how the application accesses a resource (DB driver, ORM setup, connection method), " +
				"or confirmed any significant security or architectural finding. " +
				"Discoveries are surfaced at the TOP of every future session as pre-established knowledge — " +
				"anything recorded here will not be re-investigated. " +
				"This is NOT for routine facts or techniques. Only call it when you have answered a significant goal with real evidence. " +
				"DATABASE ACCESS: when you successfully enter a database and see its schemas/tables, call record_discovery with the full access path, credentials, and what you found inside.",
			Parameters: map[string]ToolParam{
				"title":    {Type: "string", Description: "One-line summary, e.g. 'Production PostgreSQL access confirmed' or 'Full request routing chain mapped'."},
				"finding":  {Type: "string", Description: "Full narrative: what you found, where, exact values (credentials, paths, ports, schema names, route patterns), and why it matters. Include the evidence — commands run and their output."},
				"category": {Type: "string", Description: "One of: database | credentials | routing | service | code | security | config | general"},
			},
			Required: []string{"title", "finding"},
		},
		Handler: func(args map[string]any) (string, error) {
			title, _ := args["title"].(string)
			finding, _ := args["finding"].(string)
			category, _ := args["category"].(string)
			if strings.TrimSpace(title) == "" || strings.TrimSpace(finding) == "" {
				return "", fmt.Errorf("title and finding are required")
			}
			if pr.udb == nil {
				return "", fmt.Errorf("no database")
			}
			storeDiscovery(pr.udb, pr.appliance.ID, title, finding, category)
			recordScopedReference(pr.ctx, pr.appliance, "discoveries", title, finding) // dual-write to the orchestrate scope (lead migration, slice 1)
			emit(pr.id, probeEvent{Kind: "discovery", Text: "★ " + strings.TrimSpace(title)})
			return "discovery recorded", nil
		},
		NeedsConfirm: false,
	}

	// store_fact — persist an APPLIANCE-WIDE property (not a per-component fact).
	pr.store_fact_tool = AgentToolDef{
		Tool: Tool{
			Name:        "store_fact",
			Description: "Save an APPLIANCE-WIDE property (os, hostname, kernel, arch, timezone, primary role) under a short key; same key overwrites. Component-specific details — a service's version, port, or config path — go on that component's own entity via link_entities subject_attrs, NOT here. ttl='short' for volatile state, default 'long'. (The 'What to Record' section has the full routing guide.)",
			Parameters: map[string]ToolParam{
				"key":   {Type: "string", Description: "Short appliance-wide key, e.g. 'os', 'hostname', 'kernel', 'arch'. NOT a component-specific key like 'nginx_version' — that goes in link_entities subject_attrs."},
				"value": {Type: "string", Description: "The fact value."},
				"ttl":   {Type: "string", Description: "Freshness window: 'short' (re-verify after 30 min, for volatile state) or 'long' (trust for 24h, for stable config/versions). Default: 'long'."},
				"tags": {
					Type:        "array",
					Description: "Optional labels for cross-appliance search, e.g. 'database', 'security', 'network'.",
					Items:       &ToolParam{Type: "string"},
				},
			},
			Required: []string{"key", "value"},
		},
		Handler: func(args map[string]any) (string, error) {
			key, _ := args["key"].(string)
			value, _ := args["value"].(string)
			if key == "" || value == "" {
				return "", fmt.Errorf("key and value are required")
			}
			ttl, _ := args["ttl"].(string)
			// Cutover: facts now live ONLY in the appliance scope (graph attrs);
			// ssh_facts is retired. Short-TTL (ephemeral) facts are not persisted
			// — the graph has no expiry, and live state should be re-probed.
			recordScopedApplianceFact(pr.appliance, key, value, ttl)
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Stored fact: %s = %s", key, value)})
			return "fact stored", nil
		},
		NeedsConfirm: false,
	}

	// link_entities — record a RELATIONSHIP in this system's graph map so the
	// knowledge is a real topology (services, configs, dependencies) instead of
	// flat facts piled on one node. store_fact is for appliance-wide properties;
	// link_entities is for everything with structure.
	pr.link_entities_tool = AgentToolDef{
		Tool: Tool{
			Name:        "link_entities",
			Description: "Record a RELATIONSHIP between two named parts of this system — the structured graph map. Subject-relation-object, e.g. subject='nginx' relation='proxies to' object='app on :8080'. Entities auto-merge by name; put non-relational details (version, path, port) in subject_attrs. Call it whenever you learn how parts connect. (store_fact is only for appliance-wide properties; the 'What to Record' section has the full routing guide.)",
			Parameters: map[string]ToolParam{
				"subject":       {Type: "string", Description: "The subject entity's name, e.g. 'nginx', 'app', 'postgres'."},
				"subject_kind":  {Type: "string", Description: "Subject type: service, app, database, host, file, process, port, or thing. Defaults to thing."},
				"relation":      {Type: "string", Description: "The relationship verb, e.g. 'runs on', 'proxies to', 'connects to', 'depends on', 'listens on', 'reads config from'."},
				"object":        {Type: "string", Description: "The object entity's name, e.g. 'postgres', '/etc/nginx/nginx.conf', 'port 5432'."},
				"object_kind":   {Type: "string", Description: "Object type (see subject_kind). Defaults to thing."},
				"subject_attrs": {Type: "object", Description: "Optional non-relational facts about the subject as key/value strings, e.g. {\"version\": \"1.24\", \"port\": \"443\"}."},
				"note":          {Type: "string", Description: "Optional qualifier on the relationship, e.g. 'over unix socket'."},
				"replace":       {Type: "boolean", Description: "True if this CORRECTS a single-valued relation (removes the prior value for this subject+relation)."},
			},
			Required: []string{"subject", "relation", "object"},
		},
		Handler: func(args map[string]any) (string, error) {
			subject, _ := args["subject"].(string)
			relation, _ := args["relation"].(string)
			object, _ := args["object"].(string)
			if strings.TrimSpace(subject) == "" || strings.TrimSpace(relation) == "" || strings.TrimSpace(object) == "" {
				return "", fmt.Errorf("subject, relation, and object are required")
			}
			subjectKind, _ := args["subject_kind"].(string)
			objectKind, _ := args["object_kind"].(string)
			note, _ := args["note"].(string)
			replace, _ := args["replace"].(bool)
			var attrs map[string]string
			if raw, ok := args["subject_attrs"].(map[string]any); ok {
				attrs = make(map[string]string)
				for k, v := range raw {
					if s, ok := v.(string); ok {
						attrs[k] = s
					}
				}
			}
			if err := recordScopedLink(pr.appliance, subjectKind, subject, attrs, relation, objectKind, object, note, replace); err != nil {
				return "", err
			}
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Linked: %s → %s → %s", subject, relation, object)})
			return "relationship recorded", nil
		},
		NeedsConfirm: false,
	}

	// store_rule — persist a standing instruction the user has established.
	pr.store_rule_tool = AgentToolDef{
		Tool: Tool{
			Name:        "store_rule",
			Description: "Save a standing instruction or preference the user has expressed about how to work with this system. Call this when the user states a rule, preference, or convention they want followed in all future sessions — e.g. 'always check staging before production', 'never restart the web server without warning', 'use sudo for all service commands'. Rules persist across sessions and are injected into every future prompt.",
			Parameters: map[string]ToolParam{
				"rule": {Type: "string", Description: "The standing instruction to remember, written as a clear directive."},
			},
			Required: []string{"rule"},
		},
		Handler: func(args map[string]any) (string, error) {
			rule, _ := args["rule"].(string)
			if strings.TrimSpace(rule) == "" {
				return "", fmt.Errorf("rule is required")
			}
			if pr.ownerUDB == nil {
				return "", fmt.Errorf("no database")
			}
			// Rules live on the owner's store so they're shared across everyone
			// using the appliance (see the rules read above).
			storeRule(pr.ownerUDB, pr.appliance.ID, rule)
			preview := rule
			if len(preview) > 80 {
				preview = preview[:80] + "…"
			}
			emit(pr.id, probeEvent{Kind: "status", Text: "Rule saved: " + preview})
			return "rule saved", nil
		},
	}
}

func (pr *probeRun) readTools() {
	// count_lines — check file size before deciding how to read it.
	pr.count_lines_tool = AgentToolDef{
		Tool: Tool{
			Name:        "count_lines",
			Description: "Return the total number of lines in a file on the remote system. Use this before read_range or before catting a file to know whether it will fit in one read or needs pagination.",
			Parameters: map[string]ToolParam{
				"path": {Type: "string", Description: "Absolute path to the file."},
			},
			Required: []string{"path"},
		},
		Handler: func(args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			emit(pr.id, probeEvent{Kind: "cmd", Text: "count_lines " + path})
			result, err := pr.sshExec(fmt.Sprintf("wc -l %s 2>/dev/null", shellQuote(path)))
			pr.termEcho("wc -l "+path, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// read_range — read a specific line range from a file; avoids re-running expensive commands.
	pr.read_range_tool = AgentToolDef{
		Tool: Tool{
			Name: "read_range",
			Description: "Read a specific range of lines from a file on the remote system. " +
				"Use this to page through large files without re-running the original command. " +
				"Call count_lines first to know total line count, then page through in chunks up to 300 lines at a time.",
			Parameters: map[string]ToolParam{
				"path":       {Type: "string", Description: "Absolute path to the file."},
				"start_line": {Type: "integer", Description: "First line to return (1-indexed)."},
				"end_line":   {Type: "integer", Description: "Last line to return (inclusive). Maximum 300 lines per call."},
			},
			Required: []string{"path", "start_line", "end_line"},
		},
		Handler: func(args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}
			start := 1
			if v, ok := args["start_line"].(float64); ok && v >= 1 {
				start = int(v)
			}
			end := start + 99
			if v, ok := args["end_line"].(float64); ok && v >= 1 {
				end = int(v)
			}
			if end-start > 299 {
				end = start + 299
			}
			if end < start {
				end = start
			}
			label := fmt.Sprintf("read_range %s lines %d–%d", path, start, end)
			cmd := fmt.Sprintf("awk 'NR>=%d && NR<=%d' %s 2>/dev/null", start, end, shellQuote(path))
			emit(pr.id, probeEvent{Kind: "cmd", Text: label})
			result, err := pr.sshExec(cmd)
			if stripANSI(result) != "" {
				emit(pr.id, probeEvent{Kind: "output", Text: result})
			}
			pr.termEcho(label, result)
			return result, err
		},
		NeedsConfirm: false,
	}

	// search_facts — retrieve facts from the persistent knowledge base.
	pr.search_facts_tool = AgentToolDef{
		Tool: Tool{
			Name:        "search_facts",
			Description: "Search stored facts across all appliances by keyword. Checks fact keys, values, and tags. Call this before running SSH commands — the answer may already be in persistent memory.",
			Parameters: map[string]ToolParam{
				"query":     {Type: "string", Description: "Substring to search in fact keys, values, and tags."},
				"appliance": {Type: "string", Description: "Optional appliance name or ID filter."},
			},
			Required: []string{"query"},
		},
		Handler: func(args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			// Cutover: facts live in THIS appliance's scope (graph attrs). Cross-
			// appliance search is no longer supported — each appliance is its own
			// scope — so the optional "appliance" filter is ignored.
			attrs := scopedApplianceFacts(pr.udb, pr.appliance)
			keys := make([]string, 0, len(attrs))
			for k := range attrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			q := strings.ToLower(query)
			var lines []string
			for _, k := range keys {
				if strings.Contains(strings.ToLower(k), q) || strings.Contains(strings.ToLower(attrs[k]), q) {
					lines = append(lines, "- "+k+": "+attrs[k])
				}
			}
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_facts %q: %d result(s)", query, len(lines))})
			if len(lines) == 0 {
				return "no facts found", nil
			}
			return strings.Join(lines, "\n"), nil
		},
		NeedsConfirm: false,
	}

	// search_knowledge searches the curated knowledge collections the owner LINKED
	// to this appliance (runbooks, vendor docs, guides) — authoritative reference
	// material to ground answers ALONGSIDE what the worker finds on the system
	// itself. Local-only: a vector search over the owner's collections (query
	// embedded via the local llama.cpp server); it never touches the live system
	// or any third party. Only attached to the worker when the appliance has
	// linked collections.
	pr.search_knowledge_tool = AgentToolDef{
		Tool: Tool{
			Name:        "search_knowledge",
			Description: "Search the curated KNOWLEDGE linked to this appliance (runbooks, vendor docs, guides the owner attached) for material relevant to the task. Returns the top matching passages with their source. Use it to ground your answer in authoritative reference material — it does NOT touch the live system, so pair it with the system-probing tools rather than replacing them.",
			Parameters: map[string]ToolParam{
				"query": {Type: "string", Description: "What to look up, in natural language."},
				"k":     {Type: "number", Description: "Max passages to return (default 5, max 12)."},
			},
			Required: []string{"query"},
		},
		Handler: func(args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			query = strings.TrimSpace(query)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			k := 5
			if v, ok := args["k"].(float64); ok && int(v) > 0 {
				k = int(v)
				if k > 12 {
					k = 12
				}
			}
			hits := SearchCollections(pr.ctx, CollectionsDB(), pr.ownerUser, pr.appliance.Collections, query, k)
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_knowledge %q: %d passage(s)", query, len(hits))})
			if len(hits) == 0 {
				return "No matching passages in the linked knowledge.", nil
			}
			var b strings.Builder
			for i, h := range hits {
				label := strings.TrimSpace(h.Title)
				if label == "" {
					label = h.Source
				}
				fmt.Fprintf(&b, "%d. [%s] %s\n\n", i+1, label, strings.TrimSpace(h.Text))
			}
			return strings.TrimSpace(b.String()), nil
		},
		NeedsConfirm: false,
	}

	if pr.udb != nil {
		if disc := discoveriesFor(pr.udb, pr.appliance.ID); len(disc) > 0 {
			pr.cachedDiscoveries = formatDiscoveries(disc)
		}
		// Facts come from the appliance's SCOPE (graph entity attrs), not
		// ssh_facts. Graph attrs carry no per-fact age, so the prompts lean on
		// the standing "always re-probe live state" rule rather than age cutoffs.
		pr.cachedFacts = scopedFactsBlock(pr.udb, pr.appliance)
		var notes string
		if pr.udb.Get(notesTable, pr.appliance.ID, &notes) && strings.TrimSpace(notes) != "" {
			pr.cachedNotes = strings.TrimSpace(notes)
		}
		pr.cachedTechniques = techniquesFor(pr.udb, pr.appliance.ID)
		if rules := rulesForAppliance(pr.ownerUDB, pr.appliance.ID); len(rules) > 0 {
			// Rules are the owner's operator directives for THIS appliance — read
			// from the owner's store so a shared appliance applies the same
			// standing instructions for everyone, not just the owner.
			pr.cachedRules = formatRules(rules)
		}
	}
}

func (pr *probeRun) reportTools() {
	// watch_condition — register a 1-minute expect-style poll until a condition is met.
	pr.watch_condition_tool = AgentToolDef{
		Tool: Tool{
			Name: "watch_condition",
			Description: "Register an expect-style watch: runs the given command every minute until " +
				"the output contains the success pattern, then stores the result. " +
				"Use this when you've started something that takes time — a backup, a service restart, " +
				"a migration — and want to know when it finishes without blocking. " +
				"The watch fires silently in the background; the result is in stored facts on next session.",
			Parameters: map[string]ToolParam{
				"task":            {Type: "string", Description: "What you are waiting for (human description)."},
				"command":         {Type: "string", Description: "SSH command to run each minute to check the condition."},
				"success_pattern": {Type: "string", Description: "Substring that must appear in command output for the condition to be considered met."},
				"timeout_minutes": {Type: "string", Description: "Give up after this many minutes if the condition never matches. Default 60."},
			},
			Required: []string{"task", "command", "success_pattern"},
		},
		Handler: func(args map[string]any) (string, error) {
			task, _ := args["task"].(string)
			command, _ := args["command"].(string)
			pattern, _ := args["success_pattern"].(string)
			if task == "" || command == "" || pattern == "" {
				return "", fmt.Errorf("task, command, and success_pattern are required")
			}
			timeoutMin := 60
			if s, _ := args["timeout_minutes"].(string); s != "" {
				var n int
				if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
					timeoutMin = n
				}
			}
			now := time.Now()
			w := ScheduledWatch{
				ID:          UUIDv4(),
				ApplianceID: pr.appliance.ID,
				UserID:      pr.userID,
				Task:        task,
				Command:     command,
				Pattern:     pattern,
				TimeoutAt:   now.Add(time.Duration(timeoutMin) * time.Minute).Format(time.RFC3339),
				NextRunAt:   now.Add(60 * time.Second).Format(time.RFC3339),
				Created:     now.Format(time.RFC3339),
			}
			storeWatch(pr.T.DB, w)
			emit(pr.id, probeEvent{Kind: "watch", Text: fmt.Sprintf("Watching: %s (every 60s, up to %d min)", task, timeoutMin)})
			return fmt.Sprintf("Watch registered (id: %s). Will check every 60 seconds for up to %d minutes for pattern %q in: %s", w.ID[:8], timeoutMin, pattern, command), nil
		},
		NeedsConfirm: false,
	}

	// list_watches — show active watches for this appliance.
	pr.list_watches_tool = AgentToolDef{
		Tool: Tool{
			Name:        "list_watches",
			Description: "List active watches registered for this appliance.",
			Parameters:  map[string]ToolParam{},
		},
		Handler: func(args map[string]any) (string, error) {
			watches := listWatchesForAppliance(pr.T.DB, pr.appliance.ID)
			if len(watches) == 0 {
				return "No active watches.", nil
			}
			var b strings.Builder
			for _, w := range watches {
				b.WriteString(fmt.Sprintf("- [%s] %s — checking: %s (pattern: %q, timeout: %s)\n",
					w.ID[:8], w.Task, w.Command, w.Pattern, w.TimeoutAt))
			}
			return b.String(), nil
		},
		NeedsConfirm: false,
	}

	pr.save_to_codewriter_tool = AgentToolDef{
		Tool: Tool{
			Name:        "save_to_codewriter",
			Description: "Save a SQL query, shell script, or code snippet to the user's CodeWriter library in gohort. This is a local save action — do NOT run anything on the appliance. Use this when the user asks to save the script/query for later reuse rather than (or in addition to) running it immediately.",
			Parameters: map[string]ToolParam{
				"name": {Type: "string", Description: "Short descriptive name for the snippet (e.g. 'Active connections by database')."},
				"lang": {Type: "string", Description: "Language or type: 'sql', 'bash', 'python', 'go', 'javascript', 'text', etc."},
				"code": {Type: "string", Description: "The full script or query text to save."},
			},
			Required: []string{"name", "lang", "code"},
		},
		Handler: func(args map[string]any) (string, error) {
			if SaveSnippetFunc == nil {
				return "", fmt.Errorf("CodeWriter is not available")
			}
			name, _ := args["name"].(string)
			lang, _ := args["lang"].(string)
			code, _ := args["code"].(string)
			if name == "" || code == "" {
				return "", fmt.Errorf("name and code are required")
			}
			id, err := SaveSnippetFunc(pr.userID, name, lang, code)
			if err != nil {
				return "", fmt.Errorf("save failed: %w", err)
			}
			return fmt.Sprintf("Saved to CodeWriter as %q (id: %s).", name, id), nil
		},
		NeedsConfirm: false,
	}

	pr.save_to_techwriter_tool = AgentToolDef{
		Tool: Tool{
			Name:        "save_to_techwriter",
			Description: "Save a report, runbook, findings summary, or any prose document to the user's TechWriter library in gohort. This is a local save action — do NOT run anything on the appliance or search for TechWriter on the remote system. Use this when the user asks to document findings, save a report, or create a runbook from the session results.",
			Parameters: map[string]ToolParam{
				"subject": {Type: "string", Description: "Title or subject of the document (e.g. 'Disk usage report – web01', 'MySQL slow query runbook')."},
				"body":    {Type: "string", Description: "Full document body in markdown."},
			},
			Required: []string{"subject", "body"},
		},
		Handler: func(args map[string]any) (string, error) {
			if SaveArticleFunc == nil {
				return "", fmt.Errorf("TechWriter is not available")
			}
			subject, _ := args["subject"].(string)
			body, _ := args["body"].(string)
			if subject == "" || body == "" {
				return "", fmt.Errorf("subject and body are required")
			}
			id, err := SaveArticleFunc(pr.userID, subject, body)
			if err != nil {
				return "", fmt.Errorf("save failed: %w", err)
			}
			return fmt.Sprintf("Saved to TechWriter as %q (id: %s).", subject, id), nil
		},
		NeedsConfirm: false,
	}

	// list_guides / push_to_guide — the user asked to "add what I look up to a
	// guide". These write into the user's Guides via the generic core
	// DocumentTarget seam (guides registers itself; servitor never imports it),
	// same local-write posture as save_to_techwriter. Content lands as a new
	// section the user can polish in the Guides app; it's a revision like any edit.
	pr.list_guides_tool = AgentToolDef{
		Tool: Tool{
			Name:        "list_guides",
			Description: "List the user's existing guides (living multi-section documents in the gohort Guides app), so you can pick the right one to push a finding into with push_to_guide. Local read — do NOT look for guides on the remote system. No arguments.",
		},
		Handler: func(args map[string]any) (string, error) {
			ds := ListDocuments(pr.userID, "guide")
			if len(ds) == 0 {
				return "The user has no guides yet. push_to_guide with a new guide name will create one.", nil
			}
			var b strings.Builder
			b.WriteString("The user's guides (push into one by its name, or a new name to create one):\n")
			for _, d := range ds {
				fmt.Fprintf(&b, "- %s\n", d.Title)
			}
			return strings.TrimRight(b.String(), "\n"), nil
		},
		NeedsConfirm: false,
	}

	// record_finding — report something worth documenting WITHOUT choosing where
	// it goes. The complement to push_to_guide, and the one a worker should
	// normally reach for: deciding which guide a finding belongs in and what the
	// section is called are editorial judgments this worker is not positioned to
	// make (it can see one probe, not the corpus, and not the other findings from
	// the same run). The Guide Curator batches these and decides. push_to_guide
	// stays for the case where the USER named a destination in the request.
	// See docs/guides-curator.md.
	pr.record_finding_tool = AgentToolDef{
		Tool: Tool{
			Name:        "record_finding",
			Description: "Report something you learned that is worth DOCUMENTING, without choosing a destination. Use this for anything durable a future reader would want: a config value, a path, a working procedure, a failure mode and its cause. A curator later decides which guide it belongs in, merges it with related findings, and drops what isn't worth keeping — so you do NOT name a guide or a section. Do NOT report that a probe ran, or that a service was up at one moment; that is not documentation. Local save action — never run anything on the appliance for this.",
			Parameters: map[string]ToolParam{
				"topic":      {Type: "string", Description: "One line naming what this is ABOUT — e.g. \"nginx TLS cert renewal\", \"scheduler queue timeout\". Not a section title; the curator decides those."},
				"content":    {Type: "string", Description: "The finding itself, in markdown, written so it is useful months from now: the concrete values, paths, and commands, not a narration of how you found them."},
				"confidence": {Type: "string", Description: "\"verified\" (checked directly, more than once or from more than one angle), \"probable\" (consistent with what you saw, not separately confirmed), or \"single-observation\" (seen once). Be honest — a single observation cannot overwrite documented text, and claiming more than you checked is how a wrong value gets into a guide.", Enum: []string{"verified", "probable", "single-observation"}},
			},
			Required: []string{"topic", "content"},
		},
		Handler: func(args map[string]any) (string, error) {
			topic := strings.TrimSpace(strArg(args, "topic"))
			content := strings.TrimSpace(strArg(args, "content"))
			if content == "" {
				return "", fmt.Errorf("content is required")
			}
			if !AcceptsFindings("guide") {
				return "", fmt.Errorf("nothing on this deployment accepts findings for documentation")
			}
			id, err := SubmitFinding(pr.userID, "guide", DocFinding{
				Content:    content,
				Topic:      topic,
				Confidence: strArg(args, "confidence"),
				Origin: DocFindingOrigin{
					SourceKind: "system",
					ItemID:     pr.appliance.ID,
					ItemLabel:  applianceLabel(pr.appliance.Name, pr.appliance.ID),
					RunID:      pr.id,
					Observed:   time.Now().Format(time.RFC3339),
				},
			})
			if err != nil {
				return "", fmt.Errorf("could not record that finding: %w", err)
			}
			return fmt.Sprintf("Recorded (%s). The Guide Curator will decide where it belongs; you do not need to file it anywhere.", id), nil
		},
		NeedsConfirm: false,
	}

	pr.push_to_guide_tool = AgentToolDef{
		Tool: Tool{
			Name:        "push_to_guide",
			Description: "Add a finding from this investigation to one of the user's GUIDES (living documents in the gohort Guides app) as a new section. Local save action — do NOT run anything on the appliance or look for Guides on the remote system. Use when the user asks to add/document something you looked up into a guide (\"add the cron jobs to my Ops guide\"). If a guide with the given name exists it's appended to; otherwise a new guide by that name is created. Call list_guides first if unsure of the exact name.",
			Parameters: map[string]ToolParam{
				"guide":         {Type: "string", Description: "The target guide's name (e.g. 'Ops', 'DB Runbook'). If none matches an existing guide, a new guide with this name is created."},
				"section_title": {Type: "string", Description: "Title for the new section (e.g. 'Cron jobs', 'Disk layout')."},
				"content":       {Type: "string", Description: "The section body in markdown — the finding, written up cleanly. No top-level heading; the title is separate."},
			},
			Required: []string{"guide", "section_title", "content"},
		},
		Handler: func(args map[string]any) (string, error) {
			guide, _ := args["guide"].(string)
			title, _ := args["section_title"].(string)
			content, _ := args["content"].(string)
			guide = strings.TrimSpace(guide)
			content = strings.TrimSpace(content)
			if content == "" {
				return "", fmt.Errorf("content is required")
			}
			// Resolve the guide by name; create a new one when nothing matches.
			docID, newTitle := "", ""
			if guide != "" {
				for _, d := range ListDocuments(pr.userID, "guide") {
					if strings.EqualFold(strings.TrimSpace(d.Title), guide) {
						docID = d.ID
						break
					}
				}
				if docID == "" {
					newTitle = guide
				}
			}
			writtenID, err := AppendToDocument(pr.ctx, pr.userID, "guide", docID, newTitle, title, content)
			if err != nil {
				return "", fmt.Errorf("push to guide failed: %w", err)
			}
			// Link the session to what it wrote, on this push and every one
			// after. The returned id is the whole reason this is possible: it
			// was being discarded, so a guide created by an investigation was
			// findable only by going and looking for its name.
			name := guide
			if newTitle != "" {
				name = newTitle
			}
			linkSessionGuide(pr.udb, pr.id, writtenID, name, newTitle != "")
			// Said in the session as it happens. The link is durable and the
			// session list will show it later, but "I just wrote that up" is
			// something the person watching should see now rather than discover
			// by going to look.
			verb := "Added to"
			if newTitle != "" {
				verb = "Created"
			}
			emit(pr.id, probeEvent{Kind: "status", Text: verb + " guide: " + name})
			if newTitle != "" {
				return fmt.Sprintf("Created guide %q and added the %q section.", newTitle, strings.TrimSpace(title)), nil
			}
			return fmt.Sprintf("Added the %q section to the %q guide.", strings.TrimSpace(title), guide), nil
		},
		NeedsConfirm: false,
	}
}

func (pr *probeRun) assembleToolkit() {
	// ptyLocal reports whether this run holds an ssh.Client of its own, which is
	// what run_pty needs and what a peer-reached appliance does not have — its
	// session lives on the far side and only whole commands cross the wire.
	// Read off PeerName rather than off a.conn, because the toolkit is built
	// before any reconnect could repopulate it.
	pr.ptyLocal = strings.TrimSpace(pr.appliance.PeerName) == ""

	if pr.appliance.Type == "repo" {
		// Repo workers search/read the encrypted code store instead of
		// executing commands; the recording/plan/map tools are shared and
		// scope-based, so they carry over unchanged.
		pr.workerTools = append(repoCodeTools(pr.ownerUser, pr.appliance.ID),
			pr.note_lesson_tool, pr.record_technique_tool, pr.record_discovery_tool, pr.store_fact_tool, pr.link_entities_tool, pr.store_rule_tool, pr.search_facts_tool,
			pr.save_to_codewriter_tool, pr.save_to_techwriter_tool, pr.record_finding_tool, pr.push_to_guide_tool, pr.list_guides_tool,
		)
	} else if pr.appliance.Type == "toolset" {
		// The bound tools ARE the target. Resolved in the owner's context, with
		// every binding's fingerprint checked; anything that changed since it
		// was approved is withheld and named rather than quietly handed over.
		// The acting user as well as the owner: which identity the bound tools
		// run under is the appliance's own setting, and the two are the same
		// user on an unshared appliance.
		pr.resolvedTools = resolveToolset(pr.ctx, pr.ownerUser, pr.userID, pr.appliance)
		pr.workerTools = append(pr.resolvedTools.Defs,
			pr.note_lesson_tool, pr.record_technique_tool, pr.record_discovery_tool, pr.store_fact_tool, pr.link_entities_tool, pr.store_rule_tool, pr.search_facts_tool,
			pr.save_to_codewriter_tool, pr.save_to_techwriter_tool, pr.record_finding_tool, pr.push_to_guide_tool, pr.list_guides_tool,
		)
		for _, w := range pr.resolvedTools.Withheld {
			// Surfaced, not logged. An investigation that quietly got quieter
			// is indistinguishable from a target with less to say.
			emit(pr.id, probeEvent{Kind: "status", Text: "Tool withheld: " + w})
		}
	} else if pr.appliance.Type == "bundle" {
		// Bundle workers read the encrypted evidence store. Nothing executes:
		// there is no host here, only files somebody uploaded.
		pr.workerTools = append(BundleTools(pr.ctx, pr.ownerUser, pr.appliance.ID),
			pr.note_lesson_tool, pr.record_technique_tool, pr.record_discovery_tool, pr.store_fact_tool, pr.link_entities_tool, pr.store_rule_tool, pr.search_facts_tool,
			pr.save_to_codewriter_tool, pr.save_to_techwriter_tool, pr.record_finding_tool, pr.push_to_guide_tool, pr.list_guides_tool,
		)
	} else if pr.appliance.Type == "command" {
		pr.workerTools = []AgentToolDef{
			pr.newRunTool(), pr.read_log_tool, pr.search_logs_tool,
			pr.note_lesson_tool, pr.record_technique_tool, pr.record_discovery_tool, pr.store_fact_tool, pr.link_entities_tool, pr.store_rule_tool, pr.search_facts_tool,
			pr.count_lines_tool, pr.read_range_tool, pr.save_to_codewriter_tool, pr.save_to_techwriter_tool, pr.record_finding_tool, pr.push_to_guide_tool, pr.list_guides_tool,
		}
	} else {
		pr.workerTools = []AgentToolDef{
			pr.newRunTool(), pr.read_log_tool, pr.search_logs_tool,
			pr.note_lesson_tool, pr.record_technique_tool, pr.record_discovery_tool, pr.store_fact_tool, pr.link_entities_tool, pr.store_rule_tool, pr.search_facts_tool,
			pr.count_lines_tool, pr.read_range_tool,
			pr.watch_condition_tool, pr.list_watches_tool, pr.save_to_codewriter_tool, pr.save_to_techwriter_tool, pr.record_finding_tool, pr.push_to_guide_tool, pr.list_guides_tool,
		}
		// run_pty is the one tool that needs the ssh.Client itself rather than
		// an exec function, so it is the one tool the peer transport cannot
		// carry: the far side runs whole commands and returns their output,
		// with no channel for an interactive PTY. Withheld rather than offered
		// and failed, because a tool that is present and always errors spends
		// rounds and pushes the worker into improvising around it instead of
		// reaching for run_command, which works here.
		if pr.ptyLocal {
			pr.workerTools = append(pr.workerTools, pr.newRunPtyTool())
		}
	}
	// The accumulated map, traversable. Added for EVERY appliance type: the
	// graph is per-appliance and type-agnostic, and the questions it answers
	// ("what does this rely on", "how does this reach that") are the same
	// whether the thing is a service, a package or a log file.
	pr.workerTools = append(pr.workerTools, mapTools(pr.appliance.ID)...)
	// Curated linked knowledge (owner-attached collections) is searchable by the
	// worker via search_knowledge — added for every appliance type, but only when
	// the appliance actually has collections linked, so agents without any don't
	// see a dead tool.
	if len(pr.appliance.Collections) > 0 {
		pr.workerTools = append(pr.workerTools, pr.search_knowledge_tool)
	}
	// Linked repos — the 360 join. A system that declares which code it runs
	// hands its investigation that repo's search/read tools, so one probe can
	// trace a log excerpt to the emitting line WHILE inspecting the live
	// state, instead of the human joining two investigations by hand. Repos
	// skip this (they ARE the code); workspaces never reach here.
	if pr.appliance.Type != "repo" && len(pr.appliance.LinkedRepos) > 0 {
		var linked []linkedRepo
		for _, rid := range pr.appliance.LinkedRepos {
			if ra, raOwner, _, ok := pr.T.resolveAppliance(pr.userID, pr.udb, rid); ok && ra.Type == "repo" {
				linked = append(linked, linkedRepo{Owner: raOwner, ID: ra.ID, Name: applianceLabel(ra.Name, ra.ID)})
			}
		}
		if len(linked) > 0 {
			pr.workerTools = append(pr.workerTools, linkedRepoTools(linked)...)
			names := make([]string, 0, len(linked))
			for _, lr := range linked {
				names = append(names, lr.Name)
			}
			emit(pr.id, probeEvent{Kind: "status", Text: "Code linked: " + strings.Join(names, ", ")})
		}
	}
	// Enforced sanity check — servitor handles sensitive system data and
	// must never call out to third-party services. assertOnlyAllowedTools
	// panics if anything outside the local-only allow-list sneaks in.
	assertAllowedWithBindings("servitor.worker", pr.workerTools, servitorWorkerToolAllowList, toolsetBindingNames(pr.appliance))
}

const probeTopicLimit = 3

const probeLoopSignalLimit = 3

// One investigator pass = one round budget. Extracted so the
// continuation loop below can re-run it verbatim.
const (
	investigatorRoundBudget = 75 // rounds per investigator pass
	maxInvestigatorPasses   = 2  // extra budgets granted while steps keep resolving
)

type mapState struct {
	// === New investigator-driven mapping ===
	//
	// Phase 1: Quick snapshot (no LLM) — gives the investigator a starting point.
	snapshot                   string
	probeCache                 map[string]string
	probeTopicCount            map[string]int
	mapPlan                    planToolSet
	plan                       *WorkPlan
	set_plan_tool              AgentToolDef
	mark_step_in_progress_tool AgentToolDef
	record_step_findings_tool  AgentToolDef
	mark_step_blocked_tool     AgentToolDef
	revise_plan_tool           AgentToolDef
	report_gaps_tool           AgentToolDef
	probeLoopSignalCount       int
	probe_tool                 AgentToolDef
	invMsg                     strings.Builder
	invResp                    *Response
	invHistory                 []Message
	invErr                     error
	investigatorTools          []AgentToolDef
	lastInProgressStep         int
	stuckTrackedStep           int
	stuckRoundCount            int
	softNudgeFired             bool
	firmNudgeFired             bool
	invCfg                     AgentLoopConfig
	prevPending                int
	now                        time.Time
	finalFacts                 string
	finalNotes                 string
	finalTechniques            string
	finalDiscoveries           string
	invNarrative               string
	synthMsg                   string
	synthResp                  *Response
	synthErr                   error
}

// mapMode is a Map run: snapshot the target, arm the probe tool, brief the
// investigator, run it against the plan, then synthesize the profile. Each
// stage is a method on the run's mapState; the first that ends the run says so.
func (pr *probeRun) mapMode() probeAction {
	for _, stage := range []func() probeAction{
		pr.mapSnapshot,
		pr.mapProbeTool,
		pr.mapBrief,
		pr.mapInvestigate,
		pr.mapSynthesize,
	} {
		if act := stage(); act != actNone {
			return act
		}
	}
	return actNone
}

func (pr *probeRun) mapSnapshot() probeAction {
	if pr.appliance.Type == "repo" {
		emit(pr.id, probeEvent{Kind: "status", Text: "Reading repository layout…"})
		pr.m.snapshot = runRepoSnapshot(pr.ownerUser, pr.appliance.ID)
	} else if pr.appliance.Type == "bundle" {
		emit(pr.id, probeEvent{Kind: "status", Text: "Reading the bundle index…"})
		pr.m.snapshot = runBundleSnapshot(pr.ownerUser, pr.appliance.ID)
	} else if pr.appliance.Type == "toolset" {
		// One owner-nominated tool, or nothing. See runToolsetSnapshot.
		if pr.resolvedTools.Snapshot != "" {
			emit(pr.id, probeEvent{Kind: "status", Text: "Orienting via " + pr.resolvedTools.Snapshot + "…"})
		}
		pr.m.snapshot = runToolsetSnapshot(pr.resolvedTools)
	} else {
		emit(pr.id, probeEvent{Kind: "status", Text: "Taking system snapshot…"})
		pr.m.snapshot = runQuickSnapshot(pr.ctx, pr.sshExec)
	}
	if pr.ctx.Err() != nil {
		return actReturn
	}
	if pr.m.snapshot != "" {
		emit(pr.id, probeEvent{Kind: "output", Text: "## System Snapshot\n\n" + pr.m.snapshot})
	}
	return actNone
}

func (pr *probeRun) mapProbeTool() probeAction {
	// Phase 2: Investigator loop — the investigator decides what to probe,
	// follows leads, and records discoveries. Workers execute specific tasks.
	//
	// probeCache: results keyed by aggressively-normalized task (sorted
	// content tokens, stop words dropped) so paraphrased re-delegations
	// hit the cache. "list mysql tables" and "show tables in mysql"
	// normalize to the same key.
	//
	// probeTopicCount: tracks how many times the orchestrator has
	// delegated tasks sharing a content-token set with prior tasks.
	// When the same topic gets tried 3+ ways, escalate the response
	// from "[ALREADY PROBED]" to "[ENOUGH — pivot to a different
	// topic entirely]" so the orchestrator stops grinding the same
	// area through paraphrase.
	pr.m.probeCache = make(map[string]string)
	pr.m.probeTopicCount = make(map[string]int)
	// Plan tools (buildPlanTools) — Map REQUIRES the plan: mapping a system
	// is the plan. Chat builds the same group with required=false.
	pr.m.mapPlan = buildPlanTools(pr.id, true)
	pr.m.plan = pr.m.mapPlan.Plan
	pr.m.set_plan_tool = pr.m.mapPlan.Set
	pr.m.mark_step_in_progress_tool = pr.m.mapPlan.Start
	pr.m.record_step_findings_tool = pr.m.mapPlan.Findings
	pr.m.mark_step_blocked_tool = pr.m.mapPlan.Blocked
	pr.m.revise_plan_tool = pr.m.mapPlan.Revise
	pr.m.report_gaps_tool = pr.m.mapPlan.Gaps

	// probeLoopSignalCount tracks how many delegations have ended
	// with a worker [LOOP DETECTED] message. The orchestrator is
	// supposed to read these messages and pivot, but in practice
	// it often ignores them and re-delegates with paraphrased
	// task descriptions. Counting at the orchestrator's tool-call
	// boundary lets us refuse the (N+1)th delegation outright
	// once the orchestrator has demonstrated it's not reading the
	// signal — forcing model attention via tool-call refusal
	// instead of relying on prompt-level guidance.
	pr.m.probeLoopSignalCount = 0
	pr.m.probe_tool = AgentToolDef{
		Tool: Tool{
			Name: "probe",
			Description: "Execute a specific SSH investigation task on the target system. " +
				"Be precise: 'show /etc/nginx/sites-enabled/myapp.conf and identify its upstream' not 'investigate nginx'. " +
				"Pass rich context so the worker uses what you already know without re-discovering it.",
			Parameters: map[string]ToolParam{
				"task": {
					Type:        "string",
					Description: "Single clear goal: find X, read Y, verify Z. One objective per probe.",
				},
				"context": {
					Type:        "string",
					Description: "What you know so far that's relevant: paths, ports, credentials, service names.",
				},
			},
			Required: []string{"task"},
		},
		Handler: func(args map[string]any) (string, error) {
			task, _ := args["task"].(string)
			if task == "" {
				return "", fmt.Errorf("task is required")
			}
			// Hard refusal: orchestrator has accumulated too many
			// worker [LOOP DETECTED] signals across prior delegations
			// without effectively pivoting. Refuse the new delegation
			// outright before spawning a worker — forces model
			// attention via tool-call rejection rather than relying
			// on the orchestrator to read [LOOP DETECTED] strings
			// it's been ignoring.
			if pr.m.probeLoopSignalCount >= probeLoopSignalLimit {
				emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Probe refused: orchestrator hit loop-signal limit (%d signals)", pr.m.probeLoopSignalCount)})
				return fmt.Sprintf("[DELEGATION REFUSED — your prior %d delegations have triggered worker LOOP DETECTED responses. Your current investigation strategy is not converging. STOP delegating new probes. Write your final report based on what you have already learned. Acknowledge what you could not determine and why. Do not call probe again in this session.]", pr.m.probeLoopSignalCount), nil
			}
			cacheKey := normalizeTask(task)
			if cached, ok := pr.m.probeCache[cacheKey]; ok {
				pr.m.probeTopicCount[cacheKey]++
				if pr.m.probeTopicCount[cacheKey] >= probeTopicLimit {
					emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Topic exhausted: %q (%dx)", task, pr.m.probeTopicCount[cacheKey])})
					return fmt.Sprintf("[TOPIC EXHAUSTED — you have re-delegated this topic %d times now. Stop probing this area entirely. Pivot to a fundamentally different domain (different service, different layer, different angle on the original goal). Re-delegating the same topic in different words will not produce new information.]\n\nLast result for reference:\n\n%s", pr.m.probeTopicCount[cacheKey], cached), nil
				}
				return "[ALREADY PROBED — result below. Do not probe this topic again; move to a different area.]\n\n" + cached, nil
			}
			context, _ := args["context"].(string)
			var msg strings.Builder
			if context != "" {
				msg.WriteString("## Known Context\n\n")
				msg.WriteString(context)
				msg.WriteString("\n\n")
			}
			if pr.udb != nil {
				if t := techniquesFor(pr.udb, pr.appliance.ID); t != "" {
					msg.WriteString("## Known Techniques (use directly)\n\n")
					msg.WriteString(t)
					msg.WriteString("\n\n")
				}
				if facts := factsForAppliance(pr.udb, pr.appliance.ID); len(facts) > 0 {
					msg.WriteString("## Stored Facts\n\n")
					msg.WriteString(formatFacts(facts))
					msg.WriteString("\n\n")
				}
			}
			msg.WriteString("## Task\n\n")
			msg.WriteString(task)
			short := task
			if len(short) > 80 {
				short = short[:80] + "…"
			}
			emit(pr.id, probeEvent{Kind: "intent", Text: task, Reason: context})
			var workerResp *Response
			var workerErr error
			withHeartbeat(pr.ctx, pr.id, "Probe: "+short, func() {
				workerResp, _, workerErr = pr.a.RunAgentLoop(pr.ctx,
					[]Message{{Role: "user", Content: msg.String()}},
					AgentLoopConfig{
						// mapping=true: this is the reconnaissance pass, so the
						// worker persists through failures rather than handing
						// the first dead end back to the investigator.
						SystemPrompt:    buildProbeWorkerPrompt(pr.appliance, pr.scratch, true, pr.resolvedTools),
						Tools:           pr.withFreshRunTool(pr.workerTools),
						MaxRounds:       12,
						RouteKey:        "app.servitor",
						TierOverride:    applianceTierOverride(pr.appliance.WorkerTier),
						MaskDebugOutput: true,
						ChatOptions:     []ChatOption{WithTemperature(0.2), WithThink(false)},
						SerialTools:     true,
					},
				)
			})
			if workerErr != nil {
				return "", workerErr
			}
			if workerResp == nil {
				return "No findings.", nil
			}
			result := strings.TrimSpace(workerResp.Content)
			result = parseProbeOutcome(result)
			pr.m.probeCache[cacheKey] = result
			// If the worker hit the cmd loop limit during this
			// delegation, propagate the signal up to the
			// orchestrator-level counter so we can refuse future
			// delegations once the orchestrator has demonstrated
			// it's not pivoting in response.
			if strings.Contains(result, "[LOOP DETECTED]") {
				pr.m.probeLoopSignalCount++
				emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Worker loop signal received (%d/%d)", pr.m.probeLoopSignalCount, probeLoopSignalLimit)})
			}
			if len(result) > 12000 {
				result = result[:12000] + "\n… [truncated]"
			}
			emit(pr.id, probeEvent{Kind: "output", Text: result})
			return result, nil
		},
		NeedsConfirm: false,
	}
	return actNone
}

func (pr *probeRun) mapBrief() probeAction {
	// Re-mapping banner — when the appliance already had a
	// profile coming into this run, the prior facts /
	// discoveries / techniques below can mislead the
	// investigator into skipping the plan ("we already know
	// this system"). Spell out that this is a fresh re-derivation
	// before listing the prior context so the LLM doesn't read
	// the prior data as "work is done."
	if pr.saveProfile && strings.TrimSpace(pr.appliance.Profile) != "" {
		pr.m.invMsg.WriteString("## RE-MAPPING (full re-derivation)\n\n")
		pr.m.invMsg.WriteString("This system was mapped before — prior facts, discoveries, and techniques are listed below FOR YOUR REFERENCE ONLY. They are NOT a substitute for a fresh investigation. Re-verify what's still true, discover what's changed, and produce a complete new profile.\n\n")
		pr.m.invMsg.WriteString("You MUST emit a fresh `set_plan` as your first tool call. The previous plan is gone; treat this run as a clean slate that benefits from prior context, not as a continuation.\n\n")
	}
	pr.m.invMsg.WriteString("## System Snapshot\n\n")
	if pr.m.snapshot != "" {
		pr.m.invMsg.WriteString(pr.m.snapshot)
	} else {
		pr.m.invMsg.WriteString("(snapshot unavailable)\n")
	}
	pr.m.invMsg.WriteString("\n\n")
	if pr.udb != nil {
		if disc := discoveriesFor(pr.udb, pr.appliance.ID); len(disc) > 0 {
			pr.m.invMsg.WriteString("## Prior Discoveries (already established)\n\n")
			pr.m.invMsg.WriteString(formatDiscoveries(disc))
			pr.m.invMsg.WriteString("\n\n")
		}
		if pr.cachedFacts != "" {
			pr.m.invMsg.WriteString("## Prior Facts\n\n")
			pr.m.invMsg.WriteString(pr.cachedFacts)
			pr.m.invMsg.WriteString("\n\n")
		}
		if gb := scopedGraphPromptBlock(pr.appliance); gb != "" {
			pr.m.invMsg.WriteString("## System Map so far (extend it — don't re-map what's here)\n\n")
			pr.m.invMsg.WriteString(gb)
			pr.m.invMsg.WriteString("\n\n")
		}
		if pr.cachedTechniques != "" {
			pr.m.invMsg.WriteString("## Prior Techniques\n\n")
			pr.m.invMsg.WriteString(pr.cachedTechniques)
			pr.m.invMsg.WriteString("\n\n")
		}
	}
	pr.m.invMsg.WriteString("Begin your investigation.\n\n")
	pr.m.invMsg.WriteString("REQUIRED FIRST CALL: `set_plan` with ordered steps — typically 5–12, scale higher (15+) for complex appliances. Err toward more steps with narrower scopes rather than fewer with sprawling scopes; narrow steps produce sharper findings. Each step needs a short title and a what_to_find description. Foundation/discovery steps come first; deeper investigation later builds on what they find.\n\n")
	pr.m.invMsg.WriteString("After the plan is set, work the steps one at a time:\n")
	pr.m.invMsg.WriteString("  1. mark_step_in_progress (step_id)\n")
	pr.m.invMsg.WriteString("  2. probe (delegate worker investigation for that step — may call multiple times)\n")
	pr.m.invMsg.WriteString("  3. record_step_findings (step_id, 1–3 sentence summary) — OR mark_step_blocked (step_id, reason) if you can't complete it\n")
	pr.m.invMsg.WriteString("  4. Move to the next pending step\n\n")
	pr.m.invMsg.WriteString(fmt.Sprintf("If findings reveal something you couldn't have planned for, call `revise_plan` to add/remove/reorder steps (max %d revisions per session — use deliberately, not reflexively).\n\n", WorkPlanRevisionLimit))
	pr.m.invMsg.WriteString("BEFORE WRITING YOUR FINAL ANSWER: call `report_gaps`. It returns a structured summary of every blocked or skipped step. You MUST incorporate that into a 'What I Couldn't Determine' section in your final answer — the user trusts the report only when you're explicit about what you couldn't see. If the gap report is empty (everything completed), no such section is needed.\n\n")
	pr.m.invMsg.WriteString("Use store_fact / record_discovery / record_technique alongside step work for durable knowledge that survives the session. When all steps are done or blocked AND report_gaps has been called, write your final answer.")
	return actNone
}

func (pr *probeRun) stepResetCb() bool {
	cur := 0
	for _, s := range pr.m.plan.Snapshot() {
		if s.Status == WorkStepInProgress {
			cur = s.ID
			break
		}
	}
	if cur == 0 || cur == pr.m.lastInProgressStep {
		return false
	}
	pr.m.lastInProgressStep = cur
	return true
}

// PendingWorkFn lets the agent-loop's wrap-up nudge know
// when there are still authorized plan steps queued, so it
// reframes "stop exploring" as "finish the current step
// and continue down the list." Without this, the worker
// reads the default wrap-up as license to skip remaining
// steps and write a summary — observed dropping ~5 steps
// from longer plans.
func (pr *probeRun) pendingPlanWork() int {
	n := 0
	for _, s := range pr.m.plan.Snapshot() {
		if s.Status == WorkStepPending || s.Status == WorkStepInProgress {
			n++
		}
	}
	return n
}

func (pr *probeRun) stuckMsgFn() []Message {
	curStep := 0
	stepTitle := ""
	for _, s := range pr.m.plan.Snapshot() {
		if s.Status == WorkStepInProgress {
			curStep = s.ID
			stepTitle = s.Title
			break
		}
	}
	if curStep == 0 {
		// No step in progress (pre-plan, between steps, or
		// final wrap-up). Don't count and don't nudge.
		return nil
	}
	if curStep != pr.m.stuckTrackedStep {
		pr.m.stuckTrackedStep = curStep
		pr.m.stuckRoundCount = 0
		pr.m.softNudgeFired = false
		pr.m.firmNudgeFired = false
	}
	pr.m.stuckRoundCount++
	if pr.m.stuckRoundCount == 12 && !pr.m.softNudgeFired {
		pr.m.softNudgeFired = true
		return []Message{{Role: "user", Content: fmt.Sprintf(
			"Pacing check: you've spent 12 rounds on step %d (%q) without advancing. Move to another pending step now — call mark_step_in_progress on it and work it; leave this step unfinished (do NOT mark it blocked) and revisit it later with what you learn elsewhere. Coming back fresh is faster than grinding. Don't burn more than 8 more rounds here before switching.",
			curStep, stepTitle)}}
	}
	if pr.m.stuckRoundCount == 20 && !pr.m.firmNudgeFired {
		pr.m.firmNudgeFired = true
		return []Message{{Role: "user", Content: fmt.Sprintf(
			"Hard pacing limit: you've spent 20 rounds on step %d (%q). Switch to another pending step NOW — call mark_step_in_progress on the next one and work it. Leave step %d unfinished and pending; do NOT mark it blocked just because it's slow (blocking it for pacing/time is invalid — you'll get more rounds to revisit it). Only block a step for a genuine dead-end (no access, missing tool, unreachable).",
			curStep, stepTitle, curStep)}}
	}
	return nil
}

func (pr *probeRun) mapInvestigate() probeAction {
	emit(pr.id, probeEvent{Kind: "status", Text: "Investigator starting…"})
	pr.m.investigatorTools = []AgentToolDef{
		pr.m.set_plan_tool, pr.m.mark_step_in_progress_tool, pr.m.record_step_findings_tool, pr.m.mark_step_blocked_tool,
		pr.m.revise_plan_tool, pr.m.report_gaps_tool,
		pr.m.probe_tool, pr.store_fact_tool, pr.link_entities_tool, pr.record_discovery_tool, pr.record_technique_tool, pr.note_lesson_tool,
	}
	assertOnlyAllowedTools("servitor.investigator", pr.m.investigatorTools, servitorOrchestratorToolAllowList)
	// Per-step pacing reset — the soft-pacing windows
	// (midpoint nudge, wrap-up warning, failure streak)
	// rebase whenever the in_progress step ID changes. Stops
	// the "you're near the wrap-up cap" message from firing
	// at the wrong moment when the LLM is just starting a
	// fresh step. The closure tracks the last step we
	// announced a reset for; returns true once per real
	// transition.
	pr.m.lastInProgressStep = 0
	// Per-step stuck detector — when the investigator burns too
	// many rounds on a single step without advancing, inject a
	// nudge urging it to mark the step blocked and move on. The
	// 75-round budget is fleet-wide; without this guard the LLM
	// can spend 40+ rounds wrestling with one bad path while
	// every other plan step goes untouched, then hit the
	// wrap-up nudge with most of the plan still pending.
	//
	// Thresholds:
	//   - Soft nudge at 12 rounds on one step: "move to another step, leave this pending"
	//   - Firm nudge at 20 rounds: "switch steps NOW; don't block for pacing"
	// The nudges push DEFER-and-revisit, not mark_step_blocked —
	// blocking zeroes the pending count and defeats the continuation
	// that grants unfinished plans more rounds. A slow step stays
	// pending/in-progress and gets revisited with more budget.
	//
	// The nudges are one-shot per step transition — when the
	// LLM advances to a new step, the counter resets and the
	// flags clear so subsequent steps get the same grace period.
	pr.m.stuckTrackedStep = 0
	pr.m.stuckRoundCount = 0
	pr.m.softNudgeFired = false
	pr.m.firmNudgeFired = false
	pr.m.invCfg = AgentLoopConfig{
		SystemPrompt: buildInvestigatorSystemPrompt(pr.appliance, pr.resolvedTools),
		Tools:        pr.m.investigatorTools,
		MaxRounds:    investigatorRoundBudget,
		// The investigator's OWN stage, not the worker one. It borrowed
		// app.servitor's tier while taking its thinking budget from
		// app.servitor.orchestrator (see orchestratorThinkOpts), which
		// left the "Servitor: Orchestrator" row in Admin → LLM Routing
		// offering a tier selector that decided nothing: the budget
		// applied and the tier was silently ignored. Default is
		// "worker (thinking)", so this changes no behavior until an
		// operator picks something else — which is now possible.
		RouteKey:        "app.servitor.orchestrator",
		TierOverride:    applianceTierOverride(pr.appliance.OrchestratorTier),
		MaskDebugOutput: true,
		SerialTools:     true,
		ChatOptions:     append([]ChatOption{WithTemperature(0.3), WithThink(true)}, orchestratorThinkOpts()...),
		OnRoundReset:    pr.stepResetCb,
		OnRoundStart:    pr.stuckMsgFn,
		PendingWorkFn:   pr.pendingPlanWork,
		// Analyzing a repo that may define LLM tools: the investigator
		// legitimately names tools like store_fact when describing the
		// code, so don't nudge it as if it meant to call them.
		DisableToolMentionCorrection: pr.appliance.Type == "repo",
	}
	withHeartbeat(pr.ctx, pr.id, "Investigator", func() {
		pr.m.invResp, pr.m.invHistory, pr.m.invErr = pr.a.RunAgentLoop(pr.ctx,
			[]Message{{Role: "user", Content: pr.m.invMsg.String()}}, pr.m.invCfg)
	})
	// Productive continuation — a single round budget often isn't
	// enough to work a 10–15 step plan to completion, so the
	// investigator kept "running out of rounds" and synthesizing a
	// half-finished profile. When a pass exhausts its budget
	// (HitRoundCap) with steps still PENDING, grant another budget —
	// but only while it keeps resolving steps. A pass that clears
	// nothing means it's genuinely stuck (every remaining step
	// dead-ended), so stop and synthesize what we have rather than
	// grinding in circles. Total work is bounded at
	// (1 + maxInvestigatorPasses) budgets.
	pr.m.prevPending = -1
	for pass := 0; pass < maxInvestigatorPasses && pr.m.invErr == nil && pr.ctx.Err() == nil; pass++ {
		if pr.m.invResp == nil || !pr.m.invResp.HitRoundCap {
			break // natural finish — not a cap hit
		}
		pending := pr.pendingPlanWork()
		if pending == 0 {
			break // capped, but the plan is fully resolved — nothing left
		}
		if pr.m.prevPending >= 0 && pending >= pr.m.prevPending {
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf(
				"Investigator stalled with %d step(s) still pending — wrapping up with findings so far.", pending)})
			break
		}
		pr.m.prevPending = pending
		emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf(
			"Investigator reached its round budget with %d step(s) pending — continuing the investigation…", pending)})
		// Fresh stuck-detector window for the new budget.
		pr.m.stuckTrackedStep, pr.m.stuckRoundCount, pr.m.softNudgeFired, pr.m.firmNudgeFired = 0, 0, false, false
		withHeartbeat(pr.ctx, pr.id, "Investigator (continued)", func() {
			pr.m.invResp, pr.m.invHistory, pr.m.invErr = pr.a.RunAgentLoop(pr.ctx, pr.m.invHistory, pr.m.invCfg)
		})
	}
	if pr.m.invErr != nil && pr.ctx.Err() == nil {
		emit(pr.id, probeEvent{Kind: "error", Text: "Investigator error: " + pr.m.invErr.Error()})
		// Don't return — synthesize what was gathered.
	}
	if pr.ctx.Err() != nil {
		return actReturn
	}
	return actNone
}

func (pr *probeRun) mapSynthesize() probeAction {
	// Phase 3: Synthesis — structured profile from accumulated discoveries + facts.
	emit(pr.id, probeEvent{Kind: "status", Text: "Synthesizing profile from investigation findings…"})
	pr.m.now = time.Now()
	pr.m.finalFacts = ""
	if facts := factsForAppliance(pr.udb, pr.appliance.ID); len(facts) > 0 {
		pr.m.finalFacts = formatFactsWithAge(facts, pr.m.now)
	}
	if pr.udb != nil {
		pr.udb.Get(notesTable, pr.appliance.ID, &pr.m.finalNotes)
	}
	pr.m.finalTechniques = ""
	if pr.udb != nil {
		pr.m.finalTechniques = techniquesFor(pr.udb, pr.appliance.ID)
	}
	pr.m.finalDiscoveries = ""
	if pr.udb != nil {
		pr.m.finalDiscoveries = formatDiscoveries(discoveriesFor(pr.udb, pr.appliance.ID))
	}
	pr.m.invNarrative = ""
	if pr.m.invResp != nil {
		pr.m.invNarrative = strings.TrimSpace(pr.m.invResp.Content)
	}
	pr.m.synthMsg = buildSynthesisMessage(pr.m.invNarrative, pr.m.finalFacts, pr.m.finalTechniques, pr.m.finalNotes, pr.m.finalDiscoveries)
	withHeartbeat(pr.ctx, pr.id, "Synthesizing profile", func() {
		pr.m.synthResp, _, pr.m.synthErr = pr.a.RunAgentLoop(pr.ctx,
			[]Message{{Role: "user", Content: pr.m.synthMsg}},
			AgentLoopConfig{
				SystemPrompt:    buildSynthesisSystemPrompt(pr.appliance),
				Tools:           nil,
				MaxRounds:       1,
				RouteKey:        "app.servitor",
				TierOverride:    applianceTierOverride(pr.appliance.OrchestratorTier),
				MaskDebugOutput: true,
				ChatOptions:     []ChatOption{WithThink(false)},
			},
		)
	})
	if pr.m.synthErr != nil && pr.ctx.Err() == nil {
		emit(pr.id, probeEvent{Kind: "error", Text: "Synthesis error: " + pr.m.synthErr.Error()})
	}
	if pr.m.synthResp != nil && strings.TrimSpace(pr.m.synthResp.Content) != "" {
		pr.reply = strings.TrimSpace(pr.m.synthResp.Content)
	}
	if pr.reply == "" && pr.m.invNarrative != "" {
		pr.reply = pr.m.invNarrative
	}
	return actNone
}

const qaTopicLimit = 3

const maxDocInvestigatorPasses = 2

type chatState struct {
	read_doc_tool        AgentToolDef
	update_doc_tool      AgentToolDef
	lastProbeResult      string
	allProbeResults      []string
	qaProbeCache         map[string]string
	qaTopicCount         map[string]int
	probe_tool           AgentToolDef
	docs                 map[string]string
	hasFreshImage        bool
	leadPrompt           string
	injQ                 *InjectionQueue
	err                  error
	chatPlan             planToolSet
	docInvestigatorTools []AgentToolDef
	orch                 *orchestrate.OrchestrateApp
	leadImages           [][]byte
	leadScope            orchestrate.AgentScope
	leadLoop             *orchestrate.AgentLoopOverrides
	res                  orchestrate.AgentSyncResult
}

// chatMode answers a question about the appliance: the document tools, the
// probe tool, the investigator, then the follow-through that stores what the
// probes found. Each stage is a method on the run's chatState; the first that
// ends the run says so.
func (pr *probeRun) chatMode() probeAction {
	for _, stage := range []func() probeAction{
		pr.chatDocTools,
		pr.chatProbeTool,
		pr.chatInvestigate,
		pr.chatAfter,
	} {
		if act := stage(); act != actNone {
			return act
		}
	}
	return actNone
}

func (pr *probeRun) chatDocTools() probeAction {
	// Chat mode: investigator loop using probe tool — no pre-planned task list.

	// read_doc — fetch a structured knowledge document.
	pr.c.read_doc_tool = AgentToolDef{
		Tool: Tool{
			Name:        "read_doc",
			Description: "Read a structured knowledge document about this system. System docs: overview, databases, filesystem, services, apps. CLI maps: cli:<command> (e.g. cli:kubectl, cli:docker).",
			Parameters: map[string]ToolParam{
				"doc": {Type: "string", Description: "Document name: overview, databases, filesystem, services, apps, or cli:<command>."},
			},
			Required: []string{"doc"},
		},
		Handler: func(args map[string]any) (string, error) {
			doc, _ := args["doc"].(string)
			if doc == "" {
				return "", fmt.Errorf("doc is required")
			}
			emit(pr.id, probeEvent{Kind: "status", Text: "Reading " + doc + " knowledge..."})
			content, age := readDocWithAge(pr.udb, pr.appliance.ID, doc, time.Now())
			if content == "" {
				if strings.HasPrefix(doc, "cli:") {
					return fmt.Sprintf("No CLI map found for %q. Ask the user to run 'Map App' for this command first.", strings.TrimPrefix(doc, "cli:")), nil
				}
				return fmt.Sprintf("No %s document found. Probe the system to build it.", doc), nil
			}
			// Repo docs go stale when the code is refreshed (re-cloned) after the
			// last Map run — the files update but the synthesized docs don't. Warn
			// so the investigator re-verifies against current files.
			staleNote := ""
			if pr.appliance.Type == "repo" && repoOverviewStale(pr.appliance) {
				staleNote = repoStaleDocBanner
			}
			if age != "" {
				return fmt.Sprintf("%s[Last updated: %s]\n\n%s", staleNote, age, content), nil
			}
			return staleNote + content, nil
		},
		NeedsConfirm: false,
	}

	// update_doc — persist a structured knowledge document.
	pr.c.update_doc_tool = AgentToolDef{
		Tool: Tool{
			Name:        "update_doc",
			Description: "Write or replace a structured knowledge document with new findings. Call this after every probe that yields new information. These docs are the investigator's persistent memory across sessions.",
			Parameters: map[string]ToolParam{
				"doc":     {Type: "string", Description: "Document name: overview, databases, filesystem, services, or apps."},
				"content": {Type: "string", Description: "Full markdown content for this document."},
			},
			Required: []string{"doc", "content"},
		},
		Handler: func(args map[string]any) (string, error) {
			doc, _ := args["doc"].(string)
			content, _ := args["content"].(string)
			if doc == "" || content == "" {
				return "", fmt.Errorf("doc and content are required")
			}
			writeDoc(pr.udb, pr.appliance.ID, doc, content)
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Knowledge updated: %s", doc)})
			return "saved", nil
		},
		NeedsConfirm: false,
	}
	return actNone
}

func (pr *probeRun) chatProbeTool() probeAction {
	pr.c.qaProbeCache = make(map[string]string) // normalized task → last result
	pr.c.qaTopicCount = make(map[string]int)
	// probe_tool — targeted investigation at the investigator's direction.
	pr.c.probe_tool = AgentToolDef{
		Tool: Tool{
			Name: "probe",
			Description: "Execute a specific SSH investigation task on the target system. " +
				"Be precise — one clear goal per probe. Pass rich context so the worker " +
				"uses what you already know.",
			Parameters: map[string]ToolParam{
				"task":    {Type: "string", Description: "Single clear goal: find X, read Y, verify Z."},
				"context": {Type: "string", Description: "Relevant context you already know: paths, ports, credentials."},
			},
			Required: []string{"task"},
		},
		Handler: func(args map[string]any) (string, error) {
			task, _ := args["task"].(string)
			if task == "" {
				return "", fmt.Errorf("task is required")
			}
			cacheKey := normalizeTask(task)
			if cached, ok := pr.c.qaProbeCache[cacheKey]; ok {
				pr.c.qaTopicCount[cacheKey]++
				if pr.c.qaTopicCount[cacheKey] >= qaTopicLimit {
					emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Topic exhausted: %q (%dx)", task, pr.c.qaTopicCount[cacheKey])})
					return fmt.Sprintf("[TOPIC EXHAUSTED — you have re-delegated this topic %d times now. Stop probing this area entirely. Pivot to a fundamentally different domain. Re-delegating the same topic in different words will not produce new information.]\n\nLast result for reference:\n\n%s", pr.c.qaTopicCount[cacheKey], cached), nil
				}
				return "[ALREADY PROBED — result below. Do not probe this topic again; move to a different area.]\n\n" + cached, nil
			}
			context, _ := args["context"].(string)
			var msg strings.Builder
			if context != "" {
				msg.WriteString("## Known Context\n\n")
				msg.WriteString(context)
				msg.WriteString("\n\n")
			}
			if pr.udb != nil {
				if disc := discoveriesFor(pr.udb, pr.appliance.ID); len(disc) > 0 {
					msg.WriteString("## Key Discoveries (pre-established — do not re-investigate)\n\n")
					msg.WriteString(formatDiscoveries(disc))
					msg.WriteString("\n\n")
				}
				if t := techniquesFor(pr.udb, pr.appliance.ID); t != "" {
					msg.WriteString("## Known Techniques (use directly)\n\n")
					msg.WriteString(t)
					msg.WriteString("\n\n")
				}
				if facts := factsForAppliance(pr.udb, pr.appliance.ID); len(facts) > 0 {
					msg.WriteString("## Stored Facts\n\n")
					msg.WriteString(formatFacts(facts))
					msg.WriteString("\n\n")
				}
			}
			msg.WriteString("## Task\n\n")
			msg.WriteString(task)
			// Surface the orchestrator's intent for this round as a
			// prominent event so the user can see "what is the brain
			// trying to do" without needing to read raw reasoning.
			// task is the orchestrator's user-facing summary; context
			// is the supporting briefing it passed along.
			emit(pr.id, probeEvent{Kind: "intent", Text: task, Reason: context})
			var workerResp *Response
			var err error
			withHeartbeat(pr.ctx, pr.id, "Worker: investigating", func() {
				workerResp, _, err = pr.a.RunAgentLoop(pr.ctx,
					[]Message{{Role: "user", Content: msg.String()}},
					AgentLoopConfig{
						// mapping=false: a chat probe stops at the first dead end and
						// lets the investigator pick the next angle.
						SystemPrompt:    buildProbeWorkerPrompt(pr.appliance, pr.scratch, false, pr.resolvedTools),
						Tools:           pr.withFreshRunTool(pr.workerTools),
						MaxRounds:       15,
						RouteKey:        "app.servitor",
						TierOverride:    applianceTierOverride(pr.appliance.WorkerTier),
						MaskDebugOutput: true,
						ChatOptions:     []ChatOption{WithTemperature(0.2), WithThink(false)},
						SerialTools:     true,
						// Repo workers read code that names their own tools; don't
						// misread a description as an intended call.
						DisableToolMentionCorrection: pr.appliance.Type == "repo",
					},
				)
			})
			if err != nil {
				return "", err
			}
			if workerResp == nil {
				return "Worker returned no findings.", nil
			}
			result := strings.TrimSpace(workerResp.Content)
			result = parseProbeOutcome(result)
			if result != "" {
				pr.c.qaProbeCache[cacheKey] = result
				pr.c.lastProbeResult = result
				pr.c.allProbeResults = append(pr.c.allProbeResults, result)
			}
			if len(result) > 14000 {
				result = result[:14000] + "\n… [truncated]"
			}
			emit(pr.id, probeEvent{Kind: "status", Text: "Worker complete — reviewing findings."})
			return result, nil
		},
		NeedsConfirm: false,
	}
	return actNone
}

func (pr *probeRun) drainInjections() []Message {
	if pr.c.injQ == nil {
		return nil
	}
	notes := pr.c.injQ.Drain()
	if len(notes) == 0 {
		return nil
	}
	out := make([]Message, 0, len(notes))
	ids := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, Message{Role: "user", Content: "[USER NOTE — submitted mid-investigation] " + n.Text})
		ids = append(ids, n.ID)
	}
	emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf("Orchestrator picked up %d user note(s).", len(notes))})
	// Tell the UI which notes are now locked from edit/delete.
	emit(pr.id, probeEvent{Kind: "notes_consumed", IDs: ids})
	return out
}

func (pr *probeRun) leadStatus(s string) {
	emit(pr.id, probeEvent{Kind: "status", Text: s})
}

func (pr *probeRun) chatInvestigate() probeAction {
	pr.c.docs = allDocs(pr.udb, pr.appliance.ID)
	pr.c.hasFreshImage = false
	for i := len(pr.messages) - 1; i >= 0; i-- {
		if pr.messages[i].Role == "user" {
			pr.c.hasFreshImage = len(pr.messages[i].Images) > 0
			break
		}
	}
	pr.c.leadPrompt = buildLeadSystemPrompt(pr.udb, pr.appliance, pr.c.docs, pr.cachedFacts, pr.cachedNotes, pr.cachedTechniques, pr.cachedRules, pr.cachedDiscoveries, pr.c.hasFreshImage)
	emit(pr.id, probeEvent{Kind: "status", Text: "Investigator analyzing…"})

	// Resolve the per-session injection queue so the orchestrator picks
	// up mid-flight user notes between rounds. Workers don't get the hook
	// — they finish their current task before the orchestrator sees the note.
	pr.c.injQ = LookupInjectionQueue(pr.id)
	// A follow-up that turns out to need real discovery gets the SAME plan
	// machinery Map has — set a checklist, work it step by step, report what
	// it could not determine. Without this, chat could only fire one-off
	// probes: there was no way to say "this is bigger than one probe", the
	// topic guard cut re-probing at 3, and the prompt told it to synthesize
	// the moment it had an answer. So a second question that needed a real
	// investigation quietly got a shallow one. required=false — most
	// questions ARE one probe, and taxing every follow-up with a 5-step plan
	// would be worse than the gap.
	pr.c.chatPlan = buildPlanTools(pr.id, false)
	pr.c.docInvestigatorTools = append([]AgentToolDef{pr.c.read_doc_tool, pr.c.update_doc_tool, pr.c.probe_tool}, pr.c.chatPlan.All()...)
	assertOnlyAllowedTools("servitor.doc_investigator", pr.c.docInvestigatorTools, servitorOrchestratorToolAllowList)
	// Lead migration (slice 2b): the investigator runs through the orchestrate
	// SCOPED path, so its sessions and tool recordings land in the appliance
	// scope (app:servitor:<id>). It keeps its OWN complete prompt verbatim via
	// SystemPromptOverride (content parity) and its loop knobs via Loop; the
	// mid-flight injection drain (with the notes_consumed UI signal) rides
	// Loop.OnRoundStart. All per-appliance context is in the system prompt, so
	// the run message is just the conversation.
	pr.c.orch = servitorOrch()
	if pr.c.orch == nil {
		emit(pr.id, probeEvent{Kind: "error", Text: "orchestrate runtime unavailable"})
		return actReturn
	}
	for i := len(pr.messages) - 1; i >= 0; i-- {
		if pr.messages[i].Role == "user" {
			pr.c.leadImages = pr.messages[i].Images
			break
		}
	}
	pr.c.leadScope = orchestrate.AgentScope{
		AgentID:   servitorInvestigatorAgentID,
		ScopeUser: applianceMemScope(pr.appliance.ID),
		SessionID: pr.id,
	}
	pr.c.leadLoop = &orchestrate.AgentLoopOverrides{
		MaxRounds:   75,
		SerialTools: true,
		// The appliance's own tier, on THIS path too. The map/probe branch
		// builds an AgentLoopConfig directly and has honored it since the
		// setting shipped; chat comes through the scoped-agent dispatch,
		// which had no way to carry it — so the setting saved, read back
		// correctly, and did nothing on the surface an operator actually
		// uses to ask a system a question.
		TierOverride: applianceTierOverride(pr.appliance.OrchestratorTier),
		ChatOptions:  append([]ChatOption{WithTemperature(0.2), WithThink(true)}, orchestratorThinkOpts()...),
		OnRoundStart: pr.drainInjections,
	}
	withHeartbeat(pr.ctx, pr.id, "Investigator: working", func() {
		pr.c.res, pr.c.err = pr.c.orch.RunScopedAgentRich(pr.ctx, pr.c.leadScope, orchestrate.AgentSyncRun{
			SubSessionID:         pr.id,
			Message:              buildScopedLeadMessage(pr.messages),
			Images:               pr.c.leadImages,
			FreshSession:         true,
			SystemPromptOverride: pr.c.leadPrompt,
			AppTools:             pr.c.docInvestigatorTools,
			Loop:                 pr.c.leadLoop,
			StatusCallback:       pr.leadStatus,
		})
	})
	if pr.c.res.Text != "" {
		pr.reply = strings.TrimSpace(pr.c.res.Text)
	}
	// Productive continuation — when the run caps (HitRoundCap) but probes are
	// still yielding NEW data, continue the SAME scoped session (FreshSession
	// defaults false) with another budget; stop once a pass gathers nothing new.
	for pass := 0; pass < maxDocInvestigatorPasses && pr.c.err == nil && pr.ctx.Err() == nil; pass++ {
		if !pr.c.res.HitRoundCap {
			break // natural finish — not a cap hit
		}
		before := len(pr.c.allProbeResults)
		if pending := pr.c.chatPlan.Pending(); pending > 0 {
			emit(pr.id, probeEvent{Kind: "status", Text: fmt.Sprintf(
				"Investigator reached its round budget with %d plan step(s) pending — continuing…", pending)})
		} else {
			emit(pr.id, probeEvent{Kind: "status", Text: "Investigator reached its round budget — continuing the investigation…"})
		}
		withHeartbeat(pr.ctx, pr.id, "Investigator: working (continued)", func() {
			pr.c.res, pr.c.err = pr.c.orch.RunScopedAgentRich(pr.ctx, pr.c.leadScope, orchestrate.AgentSyncRun{
				SubSessionID:         pr.id,
				Message:              "Continue the investigation from where you left off and finish answering the user's question.",
				SystemPromptOverride: pr.c.leadPrompt,
				AppTools:             pr.c.docInvestigatorTools,
				Loop:                 pr.c.leadLoop,
				StatusCallback:       pr.leadStatus,
			})
		})
		if pr.c.res.Text != "" {
			pr.reply = strings.TrimSpace(pr.c.res.Text)
		}
		if len(pr.c.allProbeResults) == before && pr.c.chatPlan.Pending() == 0 {
			break // nothing new AND nothing planned left — stop rather than grind
		}
	}
	if pr.c.err != nil && pr.ctx.Err() == nil {
		emit(pr.id, probeEvent{Kind: "error", Text: pr.c.err.Error()})
		return actReturn
	}
	return actNone
}

func (pr *probeRun) chatAfter() probeAction {
	if pr.reply == "" && pr.c.lastProbeResult != "" {
		pr.reply = pr.c.lastProbeResult
	}

	// Consolidation and verification use allProbeResults / lastProbeResult.
	if pr.c.lastProbeResult != "" && pr.udb != nil {
		workerOut := pr.c.lastProbeResult
		userQuestion := ""
		if n := len(pr.messages); n > 0 && pr.messages[n-1].Role == "user" {
			userQuestion = pr.messages[n-1].Content
		}
		pr.consolidateFn = func() {
			leadAnswer := pr.reply
			bgCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			var cMsg strings.Builder
			cMsg.WriteString(fmt.Sprintf("Consolidate knowledge for %s.\n\n", pr.appliance.Name))
			if userQuestion != "" {
				cMsg.WriteString(fmt.Sprintf("## User Question\n\n%s\n\n", userQuestion))
			}
			cMsg.WriteString(fmt.Sprintf("## Worker Findings\n\n%s\n\n", workerOut))
			if leadAnswer != "" {
				cMsg.WriteString(fmt.Sprintf("## Investigator Summary\n\n%s\n", leadAnswer))
			}
			pr.a.RunAgentLoop(bgCtx, []Message{{Role: "user", Content: cMsg.String()}}, AgentLoopConfig{
				SystemPrompt:    buildConsolidationPrompt(pr.appliance),
				Tools:           []AgentToolDef{pr.c.read_doc_tool, pr.c.update_doc_tool, pr.store_fact_tool, pr.link_entities_tool, pr.record_discovery_tool, pr.record_technique_tool, pr.note_lesson_tool},
				MaxRounds:       10,
				RouteKey:        "app.servitor",
				TierOverride:    applianceTierOverride(pr.appliance.OrchestratorTier),
				MaskDebugOutput: true,
				ChatOptions:     []ChatOption{WithThink(false)},
			})
			emit(pr.id, probeEvent{Kind: "status", Text: "Background: knowledge consolidated."})
		}
	}

	// Verification pass.
	//
	// The model is only consulted when a deterministic check finds an
	// identifier in the reply that is NOT in the findings verbatim. The
	// verifier's whole job is character-for-character comparison, and when
	// every path, name, address and version in the reply already appears in
	// the findings there is nothing for it to correct — so the call, a
	// findings-sized prefill sitting between the finished answer and the
	// user, is skipped. When the check finds a candidate, the model runs
	// exactly as before: it decides, not the heuristic.
	if pr.reply != "" && len(pr.c.allProbeResults) > 0 {
		rawFindings := strings.Join(pr.c.allProbeResults, "\n\n---\n\n")
		if len(rawFindings) > 24000 {
			rawFindings = rawFindings[:24000] + "\n... [truncated]"
		}
		if unverified := unverifiedIdentifiers(pr.reply, rawFindings); len(unverified) == 0 {
			Debug("[servitor] verification skipped: every identifier in the reply appears in the findings")
		} else {
			emit(pr.id, probeEvent{Kind: "status", Text: "Verifying names and identifiers…"})
			// Targeted find/replace pairs instead of "regenerate the whole
			// response": the old shape accepted ANY differing output as the
			// corrected answer, so a 27B that reformatted prose (or invented
			// a "fix") silently replaced a correct reply. Now the model can
			// only name identifier swaps, each one is verified against the
			// findings before applying, and a malformed verdict changes
			// nothing.
			verifyPrompt := "You are a fact-checker. Compare the response against the raw worker findings below. Your ONLY job: find specific identifiers in the response — table names, service names, file paths, usernames, database names, column names, IP addresses, port numbers, version strings — that do NOT appear character-for-character in the findings (wrong underscore, wrong prefix or suffix, wrong capitalization).\n\n" +
				"Respond with ONLY a JSON array of corrections, each {\"wrong\": \"<exact string copied from the response>\", \"right\": \"<exact string copied from the findings>\"}. If every identifier matches exactly, respond with [].\n\n" +
				"## Raw Worker Findings\n\n" + rawFindings
			verifyResp, verifyErr := pr.a.WorkerChat(pr.ctx,
				[]Message{
					{Role: "user", Content: "## Response to verify\n\n" + pr.reply},
				},
				WithSystemPrompt(verifyPrompt),
				WithTemperature(0.0),
				WithThink(false),
			)
			if verifyErr == nil && verifyResp != nil {
				var pairs []struct {
					Wrong string `json:"wrong"`
					Right string `json:"right"`
				}
				if derr := DecodeJSON(verifyResp.Content, &pairs); derr == nil {
					applied := 0
					for i, p := range pairs {
						if i >= 20 {
							break // runaway verdicts are noise, not corrections
						}
						// Both sides must check out: the wrong string has to
						// actually be in the reply, and the replacement has to
						// exist verbatim in the findings (no invented fixes).
						if p.Wrong == "" || p.Wrong == p.Right ||
							!strings.Contains(pr.reply, p.Wrong) || !strings.Contains(rawFindings, p.Right) {
							continue
						}
						pr.reply = strings.ReplaceAll(pr.reply, p.Wrong, p.Right)
						applied++
					}
					if applied > 0 {
						Debug("[servitor] verification corrected %d identifier(s)", applied)
					}
				}
			}
		}
	}
	return actNone
}

func (pr *probeRun) finishTurn() probeAction {
	if pr.reply == "" {
		return actReturn
	}
	if len(pr.sessionFailures) > 0 { // chat mode only
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d command(s) failed this session:\n", len(pr.sessionFailures))
		for _, f := range pr.sessionFailures {
			fmt.Fprintf(&sb, "• %s\n  → %s\n", f.Cmd, f.Reason)
		}
		emit(pr.id, probeEvent{Kind: "status", Text: strings.TrimSpace(sb.String())})
	}
	emit(pr.id, probeEvent{Kind: "reply", Text: pr.reply})

	// Persist this turn to the chat session that backs the rail. Includes
	// map runs (saveProfile=true): handleMap pre-creates the session
	// record, so the reconnaissance prompt + final summary land in the rail
	// and stay reviewable, not just streamed live. The run id IS the
	// session id; appendTurn no-ops if no record exists (so a saveProfile
	// run without a pre-created session simply isn't persisted). listSessions
	// surfaces it on the 'done' refresh, exactly like orchestrate.
	if pr.udb != nil {
		var lastUser string
		for i := len(pr.messages) - 1; i >= 0; i-- {
			if pr.messages[i].Role == "user" {
				lastUser = pr.messages[i].Content
				break
			}
		}
		appendTurn(pr.udb, pr.appliance.ID, pr.id, lastUser, pr.reply)
	}

	if pr.consolidateFn != nil {
		emit(pr.id, probeEvent{Kind: "status", Text: "Background: consolidating knowledge..."})
		go pr.consolidateFn()
	}

	if pr.saveProfile && pr.ownerUDB != nil {
		// The profile is shared appliance knowledge — persist to the OWNER's
		// store so a shared appliance has one profile regardless of who mapped it.
		var existing Appliance
		if pr.ownerUDB.Get(applianceTable, pr.appliance.ID, &existing) {
			// Don't let a Map run that hit issues clobber a good profile. Only
			// REPLACE the profile when this run actually completed and synthesized
			// a substantive mapping — a cancelled run, or a near-empty / stub
			// reply (a failed synthesis, an error note), or one that collapsed to
			// a fraction of the prior profile, keeps the previous one instead. A
			// stale-but-real profile beats an almost-empty one.
			newProfile := strings.TrimSpace(pr.reply)
			prior := strings.TrimSpace(existing.Profile)
			tooThin := len(newProfile) < minMapProfileChars ||
				(prior != "" && len(newProfile) < len(prior)/3)
			if pr.ctx.Err() != nil || tooThin {
				emit(pr.id, probeEvent{Kind: "status", Text: "Map didn't produce a complete profile — keeping the previous one."})
				Log("[servitor.map] kept prior profile for %q (new=%d chars, prior=%d chars, cancelled=%v)",
					pr.appliance.Name, len(newProfile), len(prior), pr.ctx.Err() != nil)
			} else {
				existing.Profile = pr.reply
				existing.Scanned = time.Now().Format(time.RFC3339)
				if fresh := extractLogMap(pr.reply); len(fresh) > 0 {
					existing.LogMap = mergeLogMap(existing.LogMap, fresh)
				}
				pr.ownerUDB.Set(applianceTable, pr.appliance.ID, existing)
				extractDocsFromProfile(pr.ownerUDB, pr.appliance.ID, pr.reply)
			}
		}
	}
	return actNone
}

// minMapProfileChars is the floor below which a Map run's synthesized profile is
// treated as "didn't complete" — a real reconnaissance profile is a structured,
// multi-section markdown doc well above this; a failed/partial run yields a short
// stub or error note. Below it (or a big collapse vs. the prior profile), the
// existing profile is preserved rather than overwritten.
const minMapProfileChars = 250
