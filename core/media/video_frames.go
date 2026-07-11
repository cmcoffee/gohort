package media

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/cmcoffee/snugforge/nfo"
)

// ExtractVideoFrames is the exported variant of extractVideoFrames; same
// semantics. Lets non-core packages (chat tools that download or transcode
// video) reuse the frame sampler without re-implementing the ffmpeg dance.
func ExtractVideoFrames(data []byte, count int) ([][]byte, error) {
	return extractVideoFrames(data, count)
}

// ExtractVideoMetadata is the exported variant of extractVideoMetadata;
// returns the formatted [video_context] block for arbitrary video bytes
// so non-core packages (downloader tools) can produce the same metadata
// surface the LLM sees for user-uploaded clips.
func ExtractVideoMetadata(data []byte) string {
	return extractVideoMetadata(data)
}

// extractVideoFrames samples N frames evenly distributed across the video
// at `path`, returning each as JPEG bytes. The first frame is sampled at
// ~1% into the clip (skipping any black opening frame), the last at ~99%
// (avoiding fade-outs). Frames in between are evenly spaced.
//
// Frames are extracted via N independent ffmpeg invocations with -ss seek
// — slightly slower than a single filter-graph pass, but each call is
// independent so parallelization is trivial later, and seek+grab gives
// the cleanest still output. ffmpeg is invoked with -frames:v 1 -q:v 2
// (mjpeg encoder, high quality).
//
// Returns ([]frame, nil) on success. If ffmpeg is missing, returns
// (nil, error) — caller can degrade to metadata-only video handling.
func extractVideoFrames(data []byte, count int) ([][]byte, error) {
	if count <= 0 {
		count = videoFrameSampleCount
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no video data")
	}
	srcPath, err := writeTempFile(data, "*.mp4")
	if err != nil {
		return nil, fmt.Errorf("tempfile: %w", err)
	}
	defer os.Remove(srcPath)

	probe, err := runFfprobe(srcPath)
	if err != nil {
		return nil, fmt.Errorf("ffprobe: %w", err)
	}
	dur, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || dur <= 0 {
		return nil, fmt.Errorf("invalid duration %q", probe.Format.Duration)
	}

	// Compute timestamps. For very short clips (< count seconds), the
	// sampler still produces N frames but they'll be near-duplicates —
	// acceptable, the LLM ignores redundancy.
	timestamps := make([]float64, 0, count)
	if count == 1 {
		timestamps = append(timestamps, dur*0.5)
	} else {
		// First/last set ~1%/~99% to dodge black-frame intros/outros.
		firstFrac := 0.01
		lastFrac := 0.99
		span := lastFrac - firstFrac
		for i := 0; i < count; i++ {
			frac := firstFrac + span*float64(i)/float64(count-1)
			timestamps = append(timestamps, dur*frac)
		}
	}

	tmpDir, err := os.MkdirTemp("", "gohort-frames-*")
	if err != nil {
		return nil, fmt.Errorf("frame dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	type indexed struct {
		idx int
		buf []byte
	}
	var frames []indexed
	for i, ts := range timestamps {
		out := filepath.Join(tmpDir, fmt.Sprintf("frame_%03d.jpg", i))
		// -ss before -i is faster (input seek) but less accurate on some
		// codecs; -ss after -i is frame-accurate. We want quality over
		// latency for vision input, so use the accurate path.
		cmd := exec.Command("ffmpeg",
			"-loglevel", "error",
			"-y",
			"-i", srcPath,
			"-ss", strconv.FormatFloat(ts, 'f', 3, 64),
			"-frames:v", "1",
			"-q:v", "2",
			out,
		)
		if err := cmd.Run(); err != nil {
			nfo.Debug("[video] frame %d at %.2fs failed: %v", i, ts, err)
			continue
		}
		buf, err := os.ReadFile(out)
		if err != nil || len(buf) == 0 {
			continue
		}
		frames = append(frames, indexed{idx: i, buf: buf})
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("no frames extracted")
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].idx < frames[j].idx })
	out := make([][]byte, len(frames))
	for i, f := range frames {
		out[i] = f.buf
	}
	return out, nil
}

// ExtractVideoAudio pulls the audio stream out of a video and returns
// it as MP3 bytes (compact + universally supported by STT endpoints).
// Returns (nil, error) when:
//   - the video has no audio track (silent clip, GIF-style)
//   - ffmpeg is missing
//   - the audio stream is corrupt
//
// All failures are non-fatal at the caller's level — videodl falls
// back to frames-only transcription.
//
// MP3 chosen over WAV because: most STT services accept it (OpenAI
// /audio/transcriptions, whisper.cpp server), the file is 10-20× smaller
// for typical speech, and ffmpeg's libmp3lame encoder is fast.
func ExtractVideoAudio(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no video data")
	}
	srcPath, err := writeTempFile(data, "*.mp4")
	if err != nil {
		return nil, fmt.Errorf("tempfile: %w", err)
	}
	defer os.Remove(srcPath)

	outPath := srcPath + ".mp3"
	defer os.Remove(outPath)
	// -vn skips video; -ac 1 forces mono (smaller, fine for STT);
	// -ar 16000 matches Whisper's expected sample rate (it resamples
	// to 16k internally anyway, so sending 16k saves bytes).
	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-y",
		"-i", srcPath,
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-codec:a", "libmp3lame",
		"-q:a", "5",
		outPath,
	)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg audio extract: %w", err)
	}
	audio, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read audio: %w", err)
	}
	if len(audio) == 0 {
		return nil, fmt.Errorf("audio extract produced 0 bytes (no audio track?)")
	}
	return audio, nil
}

// TranscodeAudioToWAV converts arbitrary input audio (m4a/aac/caf/ogg/amr/…)
// to 16 kHz mono 16-bit PCM WAV — the one format whisper.cpp decodes NATIVELY,
// without needing its own ffmpeg/codec build. Inbound voice memos arrive as m4a
// (AAC in an mp4 container); a stock whisper server rejects those with a 400.
// Normalizing here, on the gohort host (which already has ffmpeg for video),
// makes transcription independent of the STT server's format support. ffmpeg
// probes the container, so the input's extension is irrelevant.
func TranscodeAudioToWAV(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no audio data")
	}
	srcPath, err := writeTempFile(data, "*.audio")
	if err != nil {
		return nil, fmt.Errorf("tempfile: %w", err)
	}
	defer os.Remove(srcPath)

	outPath := srcPath + ".wav"
	defer os.Remove(outPath)
	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-y",
		"-i", srcPath,
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		outPath,
	)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg audio transcode: %w", err)
	}
	wav, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("read wav: %w", err)
	}
	if len(wav) == 0 {
		return nil, fmt.Errorf("transcode produced 0 bytes (no audio track?)")
	}
	return wav, nil
}

// ExtractVideosFrames is the multi-video helper used by the vision pipeline.
// Returns frames concatenated in order of the input videos. Failures on
// any single video are logged and skipped — the rest still flow through.
func ExtractVideosFrames(videos [][]byte, perVideo int) [][]byte {
	if len(videos) == 0 {
		return nil
	}
	var all [][]byte
	for i, v := range videos {
		frames, err := extractVideoFrames(v, perVideo)
		if err != nil {
			nfo.Debug("[video] frame extract %d failed: %v", i, err)
			continue
		}
		all = append(all, frames...)
	}
	return all
}
