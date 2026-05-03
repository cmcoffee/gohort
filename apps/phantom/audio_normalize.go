// Audio normalization for phantom outbound. iMessage handles M4A/AAC
// audio attachments cleanly (renders as audio bubble, survives MMS
// fallback). Other formats — MP3 from ElevenLabs, WAV/OGG from
// scratch tools, etc. — sometimes get rejected by carriers or fail
// to play on the recipient's device. Server-side transcoding to AAC
// before bridge handoff sidesteps all of that.
//
// Real video clips and audio that's already AAC pass through
// unchanged. ffmpeg is the dependency; absent ffmpeg, the function
// logs a warning and returns the originals so the bridge can still
// try to deliver.

package phantom

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// normalizeAudioForDelivery walks a slice of base64-encoded media and
// transcodes any non-AAC audio entries to M4A/AAC. Video entries pass
// through. On any error during transcoding, the original entry is
// returned so delivery still attempts.
func normalizeAudioForDelivery(b64Entries []string) []string {
	if len(b64Entries) == 0 {
		return b64Entries
	}
	out := make([]string, len(b64Entries))
	for i, b64 := range b64Entries {
		out[i] = b64 // default to passthrough
		data, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(data) < 16 {
			continue
		}
		kind := sniffAudioKind(data)
		if kind == "" || kind == "aac" {
			// Not audio (likely video) or already AAC — passthrough.
			continue
		}
		converted, err := transcodeToAAC(data, kind)
		if err != nil {
			Log("[phantom] audio transcode (%s → aac) failed, sending original: %v", kind, err)
			continue
		}
		out[i] = base64.StdEncoding.EncodeToString(converted)
		Log("[phantom] audio normalized %s → aac (%d → %d bytes)", kind, len(data), len(converted))
	}
	return out
}

// sniffAudioKind returns a short tag for known audio formats, or ""
// for anything that isn't recognizably audio (presumed video and left
// alone). "aac" includes both raw ADTS streams and M4A containers.
func sniffAudioKind(data []byte) string {
	if len(data) < 12 {
		return ""
	}
	// MP3: either an ID3 tag at the start ("ID3") or an MPEG sync
	// frame (0xFF 0xFB / 0xFF 0xF3 / 0xFF 0xF2).
	if bytes.HasPrefix(data, []byte("ID3")) {
		return "mp3"
	}
	if data[0] == 0xFF && (data[1] == 0xFB || data[1] == 0xF3 || data[1] == 0xF2) {
		return "mp3"
	}
	// WAV: "RIFF" ... "WAVE".
	if bytes.HasPrefix(data, []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE")) {
		return "wav"
	}
	// OGG: "OggS" magic.
	if bytes.HasPrefix(data, []byte("OggS")) {
		return "ogg"
	}
	// FLAC: "fLaC" magic.
	if bytes.HasPrefix(data, []byte("fLaC")) {
		return "flac"
	}
	// AAC: ADTS sync (0xFF 0xF1 / 0xFF 0xF9) — already in target format.
	if data[0] == 0xFF && (data[1] == 0xF1 || data[1] == 0xF9) {
		return "aac"
	}
	// M4A / MP4 audio: ftyp box at offset 4 with brand M4A or M4B.
	// Note: video MP4 also has ftyp here, so disambiguate by brand.
	if len(data) >= 12 && bytes.Equal(data[4:8], []byte("ftyp")) {
		brand := string(data[8:12])
		switch brand {
		case "M4A ", "M4B ", "mp42", "isom":
			// Inspect a bit further: if the ftyp brand is M4A/M4B it's
			// definitely audio. mp42/isom are ambiguous — could be
			// audio-only or video. Treat as already-AAC and skip
			// transcoding either way: the worst case is we ship a
			// silent video where audio was expected, which is preferable
			// to corrupting working video by re-encoding it.
			if brand == "M4A " || brand == "M4B " {
				return "aac"
			}
			return ""
		}
	}
	return ""
}

// transcodeToAAC writes data to a temp file, runs ffmpeg to produce
// an M4A/AAC version, returns the resulting bytes. fromKind is the
// source extension hint (mp3 / wav / ogg / flac).
func transcodeToAAC(data []byte, fromKind string) ([]byte, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "phantom-audio-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	srcExt := "." + fromKind
	srcPath := filepath.Join(dir, "in"+srcExt)
	dstPath := filepath.Join(dir, "out.m4a")
	if err := os.WriteFile(srcPath, data, 0644); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-i", srcPath,
		"-vn",
		"-c:a", "aac",
		"-b:a", "128k",
		dstPath,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return os.ReadFile(dstPath)
}
