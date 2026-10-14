package sandbox

import (
	"bytes"
	"fmt"
	"sync"
)

// outputBuffer collects a command's output. Unbounded it is a bytes.Buffer;
// bounded, it keeps the first part and the last part and counts what fell in
// between. Both ends because a long run's start says what it set out to do
// and its end says how it finished, and the errors are usually at the end.
type outputBuffer struct {
	mu      sync.Mutex
	max     int64
	head    bytes.Buffer
	tail    []byte // a ring of the last tailCap bytes once head is full
	tailCap int
	tailPos int
	tailLen int
	total   int64
}

func newOutputBuffer(max int64) *outputBuffer {
	b := &outputBuffer{max: max}
	if max > 0 {
		b.tailCap = int(max / 4)
		b.tail = make([]byte, b.tailCap)
	}
	return b
}

func (b *outputBuffer) headCap() int64 { return b.max - int64(b.tailCap) }

func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += int64(len(p))
	if b.max <= 0 {
		return b.head.Write(p)
	}
	n := len(p)
	if room := b.headCap() - int64(b.head.Len()); room > 0 {
		take := int64(len(p))
		if take > room {
			take = room
		}
		b.head.Write(p[:take])
		p = p[take:]
	}
	for len(p) > 0 && b.tailCap > 0 {
		c := copy(b.tail[b.tailPos:], p)
		b.tailPos = (b.tailPos + c) % b.tailCap
		if b.tailLen += c; b.tailLen > b.tailCap {
			b.tailLen = b.tailCap
		}
		p = p[c:]
	}
	return n, nil
}

// Len is how much the command wrote, kept or not.
func (b *outputBuffer) Len() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

func (b *outputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tailLen == 0 {
		return b.head.String()
	}
	var tail []byte
	if b.tailLen < b.tailCap {
		tail = b.tail[:b.tailLen]
	} else {
		tail = append(append([]byte(nil), b.tail[b.tailPos:]...), b.tail[:b.tailPos]...)
	}
	dropped := b.total - int64(b.head.Len()) - int64(len(tail))
	if dropped <= 0 {
		return b.head.String() + string(tail)
	}
	return b.head.String() + fmt.Sprintf("\n[... %d bytes of output in the middle were not kept ...]\n", dropped) + string(tail)
}
