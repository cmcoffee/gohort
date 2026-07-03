// Channel agent runner. core (core/channel.go) owns the Channel store and the
// inbound→agent seam; this is the agent-aware half: when a message arrives on
// a bound Channel, the transport (phantom) calls core.RunChannelAgent, which
// dispatches here to run the bound agent in a per-contact session and return
// its reply for the transport to deliver. Mirrors registerStandingRunner.
// See docs/channels-and-agents.md.

package orchestrate

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// channelSurfaceContext renders a one-line provenance note for a channel
// inbound — which transport and conversation it arrived on, and where the
// agent's reply goes. It is appended (LLM-only, not persisted) to the inbound
// message so the agent stays grounded about its reply destination and won't
// confabulate one or offer to "send it to" the channel it's already on.
// Returns "" when no channel resolves (nothing trustworthy to say).
func channelSurfaceContext(in ChannelInbound) string {
	ch, ok := channelForChat(in.Owner, in.ChatID, in.Handle)
	if !ok {
		return ""
	}
	// Three DISTINCT identifiers, surfaced separately so the agent doesn't
	// conflate them (it was calling the SERVICE the channel name):
	//   - channel:      the user's label for THIS binding (e.g. "iPhone")
	//   - service:      the transport it rides (e.g. iMessage)
	//   - conversation: the room/contact (e.g. "Alex Rivera")
	channel := strings.TrimSpace(ch.Name)
	service := ServiceDisplayName(ch.Service)
	convo := chFirst(in.ConversationName, in.SenderName, "this conversation")
	var origin string
	switch {
	case channel != "" && service != "":
		origin = fmt.Sprintf("your %q channel (transport: %s)", channel, service)
	case channel != "":
		origin = fmt.Sprintf("your %q channel", channel)
	case service != "":
		origin = fmt.Sprintf("a %s channel", service)
	default:
		origin = "a connected channel"
	}
	// Group roster, when known: hand the agent who's in the conversation up
	// front so it doesn't misattribute messages or have to call list_members
	// just to know who's present. Empty for 1:1 chats.
	roster := ""
	if len(in.Roster) > 0 {
		roster = fmt.Sprintf(" Participants in this conversation: %s.", strings.Join(in.Roster, ", "))
	}
	// Binding scope: a whole-service binding (empty Address) sees EVERY chat on
	// this transport; a scoped binding sees only this contact/group. Surface which
	// so the agent reasons correctly about how much it can see and act on.
	scope := ""
	if strings.TrimSpace(ch.Address) == "" {
		svcLabel := service
		if svcLabel == "" {
			svcLabel = "this"
		}
		scope = fmt.Sprintf(" This channel is bound to the whole %s service, so you see and can act across ALL conversations on it, not only this one.", svcLabel)
	} else {
		scope = " This channel is scoped to just this conversation on the transport, not the whole service."
	}
	// A receive-only channel doesn't reply on this surface; bidirectional (the
	// default) does. Ground the agent on which it is.
	if ch.Direction == DirectionInbound {
		return fmt.Sprintf("[CHANNEL CONTEXT: This message arrived on %s, in the conversation %q.%s%s Channel name, transport, and conversation are three different things; keep them distinct. This is a receive-only channel, so your reply is NOT delivered back here. Act on the information or route it elsewhere if needed. To find a participant's number or handle (e.g. to call or text them), look it up with list_members or read_chat — don't say you don't have a contact you can resolve from the conversation's roster.]", origin, convo, roster, scope)
	}
	return fmt.Sprintf("[CHANNEL CONTEXT: This message arrived on %s, in the conversation %q.%s%s Channel name, transport, and conversation are three different things; keep them distinct. Your reply is delivered straight back to this same conversation automatically: you don't need a tool to send it, and don't offer to \"send it to\" this channel, you're already on it. Reaching a DIFFERENT person or channel would be a separate, proactive outbound message. To find a participant's number or handle (e.g. to call or text them), look it up with list_members or read_chat — don't say you don't have a contact you can resolve from the conversation's roster.]", origin, convo, roster, scope)
}

