package videodl

import (
	"context"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// transcribeVideoAudio extracts the audio track from a downloaded video and runs
// it through STT, returning a labeled transcript block ready to append to a tool
// result. Returns "" when STT is disabled, the clip has no audio track / no
// ffmpeg, or transcription fails — and logs the miss so it isn't silent. That
// silent-skip is exactly why a missing transcript used to be invisible: the
// agent viewed frames but never the spoken words, unless it happened to call the
// separate transcribe tool.
func transcribeVideoAudio(data []byte, label string) string {
	if !GetTranscribeConfig().Enabled {
		return ""
	}
	audio, err := ExtractVideoAudio(data)
	if err != nil || len(audio) == 0 {
		if err != nil {
			Log("[videodl] %s: audio extract for transcript failed (no audio track or no ffmpeg): %v", label, err)
		}
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	txt, err := Transcribe(ctx, audio, "inbound.mp3")
	if err != nil {
		Log("[videodl] %s: transcription failed: %v", label, err)
		return ""
	}
	if txt = strings.TrimSpace(txt); txt == "" {
		return ""
	}
	return "\n[Transcript of the spoken audio]\n" + txt + "\n"
}
