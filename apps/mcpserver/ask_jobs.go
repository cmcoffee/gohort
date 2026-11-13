package mcpserver

// An agent turn asked over MCP as a job its client checks back on.
//
// ask_agent used to hold the request open until the agent finished. A turn
// that searched, read and wrote ran past the client's own limits: Claude Code
// waits at most 60 seconds for the first byte of an HTTP tool call unless its
// server entry says otherwise, and gives up on one silent for five minutes,
// so a long turn ended as a timeout on the client while it was still working
// here. Now ask_agent waits a short while (askWait) and, when the turn is not
// done, answers with a job id; agent_result waits on that job a short while
// each time it is called. Every call is short, so no client limit is reached,
// and the turn runs on in the meantime: a client that gives up or disconnects
// does not stop it.
//
// A job belongs to the key that started it: no other key, and no other
// owner, reads it. It is kept in memory, and for jobKeep after it ends, so a
// reply can be fetched again; a restart drops it, as it drops the turn.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// askWait is how long ask_agent waits for the turn before handing back a
// job, and resultWait how long each agent_result waits on it: well inside
// the 60 seconds a client waits for a first byte. jobKeep is how long a
// finished job's reply stays fetchable.
var (
	askWait    = 20 * time.Second
	resultWait = 20 * time.Second
	jobKeep    = time.Hour
)

// askJob is one agent turn asked over MCP.
type askJob struct {
	id, owner, key, agent, conversation string
	started                             time.Time
	done                                chan struct{}

	mu     sync.Mutex
	status string // the agent's latest progress note, while it works
	ended  time.Time
	text   string
	images []string
	err    error
}

var askJobs = struct {
	sync.Mutex
	m map[string]*askJob
}{m: map[string]*askJob{}}

// startAskJob runs one turn as a job: run is the turn, given a context that
// outlives the request that started it and a callback for its progress
// notes.
func startAskJob(ctx context.Context, owner, key, agent, conversation string, run func(ctx context.Context, status func(string)) (string, []string, error)) *askJob {
	j := &askJob{id: newJobID(), owner: owner, key: key, agent: agent, conversation: conversation,
		started: time.Now(), done: make(chan struct{})}
	askJobs.Lock()
	sweepAskJobs()
	askJobs.m[j.id] = j
	askJobs.Unlock()
	// Not the request's context: the turn is the work, and the client
	// checking back for it is the point of the job.
	runCtx := context.WithoutCancel(ctx)
	go func() {
		text, images, err := run(runCtx, j.setStatus)
		j.mu.Lock()
		j.text, j.images, j.err, j.ended = text, images, err, time.Now()
		j.mu.Unlock()
		close(j.done)
	}()
	return j
}

// sweepAskJobs drops the jobs that ended more than jobKeep ago. Called with
// askJobs held.
func sweepAskJobs() {
	for id, j := range askJobs.m {
		j.mu.Lock()
		gone := !j.ended.IsZero() && time.Since(j.ended) > jobKeep
		j.mu.Unlock()
		if gone {
			delete(askJobs.m, id)
		}
	}
}

// askJobFor is the job id, when owner started it with this key.
func askJobFor(owner, key, id string) (*askJob, bool) {
	askJobs.Lock()
	defer askJobs.Unlock()
	sweepAskJobs()
	j, ok := askJobs.m[strings.TrimSpace(id)]
	if !ok || j.owner != owner || j.key != key {
		return nil, false
	}
	return j, true
}

func (j *askJob) setStatus(s string) {
	j.mu.Lock()
	j.status = strings.TrimSpace(s)
	j.mu.Unlock()
}

// await is the job's reply when it ends within wait, or a note that it is
// still working and how to check back. A client that goes away first gets
// its context's error; the turn goes on. A reply in a named conversation
// says which, so a client keeping several apart can tell them apart.
func (j *askJob) await(ctx context.Context, wait time.Duration) (string, []string, error) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-j.done:
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.err != nil {
			return "", nil, j.err
		}
		if j.conversation != "" {
			return fmt.Sprintf("[conversation: %s]\n\n%s", j.conversation, j.text), j.images, nil
		}
		return j.text, j.images, nil
	case <-ctx.Done():
		return "", nil, ctx.Err()
	case <-t.C:
		return j.stillWorking(), nil, nil
	}
}

