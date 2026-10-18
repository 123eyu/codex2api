package auth

import (
	"testing"
	"time"
)

func TestShouldPersistUsageFrameThrottlesAndForces(t *testing.T) {
	acc := newQuotaAutoPauseTestAccount()
	now := time.Unix(1_700_000_000, 0)
	interval := time.Minute

	if !acc.ShouldPersistUsageFrame(now, 10, true, 5, true, interval) {
		t.Fatal("first observation must persist")
	}
	acc.MarkUsageFramePersisted(now, 10, true, 5, true)
	if acc.ShouldPersistUsageFrame(now.Add(time.Second), 10.5, true, 5, true, interval) {
		t.Fatal("small delta inside the interval must be throttled")
	}
	if !acc.ShouldPersistUsageFrame(now.Add(time.Second), 16, true, 5, true, interval) {
		t.Fatal("a 5pp delta must force persistence")
	}
	acc.MarkUsageFramePersisted(now.Add(time.Second), 16, true, 5, true)
	if acc.ShouldPersistUsageFrame(now.Add(2*time.Second), 16.5, true, 5, true, interval) {
		t.Fatal("small delta after a forced write must be throttled again")
	}
	if !acc.ShouldPersistUsageFrame(now.Add(2*time.Minute), 16.5, true, 5, true, interval) {
		t.Fatal("elapsed interval must persist")
	}
}

func TestShouldPersistUsageFrameRetriesUntilMarked(t *testing.T) {
	acc := newQuotaAutoPauseTestAccount()
	now := time.Unix(1_700_000_000, 0)

	if !acc.ShouldPersistUsageFrame(now, 10, true, 5, true, time.Minute) {
		t.Fatal("first observation must persist")
	}
	// Simulate a failed write: without MarkUsageFramePersisted the decision
	// must be retried instead of being throttled for the interval.
	if !acc.ShouldPersistUsageFrame(now.Add(time.Second), 10, true, 5, true, time.Minute) {
		t.Fatal("failed write must be retried on the next observation")
	}
}

func TestShouldPersistUsageFrameForcesThresholdCrossing(t *testing.T) {
	acc := newQuotaAutoPauseTestAccount()
	now := time.Unix(1_700_000_000, 0)
	acc.effectiveAutoPause5h = 0.5

	if !acc.ShouldPersistUsageFrame(now, 48, true, 0, false, time.Hour) {
		t.Fatal("first observation must persist")
	}
	acc.MarkUsageFramePersisted(now, 48, true, 0, false)
	if !acc.ShouldPersistUsageFrame(now.Add(time.Second), 50, true, 0, false, time.Hour) {
		t.Fatal("crossing the auto-pause threshold must force persistence")
	}
}

func TestGetAccountLiveUsageSnapshotProjectsStatusAndUsage(t *testing.T) {
	acc := newQuotaAutoPauseTestAccount()
	acc.AutoPause5hThreshold = 0.9
	acc.UsagePercent5h = 95
	acc.UsagePercent5hValid = true
	acc.Reset5hAt = time.Now().Add(time.Hour)
	acc.UsagePercent7d = 31
	acc.UsagePercent7dValid = true
	acc.UsageUpdatedAt = time.Now()
	setAutoPauseThresholdsWithGuard(acc, 5)

	snapshot := acc.GetAccountLiveUsageSnapshot()
	if snapshot.Status != "quota_paused" {
		t.Fatalf("status = %q, want quota_paused", snapshot.Status)
	}
	if !snapshot.UsagePercent5hValid || snapshot.UsagePercent5h != 95 {
		t.Fatalf("5h snapshot = %+v", snapshot)
	}
	if !snapshot.UsagePercent7dValid || snapshot.UsagePercent7d != 31 {
		t.Fatalf("7d snapshot = %+v", snapshot)
	}
}