// channelObsFrom labels a channel inbound for its cortex report card: the
// sender (the "who") enriched with the CHANNEL name + transport (the "where",
// e.g. "iPhone (iMessage)"), so the standing thread — and any session that
// forks from it — records which channel a message came in on, not just who
// sent it. Falls back to the bare sender when no channel resolves.
func channelObsFrom(in ChannelInbound) string {
	who := chFirst(in.SenderName, in.ConversationName, "someone")
	ch, ok := channelForChat(in.Owner, in.ChatID, in.Handle)
	if !ok {
		return who
	}
	svc := ServiceDisplayName(ch.Service)
	channel := strings.TrimSpace(ch.Name)
	// "where" labels the CHANNEL (the user's binding label, e.g. "iPhone")
	// with the transport in parens — distinct from the sender/conversation.
	var where string
	switch {
	case channel != "" && svc != "":
		where = channel + " (" + svc + ")"
	case channel != "":
		where = channel
	case svc != "":
		where = svc
	}
	if where == "" || strings.EqualFold(strings.TrimSpace(where), strings.TrimSpace(who)) {
		return who
	}
	return who + " · " + where
}

// effectiveChannelSession resolves the session id a channel inbound actually
// runs in. A DEDICATED cortex agent (Cortex on, exactly one channel) runs its
// inbound IN its single standing thread (the channel is just the pipe), so its
// per-contact session id collapses to the cortex session; everyone else keeps
// the per-contact id passed in. The gatekeeper resolves the same id so its
// turn-taking bypass + recent-context read look at the thread the agent
// actually writes, not an empty parallel one.
func (app *OrchestrateApp) effectiveChannelSession(owner, agentID, sessionID string) string {
	if ag, ok := loadAgent(UserDB(app.DB, owner), agentID); ok && ag.Cortex &&
		len(ListChannelsForAgent(RootDB, owner, agentID)) == 1 {
		return cortexSessionID(agentID)
	}
	return sessionID
}

// inboundVideoFrameCount bounds how many frames we sample from an inbound
// channel video (an mp4 in a text). Kept small: enough to convey the clip's
// content to the vision model without ballooning the multimodal payload.
const inboundVideoFrameCount = 4

