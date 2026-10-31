package flows

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// A service's command runs as a flow's steps do: a file it is given by name,
// a step's result as the next one's {in}, the last one's {out} as the
// result. A text value standing alone as a word cannot pose as an option.
func TestACommandRunsLikeAFlow(t *testing.T) {
	needSandbox(t)
	img := pngBytes(t)
	res, err := RunCommand(context.Background(), Command{
		Steps:   []string{"cp {image} {out.png}"},
		Files:   map[string]CommandFile{"image": {Data: img, Ext: ".png"}},
		Timeout: 20 * time.Second,
	})
	if err != nil || !bytes.Equal(res.Out, img) {
		t.Fatalf("copy: %v (%d bytes)", err, len(res.Out))
	}
	res, err = RunCommand(context.Background(), Command{
		Steps:   []string{"printf %s {data}", "base64 -d {in}"},
		Values:  map[string]string{"data": base64.StdEncoding.EncodeToString(img)},
		OutExt:  ".png",
		Timeout: 20 * time.Second,
	})
	if err != nil || !bytes.Equal(res.Out, img) {
		t.Fatalf("two steps through stdout: %v (%d bytes)", err, len(res.Out))
	}
	_, err = RunCommand(context.Background(), Command{
		Steps:  []string{"printf %s {prompt}"},
		Values: map[string]string{"prompt": "--output=/etc/passwd"},
	})
	if err == nil || !strings.Contains(err.Error(), "as an option") {
		t.Errorf("a value posing as an option: %v", err)
	}
	// A value inside a word is the word's text, quoted, never a shell's.
	res, err = RunCommand(context.Background(), Command{
		Steps:  []string{"printf %s x{prompt}"},
		Values: map[string]string{"prompt": "; rm -rf . $(id)"},
	})
	if err != nil || string(res.Out) != "x; rm -rf . $(id)" {
		t.Errorf("a value with shell syntax: %q %v", res.Out, err)
	}
	if _, err := RunCommand(context.Background(), Command{Steps: []string{"no-such-program-here x"}}); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("a missing program: %v", err)
	}
}
