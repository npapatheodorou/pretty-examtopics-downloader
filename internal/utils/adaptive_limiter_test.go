package utils

import (
	"testing"
	"time"
)

func TestAdaptiveLimiterThrottleHalvesAndFloors(t *testing.T) {
	a := NewAdaptiveLimiter(4.0 /*start*/, 1.0 /*min*/, 8.0 /*max*/, 0.5 /*step*/, 4)

	a.OnThrottle()
	if got := a.RPS(); got != 2.0 {
		t.Fatalf("after one throttle expected 2.0 rps, got %v", got)
	}
	a.OnThrottle() // 1.0
	a.OnThrottle() // would be 0.5 but floored at min 1.0
	if got := a.RPS(); got != 1.0 {
		t.Fatalf("expected rate floored at min 1.0, got %v", got)
	}
}

func TestAdaptiveLimiterSpeedsUpAfterStreak(t *testing.T) {
	a := NewAdaptiveLimiter(2.0, 0.5, 8.0, 0.5, 3)

	a.OnSuccess()
	a.OnSuccess()
	if got := a.RPS(); got != 2.0 {
		t.Fatalf("rate should not change before streak goal, got %v", got)
	}
	a.OnSuccess() // third success hits streakGoal -> +0.5
	if got := a.RPS(); got != 2.5 {
		t.Fatalf("expected 2.5 rps after reaching streak goal, got %v", got)
	}
}

func TestAdaptiveLimiterSpeedupCapsAtMax(t *testing.T) {
	a := NewAdaptiveLimiter(7.8, 0.5, 8.0, 0.5, 1)
	for i := 0; i < 5; i++ {
		a.OnSuccess()
	}
	if got := a.RPS(); got != 8.0 {
		t.Fatalf("expected rate capped at max 8.0, got %v", got)
	}
}

func TestAdaptiveLimiterThrottleResetsStreak(t *testing.T) {
	a := NewAdaptiveLimiter(2.0, 0.5, 8.0, 0.5, 3)
	a.OnSuccess()
	a.OnSuccess()
	a.OnThrottle() // resets streak, rate -> 1.0
	a.OnSuccess()
	a.OnSuccess()
	if got := a.RPS(); got != 1.0 {
		t.Fatalf("streak should have reset after throttle; expected 1.0, got %v", got)
	}
}

func TestAdaptiveLimiterWaitPacesRequests(t *testing.T) {
	// At 20 rps the spacing is 50ms; three Wait() calls should take ~100ms
	// (first is immediate, next two are spaced).
	a := NewAdaptiveLimiter(20.0, 1.0, 20.0, 0.5, 100)
	start := time.Now()
	a.Wait()
	a.Wait()
	a.Wait()
	elapsed := time.Since(start)
	if elapsed < 80*time.Millisecond {
		t.Fatalf("expected pacing of ~100ms across 3 calls, got %v", elapsed)
	}
}