// registerChannelAgentRunner installs the closure core invokes to run a
// channel's bound agent on one inbound message. Call once at startup.
func registerChannelAgentRunner(app *OrchestrateApp) {
	RegisterChannelAgentRunner(func(ctx context.Context, in ChannelInbound) (ChannelReply, error) {
		// agentOwner == runtimeUser: the channel owner's agent runs under the
		// owner's own store. SessionID is per-contact (stable), so each contact
		// accumulates its own continuing thread under the agent. The rich
		// variant carries the status callback through to the sub-session and
		// returns the agent's produced attachments.
		// Session title = the conversation/room name (what's editable on the
		// transport side); per-message sender = this inbound's author. They
		// coincide for 1:1 but diverge for group rooms. Fall back to the sender
		// when the transport didn't supply a conversation name.
		title := in.ConversationName
		if title == "" {
			title = in.SenderName
		}
		sttOK := GetTranscribeConfig().Enabled // STT on? (canonical flag — matches Transcribe + the admin toggle)
		attachNote := ""                       // notes for non-image attachments (audio transcripts, unsupported types)
		// transcribeAudio runs STT on one audio blob and returns the note to
		// append. It distinguishes the REAL outcomes — disabled vs. a transcription
		// ERROR (endpoint down / 404 / format whisper can't decode) vs. empty (no
		// speech) — instead of reporting every miss as "not configured", which is
		// actively misleading when STT IS on and the endpoint is just failing. The
		// underlying error is logged so a broken endpoint is greppable in gohort.log.
		// nameHint carries a real extension (whisper picks its decoder from it — a
		// bogus ".audio" gets the request rejected).
		transcribeAudio := func(data []byte, nameHint string) string {
			if !sttOK {
				return "\n[Audio attachment received, but speech-to-text isn't enabled — turn it on in Admin → Audio transcription to get a transcript.]"
			}
			// Normalize to 16kHz mono WAV first so STT doesn't depend on the whisper
			// server decoding m4a/AAC/caf (a stock build 400s on those). Fall back to
			// the raw bytes when ffmpeg isn't present or the input is already friendly.
			sendData, sendName := data, nameHint
			if wav, terr := TranscodeAudioToWAV(data); terr == nil && len(wav) > 0 {
				sendData, sendName = wav, "inbound.wav"
			} else if terr != nil {
				Log("[channel] inbound audio transcode failed (%s) — sending raw to STT: %v", nameHint, terr)
			}
			txt, terr := Transcribe(ctx, sendData, sendName)
			if terr != nil {
				Log("[channel] inbound audio transcription failed (%s, %d bytes): %v", nameHint, len(data), terr)
				return "\n[Audio attachment received, but transcription failed (" + terr.Error() + "). STT is enabled; the endpoint or audio format is the problem.]"
			}
			if txt = strings.TrimSpace(txt); txt == "" {
				return "\n[Audio attachment received, but transcription returned no text — the clip may have no speech, or whisper couldn't decode this format.]"
			}
			return "\n[Audio transcript] " + txt
		}
		// Decode inbound photos (base64 on the wire) to the raw bytes the
		// vision model takes; skip any that don't decode rather than failing
		// the whole turn. Connectors sometimes lump ANY attachment (an m4a voice
		// memo, a pdf) into the images field, so sniff each one: only real images
		// go to the vision stream — a non-image is never presented as a picture.
		// A non-image that looks like audio is transcribed; anything else gets a
		// typed note so the agent knows something arrived but can't pretend to see it.
		var images [][]byte
		for _, b64 := range in.Images {
			data, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				continue
			}
			ct := http.DetectContentType(data)
			if strings.HasPrefix(ct, "image/") {
				images = append(images, data)
				continue
			}
			// Not a picture. Audio (and the opaque containers iMessage voice memos
			// decode to — m4a often sniffs as video/mp4 or octet-stream) goes to STT;
			// a genuine other type (pdf, vcard) gets a typed note.
			if strings.HasPrefix(ct, "audio/") || ct == "video/mp4" || ct == "application/octet-stream" {
				attachNote += transcribeAudio(data, audioNameForType(ct))
				continue
			}
			attachNote += fmt.Sprintf("\n[Attachment received (%s) — not an image; it can't be analyzed here. Don't describe it as a photo.]", ct)
		}
		// Dedicated inbound audio (voice memos, m4a/mp3) when the connector sends
		// it on its own field: transcribe so the agent gets the spoken words.
		for _, b64 := range in.Audios {
			data, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				continue
			}
			attachNote += transcribeAudio(data, audioNameForType(http.DetectContentType(data)))
		}
		// Inbound videos: the vision model can't ingest raw mp4, so sample a
		// few frames per clip into the SAME multimodal image stream, and fold a
		// metadata note into the message so the agent treats them as a video
		// (duration/resolution), not loose stills. Best-effort — a clip that
		// won't decode, or a host with no ffmpeg, is skipped silently.
		videoNote := ""
		vidFrames := 0
		for _, b64 := range in.Videos {
			data, derr := base64.StdEncoding.DecodeString(b64)
			if derr != nil {
				continue
			}
			// Visual: sample frames into the multimodal stream. Independent of
			// audio below, so a frame-extract failure doesn't lose the transcript.
			if frames, ferr := ExtractVideoFrames(data, inboundVideoFrameCount); ferr == nil && len(frames) > 0 {
				images = append(images, frames...)
				vidFrames += len(frames)
			}
			if md := strings.TrimSpace(ExtractVideoMetadata(data)); md != "" {
				videoNote += "\n" + md
			}
			// Audio: extract the track and run STT so the agent also gets the
			// spoken words, not just frames. Best-effort — needs a configured
			// STT endpoint, an audio track, and ffmpeg; any miss is skipped.
			if sttOK {
				if audio, aerr := ExtractVideoAudio(data); aerr == nil && len(audio) > 0 {
					if txt, terr := Transcribe(ctx, audio, "inbound.mp3"); terr == nil {
						if txt = strings.TrimSpace(txt); txt != "" {
							videoNote += "\n[Video transcript] " + txt
						}
					}
				}
			}
		}
		if vidFrames > 0 {
			videoNote = fmt.Sprintf("\n[Inbound video: %d frame(s) sampled and attached above as images for you to analyze; the raw video itself can't be inspected.]%s", vidFrames, videoNote)
		}
		// Channel = relay, Cortex = the thread. A DEDICATED cortex agent (a single
		// channel) runs its inbound IN its cortex — the channel is just the pipe
		// into the agent's one standing thread, so the conversation lives where the
		// agent always reads + writes (in AND out unified, no parallel store). A
		// multi-channel cortex agent (the Operator with many contacts) keeps its
		// per-room session so contacts don't merge into one thread — the
		// per-contact-vs-unified choice there is a deferred setting.
		sessionID := app.effectiveChannelSession(in.Owner, in.AgentID, in.SessionID)
		res, err := app.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
			AgentOwner:     in.Owner,
			RuntimeUser:    in.Owner,
			AgentKey:       in.AgentID,
			SubSessionID:   sessionID,
			Title:          title,
			MessageSender:  in.SenderName,
			Message:        in.Text + videoNote + attachNote,
			Images:         images,
			Interactive:    true, // a real person is texting — no delegation marker
			// Replying BACK to this same conversation is in-thread, not a
			// proactive reach-out — so it skips the send approval gate.
			ReplyAuthorizedKey: operatorRecipientKey(in.ChatID, in.Handle),
			// Tell the agent which channel/transport this arrived on (LLM-only,
			// not persisted) so it knows its reply goes straight back here and
			// doesn't confabulate a destination or offer to "send it to" the
			// channel it's already on.
			SurfaceContext: channelSurfaceContext(in),
			StatusCallback: in.StatusCallback,
		})
		if err != nil {
			// A messaging surface must not go silent — the contact texted and
			// expects a reply. Deliver a brief, friendly note instead of nothing;
			// the underlying error is logged (here + in the sync runner) for
			// diagnosis. Returning nil error so the transport actually sends it.
			Log("[channel] agent run failed for owner=%s agent=%s: %v", in.Owner, in.AgentID, err)
			return ChannelReply{Text: "Sorry — I ran into a problem working on that and couldn't finish. Please try again in a moment."}, nil
		}
		// Strip framework-internal markers at the channel boundary — the same
		// safety net phantom applies on its outbox (phantom.go) and the web loop
		// applies on its reply (runner.go). Without it, a leaked delivery marker
		// ([ATTACH: …]) or a <gohort-meta> note rides out verbatim in the text.
		// Attachments for channels travel via res.Images (workspace attach), so
		// stripping the textual marker here doesn't drop a real attachment.
		replyText := StripMetaTags(res.Text)
		// Never deliver silence: if the run produced no text AND no attachment
		// (the model ended a turn with empty content, or its whole output was a
		// stripped marker), send a graceful fallback so the contact gets SOMETHING
		// back instead of the agent appearing to give up.
		if strings.TrimSpace(replyText) == "" && len(res.Images) == 0 && len(res.Videos) == 0 {
			Log("[channel] empty agent reply for owner=%s agent=%s — sending fallback", in.Owner, in.AgentID)
			replyText = "I wasn't able to put together a response to that. Could you rephrase it, or give me a little more detail?"
		}
		// Cortex feed (received → cortex): mirror this inbound into the bound
		// agent's cortex as a non-triggering observation, so the standing thread
		// stays aware of everything coming in over its channels. No-op when the
		// agent has Cortex off. The agent ALSO replied in its per-contact thread
		// (above); this is just awareness, not a second run.
		obs := strings.TrimSpace(in.Text)
		if rt := strings.TrimSpace(replyText); rt != "" {
			obs = strings.TrimSpace(obs + "\n↳ replied: " + truncateObs(rt, 200))
		}
		app.AppendCortexObservation(in.Owner, in.AgentID, channelObsFrom(in), cortexKindMessage, obs)
		return ChannelReply{Text: replyText, Images: res.Images, Videos: res.Videos}, nil
	})
}

