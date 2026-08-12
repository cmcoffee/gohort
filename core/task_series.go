// A SERIES: several pieces of background work the model declared up front, done
// one at a time.
//
// The detach ledger allows exactly one background job per tool per turn, and
// that rule is right — two jobs for one request deliver the same thing twice
// (see DetachLedger). But it also had no answer for the honest case: "I'll make
// you three variations." The first render detached, the model was told not to
// call the tool again, and the other two never happened. What the user got was
// one picture and a promise nothing was keeping.
//
// A series is the answer, and it does NOT weaken the one-slot rule: still one
// job at a time, still one per turn. What it adds is a count that survives the
// turn. Each finished piece wakes the conversation, the wake note carries an
// instruction to start the next one, and the wake turn's ledger is fresh — so
// the next call detaches legitimately rather than being refused. Three pieces
// are three turns, in order, each one delivered as it lands.
//
// The count lives HERE rather than in the model's head on purpose. Asking it to
// carry "2 of 3" across a wake, several minutes, and whatever the user said in
// between is asking it to drop the thing quietly; nothing downstream could tell
// a finished series from an abandoned one.
//
// In memory, like the staged attachments it travels beside: a series is a live
// intention, not a record. A restart ends the chain, which is the correct
// failure — the wake notes are gone too, and what was already rendered is in the
// image space under a handle the user can still ask for by name.
package core

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	RegisterTunable(TunableSpec{
		Key: "tune_task_series_max", Category: "Limits",
		Label: "Background pieces per series",
		Help:  "Ceiling on how many pieces of background work one declared series may run — \"make me four variations\" and the like. Each piece is a separate background job, run one after another, so this is the number of extra messages the user gets. A request for more is clamped to this, not refused.",
		Kind:  KindInt, Default: 4, Min: 1, Max: 10,
	})
}

// taskSeriesTTL bounds how long an unfinished series stays open. Reached when
// the model simply stops — it was asked to start the next piece and answered
// the user instead — and the entry would otherwise sit there until the process
// ended, ready to renumber an unrelated request an hour later.
const taskSeriesTTL = 30 * time.Minute

type taskSeries struct {
	total int
	step  int // pieces claimed so far, 1-based once claimed
	at    time.Time
}

var (
	taskSeriesMu     sync.Mutex
	taskSeriesLedger = map[string]*taskSeries{}
)

func taskSeriesKey(sessionID, tool string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(tool)
}

// AdvanceTaskSeries claims the next piece of a series for this conversation and
// tool, opening one when want is more than a single piece and none is open.
//
// Returns the 1-based piece just claimed and the total. (0, 0) means this call
// is not part of a series — a lone piece of work, which is almost every call.
//
// want is read only when OPENING. A model that declares "three variations" on
// its first call has said everything it needs to; re-declaring it on each later
// call is bookkeeping it would get wrong, and omitting it must not silently end
// the series it is halfway through.
func AdvanceTaskSeries(sessionID, tool string, want int) (piece, of int) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(tool) == "" {
		return 0, 0
	}
	key := taskSeriesKey(sessionID, tool)

	taskSeriesMu.Lock()
	defer taskSeriesMu.Unlock()
	sweepTaskSeriesLocked()

	s := taskSeriesLedger[key]
	if s == nil {
		if want < 2 {
			return 0, 0
		}
		if max := taskSeriesMax(); want > max {
			// Clamped rather than refused. The number is the model's reading of
			// "a few", and an error over it costs a round to correct something
			// nobody cared about the exact value of.
			Log("[task] %s asked for a series of %d; clamped to %d", tool, want, max)
			want = max
		}
		s = &taskSeries{total: want}
		taskSeriesLedger[key] = s
	}
	s.step++
	s.at = time.Now()
	piece, of = s.step, s.total
	if piece >= of {
		delete(taskSeriesLedger, key) // finished — the last piece closes it
	}
	return piece, of
}

// ExtendTaskSeries records that a REFUSED second call was a set rather than a
// repeat, and returns the size of the set it belongs to (0 when there is none
// to make).
//
// This is the path that actually gets used, because it is the shape the model
// naturally produces. Told to make four variations it does not declare four —
// it calls the tool four times, the way it would if nothing detached. The
// declared count (see AdvanceTaskSeries) is the tidy route and the schema asks
// for it; this is the one that catches the honest attempt, and it needs no
// cooperation at all.
//
// Extends only while the first piece is still in flight — step 0, meaning the
// turn that declared it has not yet had one land. After that a second call in
// the same turn is a model mis-sequencing a set it already has, not asking for
// a bigger one, and growing the set every time it does that is how three
// variations become nine.
func ExtendTaskSeries(sessionID, tool string) (total int) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(tool) == "" {
		return 0
	}
	key := taskSeriesKey(sessionID, tool)

	taskSeriesMu.Lock()
	defer taskSeriesMu.Unlock()
	sweepTaskSeriesLocked()

	s := taskSeriesLedger[key]
	if s == nil {
		// Two: the piece already running, and the one just refused.
		s = &taskSeries{total: 2, at: time.Now()}
		taskSeriesLedger[key] = s
		return s.total
	}
	if s.step == 0 && s.total < taskSeriesMax() {
		s.total++
	}
	s.at = time.Now()
	return s.total
}

// CloseTaskSeries ends a series early: a piece failed, or the work was
// abandoned. The remaining pieces are NOT retried — a chain that repairs itself
// silently is a chain nobody asked for that keeps sending pictures.
func CloseTaskSeries(sessionID, tool string) {
	taskSeriesMu.Lock()
	defer taskSeriesMu.Unlock()
	delete(taskSeriesLedger, taskSeriesKey(sessionID, tool))
}