// stillWorking is what a check-in says while the turn runs: the job, how
// long it has been going, what the agent last said it was doing, and how to
// get the reply.
func (j *askJob) stillWorking() string {
	j.mu.Lock()
	now := j.status
	j.mu.Unlock()
	line := fmt.Sprintf("Still working on it (job %s, %s so far", j.id, time.Since(j.started).Round(time.Second))
	if now != "" {
		line += "; now: " + textutil.Clip(strings.Join(strings.Fields(now), " "), 200)
	}
	return line + fmt.Sprintf(`). Call agent_result with job "%s" to get the reply; each call waits up to %s for it.`, j.id, resultWait)
}

// agentResult is the agent_result tool: the reply of a job this key
// started, waiting on it a short while when it is still running.
func (T *MCPServer) agentResult(ctx context.Context, owner string, token *AccountToken, args map[string]any) (string, []string, error) {
	id, _ := args["job"].(string)
	if strings.TrimSpace(id) == "" {
		return "", nil, fmt.Errorf("job is required: the id ask_agent gave when it was still working")
	}
	j, ok := askJobFor(owner, keyID(token), id)
	if !ok {
		return "", nil, fmt.Errorf("no job %q for this key: it ended more than %s ago, gohort restarted since, or another key started it", strings.TrimSpace(id), jobKeep)
	}
	return j.await(ctx, resultWait)
}

// keyID is the key a job belongs to: the key's id, or nothing for a call made
// another way (a signed-in session), which then sees only its own such jobs.
func keyID(token *AccountToken) string {
	if token == nil {
		return ""
	}
	return token.ID
}

func newJobID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// mcpThread is the conversation a turn goes to. One thread per key, so two
// clients of one owner each keep their own and a key narrowed to some agents
// does not read what another key's turns left in a shared one; and, named,
// a thread of its own under that key, so a client can keep separate work
// apart and start afresh.
func mcpThread(token *AccountToken, conversation string) string {
	session := mcpSession
	if k := keyID(token); k != "" {
		session += ":" + k
	}
	if slug := threadSlug(conversation); slug != "" {
		session += ":" + slug
	}
	return session
}

// threadSlug is a conversation's name as part of a session id: lower case,
// letters and digits, runs of anything else one dash, at most 48 long.
func threadSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// mcpSender is who a turn is from in the transcript: the key's own name (the
// client it was made for), else what clients have always been called here.
func mcpSender(token *AccountToken) string {
	if token != nil && strings.TrimSpace(token.Name) != "" {
		return strings.TrimSpace(token.Name)
	}
	return "MCP client"
}

// toolScope is the name a key's tool list grants a tool by: agent_result and
// list_conversations are part of ask_agent, so a key that may ask may fetch
// what it asked and find its conversations again, and a key narrowed before
// they existed keeps working.
func toolScope(name string) string {
	switch name {
	case "agent_result", "list_conversations":
		return "ask_agent"
	}
	return name
}

// chFirstNonBlank is the first of its arguments that is not blank.
func chFirstNonBlank(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// listConversations is the list_conversations tool: this key's threads, by
// the name ask_agent's conversation argument takes to continue each.
func (T *MCPServer) listConversations(owner string, token *AccountToken) string {
	prefix := mcpThread(token, "")
	threads := ListMCPThreads(owner, prefix)
	if len(threads) == 0 {
		return "No conversations yet for this key."
	}
	var b strings.Builder
	for _, t := range threads {
		name := conversationOf(t.Thread, prefix)
		label := name
		if label == "" {
			label = "(the default conversation: leave conversation out)"
		} else if t.Title != "" && t.Title != name {
			label = t.Title + " (conversation: " + name + ")"
		}
		fmt.Fprintf(&b, "- %s, with %s (%s): %d message(s), last active %s\n",
			label, chFirstNonBlank(t.AgentName, t.AgentID), t.AgentID, t.Messages, t.LastAt.Format("2006-01-02 15:04"))
	}
	return strings.TrimSpace(b.String())
}

// conversationOf is the conversation name a thread id carries after the
// key's prefix, or "" for the key's default thread.
func conversationOf(thread, prefix string) string {
	if thread == prefix {
		return ""
	}
	return strings.TrimPrefix(thread, prefix+":")
}