// RenameChannelSession retitles a channel room's per-contact session under its
// bound agent. The transport (phantom) calls this when the conversation's
// display name is edited, so the Agency rail and transcript title track the
// name set on the messaging side — the channel's title is owned by the
// transport, not the web rail. No-op if the user/agent/session can't resolve.
func (T *OrchestrateApp) RenameChannelSession(owner, agentID, chatID, name string) {
	if T == nil || T.DB == nil || owner == "" || agentID == "" || chatID == "" {
		return
	}
	udb := UserDB(T.DB, owner)
	if udb == nil {
		return
	}
	renameChatSession(udb, agentID, "chan:"+chatID, name)
}

// audioNameForType picks a filename (with a real extension) to hand the STT
// server for a blob of the given sniffed content type. whisper inspects the
// extension to choose its decoder, so a correct one matters — a bogus
// ".audio" got requests rejected. m4a/aac and the mp4-family containers an
// iMessage voice memo decodes to commonly sniff as video/mp4 or octet-stream,
// so those default to .m4a (whisper.cpp needs ffmpeg to decode that family).
func audioNameForType(ct string) string {
	switch {
	case strings.HasPrefix(ct, "audio/mpeg"):
		return "inbound.mp3"
	case strings.HasPrefix(ct, "audio/wav"), strings.HasPrefix(ct, "audio/x-wav"):
		return "inbound.wav"
	case strings.HasPrefix(ct, "audio/ogg"):
		return "inbound.ogg"
	case strings.HasPrefix(ct, "audio/flac"), strings.HasPrefix(ct, "audio/x-flac"):
		return "inbound.flac"
	default:
		return "inbound.m4a"
	}
}
