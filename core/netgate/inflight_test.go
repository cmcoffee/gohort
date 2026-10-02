package netgate

import "testing"

func TestInFlightBoundsAKeyAndGivesSlotsBack(t *testing.T) {
	f := NewInFlight(2)
	r1, ok1 := f.Acquire("u")
	_, ok2 := f.Acquire("u")
	if !ok1 || !ok2 {
		t.Fatal("slots under the limit were refused")
	}
	if _, ok := f.Acquire("u"); ok {
		t.Error("a third concurrent request was admitted")
	}
	if _, ok := f.Acquire("v"); !ok {
		t.Error("another key was refused")
	}
	r1()
	r1() // a second release must not free two slots
	if _, ok := f.Acquire("u"); !ok {
		t.Error("a released slot was not given back")
	}
	if _, ok := f.Acquire("u"); ok {
		t.Error("a double release freed an extra slot")
	}
}
