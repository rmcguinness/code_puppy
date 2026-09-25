package breaker

import (
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestOpensBacksOffAndRecovers(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	b := New(2)
	b.SetClock(clk.now)

	if ok, _ := b.Allow(); !ok {
		t.Fatal("new breaker refused a call")
	}
	if _, opened, _ := b.Failure(); opened {
		t.Fatal("opened after one failure")
	}
	_, opened, cd := b.Failure()
	if !opened || cd != InitialCooldown {
		t.Fatalf("second failure: opened=%v cooldown=%v", opened, cd)
	}
	if ok, retryIn := b.Allow(); ok || retryIn != InitialCooldown {
		t.Fatalf("open breaker: ok=%v retryIn=%v", ok, retryIn)
	}

	clk.advance(InitialCooldown)
	if ok, _ := b.Allow(); !ok {
		t.Fatal("no trial after the cooldown")
	}
	if ok, _ := b.Allow(); ok {
		t.Fatal("a second caller got through during the trial")
	}
	if _, opened, cd := b.Failure(); !opened || cd != 2*InitialCooldown {
		t.Fatalf("failed trial: opened=%v cooldown=%v", opened, cd)
	}

	for range 10 { // back-off is capped
		clk.advance(MaxCooldown)
		b.Allow()
		b.Failure()
	}
	if b.cooldown != MaxCooldown {
		t.Fatalf("cooldown = %v, want cap %v", b.cooldown, MaxCooldown)
	}

	clk.advance(MaxCooldown)
	b.Allow()
	if !b.Success() {
		t.Fatal("successful trial did not report recovery")
	}
	if ok, _ := b.Allow(); !ok || b.Success() {
		t.Fatal("breaker not closed after recovery")
	}
}

func TestThresholdOfOneOpensOnFirstFailure(t *testing.T) {
	b := New(0) // clamped to 1
	if _, opened, _ := b.Failure(); !opened {
		t.Fatal("threshold 1 did not open on the first failure")
	}
}
