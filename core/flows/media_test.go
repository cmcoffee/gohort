package flows

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/media"
)

// sampleVideo is a two-second 64x64 test pattern with a tone, made by
// ffmpeg itself (outside any sandbox: this is the test's input).
func sampleVideo(t *testing.T) []byte {
	t.Helper()
	if !Installed("ffmpeg") || !Installed("ffprobe") {
		t.Skip("ffmpeg is not installed here")
	}
	out := filepath.Join(t.TempDir(), "v.mp4")
	cmd := exec.Command("ffmpeg", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=2:size=64x64:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-shortest", "-pix_fmt", "yuv420p", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not make a sample video: %v %s", err, b)
	}
	b, _ := os.ReadFile(out)
	return b
}

// The media functions run as jobs, their native flows in the sandbox:
// frames, details, sound and WAV, as the code always made them.
func TestTheMediaFunctionsRunAsJobs(t *testing.T) {
	needSandbox(t)
	video := sampleVideo(t)
	if got := Sniff(video); got != "video/mp4" {
		t.Errorf("a video should be sniffed as MP4: %s", got)
	}
	// Two of the three on a two-second clip, as the sampler always gave:
	// its last stamp (99% in) falls past the clip's last frame.
	frames, err := media.ExtractVideoFrames(video, 3)
	if err != nil || len(frames) < 2 || !Readable(frames[0], KindImage) {
		t.Fatalf("want frames the model can read: %v %d", err, len(frames))
	}
	if meta := media.ExtractVideoMetadata(video); !strings.HasPrefix(meta, "[video_context]") || !strings.Contains(meta, "64x64") {
		t.Errorf("want the video's details: %q", meta)
	}
	mp3, err := media.ExtractVideoAudio(video)
	if err != nil || Sniff(mp3) != "audio/mpeg" {
		t.Fatalf("want the sound as MP3: %v %s", err, Sniff(mp3))
	}
	wav, err := media.TranscodeAudioToWAV(mp3)
	if err != nil || Sniff(wav) != "audio/wave" {
		t.Fatalf("want it as WAV: %v %s", err, Sniff(wav))
	}
}

// A Box runs its tools in the sandbox, with no network unless the job has
// it.
func TestABoxHasNoNetwork(t *testing.T) {
	needSandbox(t)
	dir, err := runDir()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	out, err := (&Box{dir: dir}).Run(context.Background(), 10e9, "cat", "/proc/net/dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if name, _, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && !strings.Contains(name, "|") && name != "lo" {
			t.Errorf("a box without the network sees %q", name)
		}
	}
}

// Frame sampling adapted to another command: {count} frames into {outdir}.
func TestFrameSamplingCanBeAdapted(t *testing.T) {
	needSandbox(t)
	video := sampleVideo(t)
	// One more than asked for, so only the adapted flow could have given it.
	a := Adapted{Job: "media.frames", Prefer: true, Steps: []string{"sh -c 'ffmpeg -loglevel error -i \"$0\" -vf fps=2 -frames:v $(($1 + 1)) \"$2\"/f%02d.jpg' {in} {count} {outdir}"}}
	if err := SetAdapted("frames-fps", a); err != nil {
		t.Fatal(err)
	}
	defer RemoveAdapted("frames-fps")
	frames, err := media.ExtractVideoFrames(video, 2)
	if err != nil || len(frames) != 3 {
		t.Fatalf("want 3 frames from the adapted flow, asked for 2: %v %d", err, len(frames))
	}
	res, _ := Do(context.Background(), "media.frames", Input{Data: video, From: "video/mp4", Params: map[string]string{"count": "2"}})
	if res.By != "frames-fps" {
		t.Errorf("the preferred flow should have done it: %q", res.By)
	}
	// Details adapted to a raw tool are prefaced as the native ones are.
	SetAdapted("probe-raw", Adapted{Job: "media.metadata", Prefer: true, Command: "ffprobe -v error -show_format {in}"})
	defer RemoveAdapted("probe-raw")
	if meta := media.ExtractVideoMetadata(video); !strings.HasPrefix(meta, "[video_context]\n") || !strings.Contains(meta, "format_name") {
		t.Errorf("adapted details should read as the video's: %q", meta)
	}
}

// Transcription: with no speech service, it says what to set up; adapted to
// a command, that command transcribes; with a service, the service does.
func TestTranscriptionIsAServiceOrAnAdaptedCommand(t *testing.T) {
	needSandbox(t)
	was := media.GetTranscribeConfig()
	defer media.SetTranscribeConfig(was)
	media.SetTranscribeConfig(media.TranscribeConfig{})
	audio := append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), bytes.Repeat([]byte{0}, 64)...)
	if _, err := media.Transcribe(context.Background(), audio, "memo.mp3"); err == nil || !strings.Contains(err.Error(), "speech-to-text") {
		t.Fatalf("with no service, say what to set up: %v", err)
	}
	SetAdapted("whisper", Adapted{Job: "media.transcribe", Command: "sh -c 'echo hello from the command' {in}"})
	defer RemoveAdapted("whisper")
	if text, err := media.Transcribe(context.Background(), audio, "memo.mp3"); err != nil || strings.TrimSpace(text) != "hello from the command" {
		t.Fatalf("the adapted command should transcribe it: %v %q", err, text)
	}
	RemoveAdapted("whisper")

	var gotName string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f, h, err := r.FormFile("file"); err == nil {
			io.Copy(io.Discard, f)
			gotName = h.Filename
		}
		io.WriteString(w, "hello from the service")
	}))
	defer srv.Close()
	media.SetTranscribeConfig(media.TranscribeConfig{Enabled: true, Endpoint: srv.URL})
	if text, err := media.Transcribe(context.Background(), audio, "memo.mp3"); err != nil || strings.TrimSpace(text) != "hello from the service" || gotName != "memo.mp3" {
		t.Errorf("the service should transcribe it, under its name: %v %q %q", err, text, gotName)
	}
}

