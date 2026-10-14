package sandbox

import (
	"strings"
	"testing"
)

// Bounded, the buffer keeps the start and the end of a long output and says
// how much of the middle it dropped; unbounded, and under the bound, it keeps
// everything as it was written.
func TestOutputBufferKeepsBothEnds(t *testing.T) {
	b := newOutputBuffer(0)
	b.Write([]byte("all of it"))
	if b.String() != "all of it" {
		t.Errorf("unbounded: %q", b.String())
	}

	b = newOutputBuffer(100) // 75 head, 25 tail
	b.Write([]byte(strings.Repeat("a", 50)))
	b.Write([]byte(strings.Repeat("b", 40)))
	if got := b.String(); got != strings.Repeat("a", 50)+strings.Repeat("b", 40) {
		t.Errorf("under the bound: %q", got)
	}

	b = newOutputBuffer(100)
	b.Write([]byte("START" + strings.Repeat("x", 1000)))
	for i := 0; i < 50; i++ {
		b.Write([]byte("y"))
	}
	b.Write([]byte("...END"))
	got := b.String()
	if !strings.HasPrefix(got, "START") || !strings.HasSuffix(got, "...END") {
		t.Errorf("the ends were not kept: %q", got)
	}
	if !strings.Contains(got, "bytes of output in the middle were not kept") {
		t.Errorf("no note of what was dropped: %q", got)
	}
	if b.Len() != int64(5+1000+50+6) {
		t.Errorf("Len = %d, want everything written counted", b.Len())
	}
	if len(got) > 200 {
		t.Errorf("kept %d bytes under a 100-byte bound", len(got))
	}
}
