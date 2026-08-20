// A limiter that can be walked around is not a limiter. These pin the two
// properties that make it one: the key comes from the TCP peer rather than a
// header a caller controls, and the tracking map has a ceiling of its own so
// it cannot become the exhaustion it exists to prevent.
package core

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiterAllowsUpToTheCeilingThenRefuses(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		if !rl.Allow("caller") {
			t.Fatalf("call %d should have been allowed", i+1)
		}
	}
	if rl.Allow("caller") {
		t.Error("the fourth call should have been refused")
	}
	// A different key has its own budget.
	if !rl.Allow("other") {
		t.Error("one caller's ceiling blocked a different caller")
	}
}

func TestLimiterWindowRolls(t *testing.T) {
	rl := NewRateLimiter(1, 40*time.Millisecond)
	if !rl.Allow("caller") {
		t.Fatal("first call refused")
	}
	if rl.Allow("caller") {
		t.Fatal("second call inside the window should be refused")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.Allow("caller") {
		t.Error("the window did not roll")
	}
}

func TestLimiterDisabledByAZeroCeiling(t *testing.T) {
	// So a call site can be switched off by configuration without growing a
	// branch of its own.
	rl := NewRateLimiter(0, time.Minute)
	for i := 0; i < 50; i++ {
		if !rl.Allow("caller") {
			t.Fatal("a zero ceiling should allow everything")
		}
	}
}

func TestLimiterAllowsAnUnidentifiableCaller(t *testing.T) {
	// A limiter cannot bucket what it cannot name, and refusing would turn
	// "we could not tell who this is" into an outage.
	rl := NewRateLimiter(1, time.Minute)
	for i := 0; i < 5; i++ {
		if !rl.Allow("") {
			t.Fatal("an empty key should not be throttled")
		}
	}
}

func TestLimiterMapIsBounded(t *testing.T) {
	// The map must not grow with attacker-supplied keys. Every distinct source
	// gets one entry; past the cap, new keys are refused rather than stored.
	rl := NewRateLimiter(5, time.Minute)
	rl.maxKeys = 8
	allowed := 0
	for i := 0; i < 200; i++ {
		if rl.Allow(string(rune('a'+i%26)) + string(rune('0'+i/26))) {
			allowed++
		}
	}
	rl.mu.Lock()
	size := len(rl.windows)
	rl.mu.Unlock()
	if size > rl.maxKeys {
		t.Errorf("the tracking map grew past its cap: %d > %d", size, rl.maxKeys)
	}
	if allowed == 0 {
		t.Error("the cap refused everything, which is an outage rather than a limit")
	}
}

func TestRequestSourceIgnoresForwardingHeaders(t *testing.T) {
	// The whole point: honoring X-Forwarded-For would let one caller present a
	// fresh identity per request and never hit any ceiling.
	r := httptest.NewRequest("POST", "/custom/pub/tok/data/x", nil)
	r.RemoteAddr = "198.51.100.7:5555"
	for _, h := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		r.Header.Set(h, "10.0.0.1")
	}
	if got := RequestSource(r); got != "198.51.100.7" {
		t.Errorf("RequestSource honored a caller-supplied header: %q", got)
	}
}

func TestRequestSourceHandlesAnOddRemoteAddr(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "not-a-host-port"
	if got := RequestSource(r); got != "not-a-host-port" {
		t.Errorf("unparseable RemoteAddr should pass through, got %q", got)
	}
	if got := RequestSource(nil); got != "" {
		t.Errorf("a nil request should yield no source, got %q", got)
	}
}

func TestTooManyRequestsTellsTheClientWhenToRetry(t *testing.T) {
	w := httptest.NewRecorder()
	TooManyRequests(w, time.Minute, "slow down")
	if w.Code != 429 {
		t.Errorf("want 429, got %d", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
	// A sub-second wait still has to be expressible.
	w = httptest.NewRecorder()
	TooManyRequests(w, 0, "slow down")
	if got := w.Header().Get("Retry-After"); got != "1" {
		t.Errorf("Retry-After for a zero wait = %q, want 1", got)
	}
}