// TaskSeriesOpen reports whether a series is mid-flight, for tests and for a
// caller that wants to know before it acts.
func TaskSeriesOpen(sessionID, tool string) bool {
	taskSeriesMu.Lock()
	defer taskSeriesMu.Unlock()
	_, ok := taskSeriesLedger[taskSeriesKey(sessionID, tool)]
	return ok
}

func taskSeriesMax() int {
	if n := TuneInt("tune_task_series_max"); n >= 1 {
		return n
	}
	return 4
}

func sweepTaskSeriesLocked() {
	for k, s := range taskSeriesLedger {
		if time.Since(s.at) > taskSeriesTTL {
			delete(taskSeriesLedger, k)
		}
	}
}

// BookSeriesPiece records a finished piece of detached work against the set and
// leaves the instruction that starts the next one, when any remain.
//
// Returns the 1-based piece and the total, or (0, 0) when this is not part of a
// set — so the caller can word its own result line and nothing else has to know
// what the work was. Inline calls book nothing: they still have a round of
// their own, and there is no wake to instruct.
//
// Shared so that every tool that can produce a piece books it the same way. The
// grouped `image` tool and each connector's generate_image_<name> are the same
// act under two names, and when only one of them counted, a set begun on one
// and continued on the other lost its place.
func BookSeriesPiece(sess *ToolSession, key string, want int, what string) (piece, of int) {
	if sess == nil || !sess.Detached {
		return 0, 0
	}
	piece, of = AdvanceTaskSeries(sess.DeliverySession(), key, want)
	if c := SeriesContinuation(piece, of, what); c != "" {
		sess.SetTaskContinuation(c)
	}
	return piece, of
}

// SeriesContinuationMarker opens every continuation, so the turn that carries
// one can be told apart afterwards from an ordinary wake.
//
// The distinction decides what "no tool calls" means. An ordinary wake has
// nothing left to do but say what came back, and answering in one line with no
// tool call is the CORRECT shape — the scheduled path suppresses such turns and
// had to be taught not to suppress those. A wake carrying a continuation is the
// opposite: it was asked to start the next piece, so a turn with no tool call
// is a set that just quietly stopped, and nothing downstream could see it.
const SeriesContinuationMarker = "[SET IN PROGRESS]"

// SeriesContinuation is the instruction handed to the turn that DELIVERS piece
// N, telling it to start piece N+1.
//
// It has to carry the count, because the wake turn is a new turn and its only
// knowledge of the series is this sentence. And it has to repeat the delivery
// rules the detach notice already set out — say you are on it, do not narrate
// the machinery — because those were addressed to a turn that has since ended,
// and a wake turn that has not been told them will happily explain queues.
func SeriesContinuation(piece, of int, what string) string {
	if of <= 1 || piece >= of {
		return ""
	}
	var b strings.Builder
	b.WriteString(SeriesContinuationMarker + " THIS IS PIECE " + strconv.Itoa(piece) + " OF " + strconv.Itoa(of) + " — you are not done.\n")
	// The TOOL CALL FIRST, and said before anything about the reply.
	//
	// This instruction used to open with "deliver this one, and in the SAME turn
	// start the next", and what came back was a turn that delivered beautifully
	// and started nothing: "Here is your first take… Starting the second
	// variation now." — no tool call, set stranded at 1 of 3. A model told to
	// write and then act writes, and a turn that has produced its reply is over.
	// Asking for the act first costs nothing and is the difference between a set
	// that finishes and a promise in prose.
	b.WriteString("FIRST, before you write a single word of your reply: call the same tool again to start the next one")
	if w := strings.TrimSpace(what); w != "" {
		b.WriteString(" (" + w + ")")
	}
	b.WriteString(" — with a prompt that is a genuine variation on the idea rather than a repeat of the one you just used: a different angle, palette, composition, or mood, whichever the request was about.\n")
	b.WriteString("THEN, in the same turn, write one line delivering the piece that just finished. Both, in that order. A turn that only writes the line is a turn that abandoned the set — the words \"starting the next one\" are not starting it.\n")
	// The failure this line exists for: the model reads "start the next one",
	// starts it, and then tells the user about the mechanism — "the second
	// variation is now running in the background" — which is exactly the
	// machinery the detach notice bans, restated by a turn that never saw it.
	b.WriteString("Say it the way a person would: here is the first, the next is coming. Do NOT mention jobs, queues, backgrounds or waiting, do NOT put a time on it, and do NOT ask whether they want the rest — they already said yes by asking for several.\n")
	b.WriteString("Start the next one UNLESS the user has since asked for something different or told you to stop; if they have, say what you are dropping and drop it.")
	return b.String()
}

// CloseTaskSeriesForSession ends every set running in a conversation.
//
// For cancellation. Stopping one background job has to stop the SET it belonged
// to, or the button lies: kill the second of four renders and the next wake
// cheerfully starts the third, because the ledger still says there are pieces
// left and nothing told it otherwise.
//
// By session rather than by tool, because whoever cancels a run knows which
// conversation it was for and not which tool identity was rationing it — and
// "stop this" means stop the work, not stop one accounting key.
func CloseTaskSeriesForSession(sessionID string) int {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0
	}
	prefix := sessionID + "\x00"
	taskSeriesMu.Lock()
	defer taskSeriesMu.Unlock()
	closed := 0
	for k := range taskSeriesLedger {
		if strings.HasPrefix(k, prefix) {
			delete(taskSeriesLedger, k)
			closed++
		}
	}
	return closed
}
