package admin

// Testing a PEERED transcription config.
//
// The peer base serves exactly one path — /audio/transcriptions — so the
// /models-then-root GET probe the local test uses is wrong twice over here:
// /models 404s, and the root fallback lands on the far side's web UI and
// reports a cheerful success for a link that cannot transcribe a thing.
//
// The manifest answers the questions that actually matter in one call — is it
// reachable, is the key still good, is transcribe granted, does that instance
// still have STT of its own — and each failure names its own remedy, which a
// status code from the wrong URL never could.

import (
	"context"

	. "github.com/cmcoffee/gohort/core"
)

// peerTranscribeTestResult probes a peer for the Test button. Returns the
// (ok, message, error) triple writeTestResult takes.
func peerTranscribeTestResult(ctx context.Context, p RemotePeer) (bool, string, string) {
	m, err := ProbeRemotePeer(ctx, p.BaseURL, p.Key)
	if err != nil {
		return false, "", err.Error()
	}
	granted := false
	for _, e := range m.Capabilities {
		if e.Name == PeerCapTranscribe && e.Served && e.Granted {
			granted = true
		}
	}
	if !granted {
		return false, "", "reached " + p.BaseURL +
			", but it does not offer transcription to this key — grant \"transcribe\" on that instance (Resource Sharing › Shared With › Grants) and Refresh the peer here"
	}
	if m.Transcribe == nil {
		return false, "", "reached " + p.BaseURL +
			" and the grant is in place, but that instance has no transcription endpoint configured of its own"
	}
	msg := "Peer " + p.Name + " reachable, transcribe granted"
	if m.Transcribe.Model != "" {
		msg += " (" + m.Transcribe.Model + ")"
	}
	return true, msg, ""
}
