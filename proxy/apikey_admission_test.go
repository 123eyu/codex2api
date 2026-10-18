package proxy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIKeyAdmissionLimiterRejectsBurst(t *testing.T) {
	limiter := newAPIKeyAdmissionLimiter()
	now := time.Unix(1_700_000_000, 0)

	for i := 0; i < 2; i++ {
		if !limiter.allow(1, 2, 0, 1, now) {
			t.Fatalf("request %d within RPM rejected", i+1)
		}
	}
	if limiter.allow(1, 2, 0, 1, now) {
		t.Fatal("third request in the same second should exceed RPM=2")
	}
	if !limiter.allow(2, 2, 0, 1, now) {
		t.Fatal("other API key should not be affected")
	}
	if !limiter.allow(1, 2, 0, 1, now.Add(time.Minute)) {
		t.Fatal("request should be admitted after the RPM window rolls")
	}
}

func TestAPIKeyAdmissionLimiterConcurrentBurst(t *testing.T) {
	limiter := newAPIKeyAdmissionLimiter()
	now := time.Unix(1_700_000_000, 0)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.allow(1, 5, 0, 1, now) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 5 {
		t.Fatalf("allowed = %d, want exactly 5 (RPM limit)", got)
	}
}

func TestAPIKeyAdmissionLimiterReservesBatchRequests(t *testing.T) {
	limiter := newAPIKeyAdmissionLimiter()
	now := time.Unix(1_700_000_000, 0)
	if !limiter.allow(1, 10, 0, 4, now) {
		t.Fatal("batch of 4 within RPM=10 rejected")
	}
	if limiter.allow(1, 10, 0, 7, now) {
		t.Fatal("batch of 7 would exceed the remaining RPM budget")
	}
	if !limiter.allow(1, 10, 0, 6, now) {
		t.Fatal("batch of 6 should fit exactly")
	}
}

func TestAPIKeyAdmissionLimiterEnforcesRPD(t *testing.T) {
	limiter := newAPIKeyAdmissionLimiter()
	now := time.Unix(1_700_000_000, 0)
	if !limiter.allow(1, 0, 2, 1, now) {
		t.Fatal("first RPD request rejected")
	}
	if !limiter.allow(1, 0, 2, 1, now.Add(time.Hour)) {
		t.Fatal("second RPD request rejected")
	}
	if limiter.allow(1, 0, 2, 1, now.Add(2*time.Hour)) {
		t.Fatal("third RPD request should exceed RPD=2")
	}
	if !limiter.allow(1, 0, 2, 1, now.Add(apiKeyRPDWindow)) {
		t.Fatal("RPD window should reset after 24h")
	}
}
