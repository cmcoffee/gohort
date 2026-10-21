package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cmcoffee/snugforge/nfo"
)

// ExtractVideoFrames samples count frames evenly across a video, as JPEG:
// by the frame sampling job (FramesHook, core/flows) when it is loaded, else
// by SampleFrames here. Lets non-core packages (chat tools that download or
// transcode video) reuse the frame sampler without re-implementing it.
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

func extractVideoFrames(data []byte, count int) ([][]byte, error) {
	if count <= 0 {
		count = videoFrameSampleCount
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("no video data")
	}
	if FramesHook != nil {
		return FramesHook(data, count)
	}
	var frames [][]byte
	err := withLocalTools(func(t Tools) error {
		var err error
		frames, err = SampleFrames(context.Background(), t, data, count)
		return err
	})
	return frames, err
}

// SampleFrames samples count frames evenly distributed across the video,
// with t, returning each as JPEG bytes. The first frame is sampled at ~1%
// into the clip (skipping any black opening frame), the last at ~99%
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
func SampleFrames(ctx context.Context, t Tools, data []byte, count int) ([][]byte, error) {
	if count <= 0 {
		count = videoFrameSampleCount
	}
	if err := writeInput(t, data); err != nil {
		return nil, err
	}
	probe, err := probeWith(ctx, t, mediaInput)
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

	type indexed struct {
		idx int
		buf []byte
	}
	var frames []indexed
	for i, ts := range timestamps {
		out := fmt.Sprintf("frame_%03d.jpg", i)
		// -ss before -i is faster (input seek) but less accurate on some
		// codecs; -ss after -i is frame-accurate. We want quality over
		// latency for vision input, so use the accurate path.
		_, err := t.Run(ctx, ffFrameLimit, "ffmpeg",
			"-loglevel", "error",
			"-y",
			"-protocol_whitelist", "file,pipe",
			"-i", mediaInput,
			"-ss", strconv.FormatFloat(ts, 'f', 3, 64),
			"-frames:v", "1",
			"-q:v", "2",
			out,
		)
		if err != nil {
			nfo.Debug("[video] frame %d at %.2fs failed: %v", i, ts, err)
			continue
		}
		buf, err := os.ReadFile(filepath.Join(t.Dir(), out))
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
// it as MP3 bytes (compact + universally supported by STT endpoints): by
// the sound-from-video job (AudioHook, core/flows) when it is loaded, else
// by AudioTrack here. Returns (nil, error) when:
//   - the video has no audio track (silent clip, GIF-style)
//   - ffmpeg is missing
//   - the audio stream is corrupt
//
// All failures are non-fatal at the caller's level — videodl falls
// back to frames-only transcription.
func ExtractVideoAudio(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no video data")
	}
	if AudioHook != nil {
		return AudioHook(data)
	}
	var audio []byte
	err := withLocalTools(func(t Tools) error {
		var err error
		audio, err = AudioTrack(context.Background(), t, data)
		return err
	})
	return audio, err
}

// AudioTrack extracts a video's sound with t, as MP3.
//
// MP3 chosen over WAV because: most STT services accept it (OpenAI
// /audio/transcriptions, whisper.cpp server), the file is 10-20× smaller
// for typical speech, and ffmpeg's libmp3lame encoder is fast.
func AudioTrack(ctx context.Context, t Tools, data []byte) ([]byte, error) {
	if err := writeInput(t, data); err != nil {
		return nil, err
	}
	if err := checkMediaFile(filepath.Join(t.Dir(), mediaInput)); err != nil {
		return nil, err
	}
	// -vn skips video; -ac 1 forces mono (smaller, fine for STT);
	// -ar 16000 matches Whisper's expected sample rate (it resamples
	// to 16k internally anyway, so sending 16k saves bytes).
	if _, err := t.Run(ctx, ffAudioLimit, "ffmpeg",
		"-loglevel", "error",
		"-y",
		"-protocol_whitelist", "file,pipe",
		"-i", mediaInput,
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-codec:a", "libmp3lame",
		"-q:a", "5",
		"-fs", ffAudioMaxBytes,
		"audio.mp3",
	); err != nil {
		return nil, fmt.Errorf("ffmpeg audio extract: %w", err)
	}
	audio, err := os.ReadFile(filepath.Join(t.Dir(), "audio.mp3"))
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
// without needing its own ffmpeg/codec build: by the audio preparation job
// (WAVHook, core/flows) when it is loaded, else by ToWAV here. Inbound voice
// memos arrive as m4a (AAC in an mp4 container); a stock whisper server
// rejects those with a 400. Normalizing here, on the gohort host (which
// already has ffmpeg for video), makes transcription independent of the STT
// server's format support. ffmpeg probes the container, so the input's
// extension is irrelevant.
func TranscodeAudioToWAV(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("no audio data")
	}
	if WAVHook != nil {
		return WAVHook(data)
	}
	var wav []byte
	err := withLocalTools(func(t Tools) error {
		var err error
		wav, err = ToWAV(context.Background(), t, data)
		return err
	})
	return wav, err
}

// ToWAV converts audio to 16 kHz mono WAV with t.
func ToWAV(ctx context.Context, t Tools, data []byte) ([]byte, error) {
	if err := writeInput(t, data); err != nil {
		return nil, err
	}
	if err := checkMediaFile(filepath.Join(t.Dir(), mediaInput)); err != nil {
		return nil, err
	}
	if _, err := t.Run(ctx, ffAudioLimit, "ffmpeg",
		"-loglevel", "error",
		"-y",
		"-protocol_whitelist", "file,pipe",
		"-i", mediaInput,
		"-vn",
		"-ac", "1",
		"-ar", "16000",
		"-c:a", "pcm_s16le",
		"-fs", ffAudioMaxBytes,
		"audio.wav",
	); err != nil {
		return nil, fmt.Errorf("ffmpeg audio transcode: %w", err)
	}
	wav, err := os.ReadFile(filepath.Join(t.Dir(), "audio.wav"))
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

// Bounds on every ffmpeg / ffprobe run. The input is whatever somebody sent
// (an upload, a channel message's attachment), and a crafted file can make
// ffmpeg spin forever; without a deadline each one held a worker and a
// process until the server restarted.
const (
	ffFrameLimit    = 60 * time.Second
	ffProbeLimit    = 60 * time.Second
	ffAudioLimit    = 5 * time.Minute
	ffAudioMaxBytes = "104857600" // -fs: 100 MB of extracted audio
)

// ffmpegCommand is exec.Command with a deadline. The caller runs cmd and then
// calls cancel.
func ffmpegCommand(limit time.Duration, bin string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	return cmd, cancel
}

// checkMediaFile refuses an input that is a playlist or concat list rather
// than media. ffmpeg picks its demuxer from the content, and an HLS playlist
// or ffconcat file names OTHER files to read, local paths included, which
// then come back as frames or audio. -protocol_whitelist keeps it off the
// network; this keeps it off the rest of the disk.
func checkMediaFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 64)
	n, _ := f.Read(head)
	h := strings.ToLower(strings.TrimLeft(string(head[:n]), "\ufeff \t\r\n"))
	for _, prefix := range []string{"#extm3u", "ffconcat", "#ext", "file ", "file'"} {
		if strings.HasPrefix(h, prefix) {
			return fmt.Errorf("refusing a playlist or concat list: send the media file itself")
		}
	}
	return nil
}
