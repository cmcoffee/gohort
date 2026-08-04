// What the agent said it was going to do, and hasn't.
//
// The in-turn guards stop a promise from shipping unbacked. This is for the one
// that gets through anyway — and something always gets through, because a guard
// runs out of corrections and a model can be talked into anything twice.
//
// The gap it closes is memory. Every turn arrives as a fresh call, so a user
// holding the agent to something it said a minute ago is answered by a model
// that has no idea it promised anything. Observed, in a group chat:
//
//	WiWee: On it — let me grab some reference photos and composite them in.
//	Craig: Wiwee, are you really ?
//	WiWee: Yeah, Craig. I'm really going to keep saying no until you stop
//	       asking or you actually approve something. Pick one.
//	Craig: Wiwee, you didn't really do what you said you were gonna do
//	WiWee: You're right, Craig. I'm an idiot sometimes. Let me actually fix
//	       this now instead of making promises I don't keep.
//
// Both replies came back in one round with no tool call. "are you really?" is
// three words with no antecedent in them, so the model bound it to the wrong
// thread entirely and answered a question nobody asked. Then, told plainly what
// it had done, it apologized and made the identical promise again — because
// nothing anywhere said "you already said that, and nothing happened."
//
// So: record what was promised, hand it to the next turn, and retire it the
// moment the work actually happens.

package orchestrate

import (
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const commitmentTable = "orchestrate_commitments"

// commitmentMaxTurns is how many turns an unkept promise stays in front of the
// model before it is dropped.
//
// Deliberately short. The failure this guards against is real but so is the
// opposite one: an agent reminded every turn that it owes someone something
// starts apologizing every turn, and the observed reply was already "You're
// right, Craig. I'm an idiot sometimes." Nagging a model that is prone to
// self-flagellation buys a worse conversation than the one it fixes.
//
// Two turns is enough to cover the challenge that follows a broken promise,
// which is when it matters. Anything still outstanding after that has been
// overtaken by the conversation, and if the model promises again it is recorded
// again — so a genuinely persistent problem renews itself for as long as it
// lasts, without anything having to nag.
const commitmentMaxTurns = 2

// openCommitment is one thing the agent said it would do and did not.
type openCommitment struct {
	// Said is the promise in the model's OWN words. Handing back a framework
	// paraphrase invites it to argue with the paraphrase; its own sentence is
	// not arguable.
	Said  string    `json:"said"`
	At    time.Time `json:"at"`
	Turns int       `json:"turns"` // turns this has survived, for the cap
}

// recordTurnCommitment closes the books on one turn: it clears any promise the
// turn made good on, and records a new one if the turn ended on a promise it
// did not keep.
//
// didWork is the retirement signal, and it is deliberately "called any tool at
// all" rather than "did the specific thing promised". Matching a promise to the
// work that satisfies it needs a judge; a turn that actually did something is a
// turn where the conversation has moved, and the cost of retiring early (a
// missed reminder) is far below the cost of retiring late (an agent apologizing
// for something it already handled).
func recordTurnCommitment(db Database, sessionID, reply string, didWork bool) {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if didWork {
		clearCommitment(db, sessionID)
		return
	}
	if !ReplyPromisesWork(reply) {
		// Nothing promised. An outstanding one AGES here rather than being
		// dropped: the user may have replied about something else entirely,
		// and the promise is still outstanding either way.
		ageCommitment(db, sessionID)
		return
	}
	said := oneLineError(strings.TrimSpace(reply), 220) // same flattener, same reason
	if said == "" {
		return
	}
	Log("[commitment] session %s ended on an unkept promise: %q", sessionID, said)
	db.Set(commitmentTable, sessionID, openCommitment{Said: said, At: time.Now()})
}

// openCommitmentFor returns the promise still outstanding in this conversation.
func openCommitmentFor(db Database, sessionID string) (openCommitment, bool) {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return openCommitment{}, false
	}
	var c openCommitment
	db.Get(commitmentTable, sessionID, &c)
	if strings.TrimSpace(c.Said) == "" {
		return openCommitment{}, false
	}
	return c, true
}

// ageCommitment ticks a surviving promise and drops it at the cap.
func ageCommitment(db Database, sessionID string) {
	c, ok := openCommitmentFor(db, sessionID)
	if !ok {
		return
	}
	c.Turns++
	if c.Turns >= commitmentMaxTurns {
		clearCommitment(db, sessionID)
		return
	}
	db.Set(commitmentTable, sessionID, c)
}

func clearCommitment(db Database, sessionID string) {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	db.Unset(commitmentTable, sessionID)
}

// commitmentNote is the turn-scoped context handed to the next turn.
//
// Every line of it is aimed at one of the two things the model actually did
// when challenged. It answers the ambiguous jab ("are you really?") by naming
// the antecedent, so the model has something to bind the question to instead of
// picking a thread at random. And it rules out the apology, because "You're
// right, I'm an idiot" followed by the same promise again is not a repair — it
// is the same failure with worse manners.
func commitmentNote(c openCommitment) string {
	return frameworkNoteTag +
		"OUTSTANDING FROM YOUR LAST TURN — you said: \"" + c.Said + "\"\n" +
		"You then called no tool and it did not happen. If they are asking about it now (\"did you\", \"are you really\", \"you said you would\"), THAT is what they mean.\n" +
		"Do it NOW with a real tool call, or say in one line what is actually stopping you. Do NOT apologize, do NOT call yourself names, and do NOT promise it again — a second promise is the same failure with better manners. Either it happens this turn or you say why it can't."
}

// commitmentTurnNote is the TurnNotes hook's view: the note if one is owed,
// empty otherwise.
func commitmentTurnNote(db Database, sessionID string) string {
	c, ok := openCommitmentFor(db, sessionID)
	if !ok {
		return ""
	}
	return commitmentNote(c)
}

// turnNotes composes every turn-scoped note this app supplies into the one
// string the loop appends to the newest user message.
//
// Order is deliberate: reference material first, the thing to ACT on last, so
// the instruction sits closest to where the model starts writing.
func turnNotes(sess *ToolSession, db Database, sessionID, userMessage string) string {
	var parts []string
	if n := imageSpaceNote(sess, userMessage); n != "" {
		parts = append(parts, n)
	}
	if n := commitmentTurnNote(db, sessionID); n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, "\n\n")
}
