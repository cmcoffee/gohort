package bridges

// What an inbound message carries, made readable before anything judges it.
//
// Two things went wrong with a photo sent to an agent's group chat. The text
// of a photo-only iMessage is the object replacement character iMessage puts
// where the attachment sits, so the gatekeeper read "[image with caption: ￼]"
// as a captioned picture with nothing to say and blocked it. And a blocked
// message's pictures stayed recorded only: "there ya go, can you make it now?"
// arrived next with nothing attached, so the agent had no picture to make it
// from.

import (
	"strings"
	"sync"
	"time"
)

// attachmentMark is the character iMessage writes into a message's text where
// an attachment sits.
const attachmentMark = "￼"

// withoutAttachmentMarks is text with the attachment marks taken out, when the
// message carries the media they stood for. A message with none keeps its text
// as it came: the mark is then the only sign anything was attached.
func withoutAttachmentMarks(text string, hasMedia bool) string {
	if !hasMedia || !strings.Contains(text, attachmentMark) {
		return text
	}
	return strings.TrimSpace(strings.ReplaceAll(text, attachmentMark, ""))
}

// heldMediaTTL is how long a blocked message's pictures wait for the sender's
// next message that is let through.
const heldMediaTTL = 10 * time.Minute

// heldMediaMax bounds the pictures held for one sender in one conversation.
const heldMediaMax = 6

type heldMediaEntry struct {
	images []string
	videos []string
	at     time.Time
}

// heldMedia is the pictures of blocked messages, by owner, conversation and
// sender. In memory: a restart drops them, and they are still in the thread.
var heldMedia = struct {
	sync.Mutex
	m map[string]*heldMediaEntry
}{m: map[string]*heldMediaEntry{}}

func heldMediaKey(owner, chatID, handle string) string {
	return owner + "\x00" + strings.TrimSpace(chatID) + "\x00" + normalizeIdentity(handle)
}

// holdBlockedMedia keeps a blocked message's pictures and videos for the
// sender's next message that is let through.
func holdBlockedMedia(owner, chatID, handle string, images, videos []string) {
	if len(images)+len(videos) == 0 {
		return
	}
	heldMedia.Lock()
	defer heldMedia.Unlock()
	sweepHeldMediaLocked()
	k := heldMediaKey(owner, chatID, handle)
	e := heldMedia.m[k]
	if e == nil {
		e = &heldMediaEntry{}
		heldMedia.m[k] = e
	}
	e.images = append(e.images, images...)
	e.videos = append(e.videos, videos...)
	if n := len(e.images); n > heldMediaMax {
		e.images = e.images[n-heldMediaMax:]
	}
	if n := len(e.videos); n > heldMediaMax {
		e.videos = e.videos[n-heldMediaMax:]
	}
	e.at = time.Now()
}

// takeHeldMedia returns, and forgets, what a blocked message from this sender
// left waiting in this conversation.
func takeHeldMedia(owner, chatID, handle string) (images, videos []string) {
	heldMedia.Lock()
	defer heldMedia.Unlock()
	sweepHeldMediaLocked()
	k := heldMediaKey(owner, chatID, handle)
	e := heldMedia.m[k]
	if e == nil {
		return nil, nil
	}
	delete(heldMedia.m, k)
	return e.images, e.videos
}

func sweepHeldMediaLocked() {
	for k, e := range heldMedia.m {
		if time.Since(e.at) > heldMediaTTL {
			delete(heldMedia.m, k)
		}
	}
}

// selfCopyWindow is how far apart the two copies of a message to yourself may
// arrive and still be read as one message.
const selfCopyWindow = 15 * time.Second

// selfLegs remembers, for a few seconds, each message the owner sent into a
// one-to-one conversation, so the received copy of it can be told apart.
var selfLegs = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

// isSelfCopy reports whether an inbound is the second copy of a message the
// owner sent themselves. Texting your own number writes two rows, the one your
// account sent and the one it received, and each reached the agent as its own
// message: every message answered twice, in a thread where both sides are you.
//
// The copy is the received message from the thread's own number carrying the
// text of one the owner sent into it seconds before. The configured own handle
// (SelfHandle) has to be this thread when it is set; unset, the pairing is the
// only signal there is. Either order is handled where the thread is known to
// be the owner's own; elsewhere only the received copy is taken as the copy,
// so the owner's own message is never the one dropped.
func (T *Bridges) isSelfCopy(chatID, handle, text string) bool {
	addr := chatHandle(chatID)
	norm := strings.Join(strings.Fields(strings.ToLower(text)), " ")
	if addr == "" || norm == "" {
		return false // a group, or nothing to compare
	}
	fromMe := strings.TrimSpace(handle) == ""
	if !fromMe && normalizeIdentity(handle) != normalizeIdentity(addr) {
		return false
	}
	known := T.isSelfThread(chatID, addr)
	if strings.TrimSpace(T.config().SelfHandle) != "" && !known {
		return false // the owner's own number is set, and this thread is not it
	}
	k := normalizeIdentity(addr) + "\x00" + norm
	selfLegs.Lock()
	defer selfLegs.Unlock()
	for key, at := range selfLegs.m {
		if time.Since(at) > selfCopyWindow {
			delete(selfLegs.m, key)
		}
	}
	mine, theirs := "s\x00"+k, "r\x00"+k
	if fromMe {
		if _, ok := selfLegs.m[theirs]; ok {
			delete(selfLegs.m, theirs)
			return true
		}
		selfLegs.m[mine] = time.Now()
		return false
	}
	if _, ok := selfLegs.m[mine]; ok {
		delete(selfLegs.m, mine)
		return true
	}
	if known {
		selfLegs.m[theirs] = time.Now()
	}
	return false
}