func TestAudioIsChecked(t *testing.T) {
	for want, data := range map[string][]byte{
		"audio/mpeg": append([]byte("ID3\x04"), bytes.Repeat([]byte{0}, 32)...),
		"audio/flac": []byte("fLaC\x00\x00"),
		"audio/aac":  {0xFF, 0xF1, 0x50, 0x80},
		"audio/mp4":  append([]byte{0, 0, 0, 24}, []byte("ftypM4A \x00\x00\x00\x00isom")...),
	} {
		if got := Sniff(data); got != want {
			t.Errorf("want %s, got %s", want, got)
		}
		if _, err := KindAudio.accept(data); err != nil {
			t.Errorf("%s should be audio a speech service reads: %v", want, err)
		}
	}
	if _, err := KindAudio.accept([]byte("not audio")); err == nil {
		t.Error("text is not audio")
	}
}

// A video's sound and audio for transcription run as their jobs: adapted
// flows the admin preferred give what only they would (Ogg, 8 kHz).
func TestSoundAndWAVRunAsTheirJobs(t *testing.T) {
	needSandbox(t)
	video := sampleVideo(t)
	SetAdapted("sound-ogg", Adapted{Job: "media.audio", Prefer: true, Command: "ffmpeg -loglevel error -i {in} -vn -c:a libvorbis {out.ogg}"})
	defer RemoveAdapted("sound-ogg")
	sound, err := media.ExtractVideoAudio(video)
	if err != nil || Sniff(sound) != "application/ogg" {
		t.Fatalf("want the adapted flow's Ogg: %v %s", err, Sniff(sound))
	}
	SetAdapted("wav-8k", Adapted{Job: "media.wav", Prefer: true, Command: "ffmpeg -loglevel error -i {in} -ar 8000 -ac 1 {out.wav}"})
	defer RemoveAdapted("wav-8k")
	wav, err := media.TranscodeAudioToWAV(sound)
	if err != nil || len(wav) < 28 || Sniff(wav) != "audio/wave" {
		t.Fatalf("want a WAV: %v", err)
	}
	if rate := int(wav[24]) | int(wav[25])<<8 | int(wav[26])<<16; rate != 8000 {
		t.Errorf("want the adapted flow's 8 kHz, got %d", rate)
	}
}

// A video download: the link is checked before any flow runs, the native
// flow (yt-dlp, in the sandbox with the network) fetches it, and an
// adapted downloader can do it instead.
func TestAVideoIsDownloadedFromALink(t *testing.T) {
	needSandbox(t)
	video := sampleVideo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(video)
	}))
	defer srv.Close()
	link := srv.URL + "/clip.mp4"

	was := URLGuard
	defer func() { URLGuard = was }()
	URLGuard = nil
	if _, err := Do(context.Background(), "media.download", Input{Params: map[string]string{"url": link}}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("with no guard, every link should be refused: %v", err)
	}
	// The test's guard: the test server only, never anything else.
	URLGuard = func(v string) error {
		if !strings.HasPrefix(v, srv.URL) {
			return fmt.Errorf("not the test server")
		}
		return nil
	}
	if _, err := Do(context.Background(), "media.download", Input{Params: map[string]string{"url": "--exec=touch x"}}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a link the guard refuses should not reach a flow: %v", err)
	}
	if !Installed("yt-dlp") {
		t.Skip("yt-dlp is not installed here")
	}
	res, err := Do(context.Background(), "media.download", Input{Params: map[string]string{"url": link}})
	if err != nil || !bytes.Equal(res.Out, video) || res.By != "yt-dlp" {
		t.Fatalf("want the video by yt-dlp: %v %q (%s)", err, res.By, res.Log)
	}

	// Adapted to curl, preferred: it fetches it, and the guard still holds.
	SetAdapted("curl-dl", Adapted{Job: "media.download", Prefer: true, Command: "curl -sS -o {out.mp4} {url}"})
	defer RemoveAdapted("curl-dl")
	res, err = Do(context.Background(), "media.download", Input{Params: map[string]string{"url": link}})
	if err != nil || res.By != "curl-dl" || !bytes.Equal(res.Out, video) {
		t.Fatalf("want the video by the adapted flow: %v %q", err, res.By)
	}
	if _, err := Do(context.Background(), "media.download", Input{Params: map[string]string{"url": "http://elsewhere.invalid/x"}}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("an adapted flow should get only links the guard allows: %v", err)
	}
	// What comes back has to be a video.
	SetAdapted("curl-dl", Adapted{Job: "media.download", Prefer: true, Command: "sh -c 'echo not a video' {url}"})
	if res, err := Do(context.Background(), "media.download", Input{Params: map[string]string{"url": link}}); err == nil && res.By == "curl-dl" {
		t.Error("a result that is not a video should not be used")
	}
}
