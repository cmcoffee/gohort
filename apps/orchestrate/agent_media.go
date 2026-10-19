package orchestrate

// Agents hand each other pictures, audio and video, both ways.
//
// They passed text and nothing else. A song a music agent made reached the
// agent that asked for it as a sentence saying it was made, and on a channel
// turn it reached nobody: the wrapper claimed it for a page there was not.
// And an agent could not give the agent it ran a photo someone had sent it.
//
// Back: what the run attached goes onto the session of the agent that ran it
// (handBack), so it leaves with that one's reply, wherever the reply goes. On
// a web turn what a page already showed live stays where it is, so nothing is
// delivered twice. A delegation has no shared session, so its run hands its
// media to a sink on its context (withMediaReturn) that the caller empties.
//
// Out: the caller names what to hand over in `attach`, by its own names for
// it (handOut), and each item arrives as the run's own inbound media, media#1
// onward, exactly as a photo a person sent would (receiveHanded). Text and
// other files need none of this: the two share a working folder, and naming
// the file in the message is enough.

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handBack moves what the agent a run dispatched attached (a picture, a song,
// a file) onto the session of the agent that ran it, so it goes out with that
// one's reply wherever the reply goes, and returns the line that tells it so.
// What a page already showed live stays where it is. Before, agents passed
// each other text and nothing else: a music agent's song reached its caller
// as a sentence saying it was made.
func handBack(agent string, from, to *ToolSession) string {
	images, videos, files := from.HandOverUnflushed(to)
	var got []string
	for _, c := range []struct {
		n          int
		one, other string
	}{{images, "an image", "%d images"}, {videos, "an audio or video file", "%d audio or video files"}, {files, "a file", "%d files"}} {
		switch {
		case c.n == 1:
			got = append(got, c.one)
		case c.n > 1:
			got = append(got, fmt.Sprintf(c.other, c.n))
		}
	}
	if len(got) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\n[%s attached %s. It is part of your turn now and goes out with your reply: do not attach it again, and do not say it is missing.]", agent, strings.Join(got, " and "))
}

type mediaReturnKey struct{}

// withMediaReturn is ctx for a run whose attachments go to sink when it ends:
// a delegation, whose result is otherwise text alone. The caller then hands
// them on (handBack).
func withMediaReturn(ctx context.Context, sink *ToolSession) context.Context {
	return context.WithValue(ctx, mediaReturnKey{}, sink)
}

// mediaReturnFrom is the session a run's attachments go back to, or nil.
func mediaReturnFrom(ctx context.Context) *ToolSession {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(mediaReturnKey{}).(*ToolSession)
	return s
}

// handedItem is one attachment an agent hands the agent it runs.
type handedItem struct {
	kind string // "image" or "video" (audio rides video, as everywhere)
	raw  []byte
	was  string // what the caller called it
	from string // the agent that handed it over
}

type handedMediaKey struct{}

// withHandedMedia is ctx for a run that receives items as its inbound media,
// for the paths that build the run's session out of the caller's sight (a
// delegation, a handed-off run).
func withHandedMedia(ctx context.Context, items []handedItem) context.Context {
	if len(items) == 0 {
		return ctx
	}
	return context.WithValue(ctx, handedMediaKey{}, items)
}

// handedMediaFrom is what ctx hands the run it starts, or nil.
func handedMediaFrom(ctx context.Context) []handedItem {
	if ctx == nil {
		return nil
	}
	items, _ := ctx.Value(handedMediaKey{}).([]handedItem)
	return items
}

// handOut resolves the attachments an agent names for the agent it runs,
// against its own session: something sent to it (media#N), an image it kept,
// or a picture, audio or video file in its working folder. It returns them
// with the line that tells the receiving agent what it was handed. A name that
// resolves to nothing is refused, so the caller hears it rather than the run
// going out without it.
func handOut(caller *ToolSession, from string, refs []string) ([]handedItem, string, error) {
	var items []handedItem
	var missing, lines []string
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		b64, kind, ok := resolveAttachmentRef(caller, ref, true)
		raw, err := base64.StdEncoding.DecodeString(b64)
		if !ok || err != nil || len(raw) == 0 {
			missing = append(missing, ref)
			continue
		}
		if kind != "video" {
			kind = "image"
		}
		items = append(items, handedItem{kind: kind, raw: raw, was: ref, from: from})
		what := "an image"
		if kind == "video" {
			what = "an audio or video file"
		}
		lines = append(lines, fmt.Sprintf("media#%d (%s, which %s called %s)", len(items), what, from, ref))
	}
	if len(missing) > 0 {
		return nil, "", fmt.Errorf("nothing was sent: %s is not something you can hand over. attach takes media#N (something sent to you), an image you kept, or a picture, audio or video file in your working folder; a text or other file needs no attach, name it in the message, the agent shares your working folder", strings.Join(missing, ", "))
	}
	if len(items) == 0 {
		return nil, "", nil
	}
	return items, fmt.Sprintf("\n\n[%s handed you, with this request: %s. Use them by those ids, as you would something a person sent you.]", from, strings.Join(lines, "; ")), nil
}

// receiveHanded makes what an agent was handed its own inbound media, in the
// order handOut numbered them.
func receiveHanded(sess *ToolSession, items []handedItem) {
	for _, it := range items {
		sess.RegisterInboundMedia(it.kind, it.raw, it.from)
	}
}

// takeHandedMedia gives sess what ctx hands the run, and returns ctx without
// it: handed to this run only, not to one it starts in turn.
func takeHandedMedia(ctx context.Context, sess *ToolSession) context.Context {
	items := handedMediaFrom(ctx)
	if len(items) == 0 {
		return ctx
	}
	receiveHanded(sess, items)
	return context.WithValue(ctx, handedMediaKey{}, []handedItem(nil))
}

// handerName is the agent that delegates, by name, for the line that tells
// the receiving agent who handed it something.
func handerName(owner, agentID string) string {
	if rec, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, agentID); ok && strings.TrimSpace(rec.Name) != "" {
		return rec.Name
	}
	return chFirst(strings.TrimSpace(agentID), "the agent that asked")
}
