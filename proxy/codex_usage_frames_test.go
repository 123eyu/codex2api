package proxy

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

func newUsageFrameTestAccount() *auth.Account {
	return &auth.Account{
		DBID:       1,
		PlanType:   "plus",
		Status:     auth.StatusReady,
		HealthTier: auth.HealthTierHealthy,
	}
}

func TestApplyCodexRateLimitsFrameUpdatesWindows(t *testing.T) {
	acc := newUsageFrameTestAccount()
	observedAt := time.Unix(1_700_000_000, 0)
	frame := gjson.Parse(`{
		"type":"codex.rate_limits",
		"plan_type":"plus",
		"rate_limits":{
			"primary":{"used_percent":42,"window_minutes":300,"reset_at":1700000900},
			"secondary":{"used_percent":8,"window_minutes":10080,"reset_at":1700604800}
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.rate_limits", frame, observedAt)

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 42 {
		t.Fatalf("5h usage = %v, valid=%v, want 42", pct5h, ok)
	}
	if want := observedAt.Add(15 * time.Minute); !acc.GetReset5hAt().Equal(want) {
		t.Fatalf("Reset5hAt = %v, want %v", acc.GetReset5hAt(), want)
	}
	pct7d, ok := acc.GetUsagePercent7d()
	if !ok || pct7d != 8 {
		t.Fatalf("7d usage = %v, valid=%v, want 8", pct7d, ok)
	}
}

func TestApplyCodexRateLimitsFrameKeepsAbsent5hWindow(t *testing.T) {
	acc := newUsageFrameTestAccount()
	observedAt := time.Unix(1_700_000_000, 0)
	acc.SetUsageSnapshot5hAt(77, observedAt.Add(time.Hour), observedAt)
	frame := gjson.Parse(`{
		"type":"codex.rate_limits",
		"rate_limits":{
			"primary":null,
			"secondary":{"used_percent":12,"window_minutes":10080,"reset_at":1700604800}
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.rate_limits", frame, observedAt.Add(time.Minute))

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 77 {
		t.Fatalf("partial frame cleared 5h snapshot: usage=%v valid=%v", pct5h, ok)
	}
}

func TestApplyCodexRateLimitsFrameIgnoresMeteredLimits(t *testing.T) {
	acc := newUsageFrameTestAccount()
	observedAt := time.Unix(1_700_000_000, 0)
	acc.SetUsageSnapshot5hAt(55, observedAt.Add(time.Hour), observedAt)
	frame := gjson.Parse(`{
		"type":"codex.rate_limits",
		"metered_limit_name":"codex_spark",
		"rate_limits":{
			"primary":{"used_percent":99,"window_minutes":300,"reset_at":1700000900}
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.rate_limits", frame, observedAt.Add(time.Minute))

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 55 {
		t.Fatalf("metered limit frame overwrote main 5h snapshot: usage=%v valid=%v", pct5h, ok)
	}
}

func TestApplyCodexResponseMetadataFrameAdoptsUsageHeaders(t *testing.T) {
	acc := newUsageFrameTestAccount()
	observedAt := time.Unix(1_700_000_000, 0)
	frame := gjson.Parse(`{
		"type":"codex.response.metadata",
		"headers":{
			"x-codex-primary-used-percent":"33",
			"x-codex-primary-window-minutes":"300",
			"x-codex-primary-reset-at":"1700000900",
			"set-cookie":"must-not-be-used"
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.response.metadata", frame, observedAt)

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 33 {
		t.Fatalf("5h usage = %v valid=%v, want 33", pct5h, ok)
	}
	if want := observedAt.Add(15 * time.Minute); !acc.GetReset5hAt().Equal(want) {
		t.Fatalf("Reset5hAt = %v, want %v", acc.GetReset5hAt(), want)
	}
}

func TestApplyCodexUsageEventFrameIgnoresUnrelatedEvents(t *testing.T) {
	acc := newUsageFrameTestAccount()
	observedAt := time.Unix(1_700_000_000, 0)
	acc.SetUsageSnapshot5hAt(21, observedAt.Add(time.Hour), observedAt)
	frame := gjson.Parse(`{"type":"response.created","response":{"id":"resp-1"}}`)

	ApplyCodexUsageEventFrame(nil, acc, "response.created", frame, observedAt)

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 21 {
		t.Fatalf("unrelated event changed usage: usage=%v valid=%v", pct5h, ok)
	}
}

func TestApplyCodexRateLimitsFrameFreePlanHasOnlyLongWindow(t *testing.T) {
	acc := newUsageFrameTestAccount()
	acc.PlanType = "free"
	observedAt := time.Unix(1_700_000_000, 0)
	frame := gjson.Parse(`{
		"type":"codex.rate_limits",
		"plan_type":"free",
		"rate_limits":{
			"primary":{"used_percent":64,"window_minutes":10080,"reset_at":1700604800},
			"secondary":null
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.rate_limits", frame, observedAt)

	if _, ok := acc.GetUsagePercent5h(); ok {
		t.Fatal("free plan frame must not fabricate a 5h window")
	}
	pct7d, ok := acc.GetUsagePercent7d()
	if !ok || pct7d != 64 {
		t.Fatalf("7d usage = %v valid=%v, want 64", pct7d, ok)
	}
}

func TestApplyCodexRateLimitsFrame5hOnlyKeeps7dSnapshot(t *testing.T) {
	acc := newUsageFrameTestAccount()
	observedAt := time.Unix(1_700_000_000, 0)
	acc.SetUsagePercent7d(31)
	frame := gjson.Parse(`{
		"type":"codex.rate_limits",
		"rate_limits":{
			"primary":{"used_percent":56,"window_minutes":300,"reset_at":1700000900},
			"secondary":null
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.rate_limits", frame, observedAt)

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 56 {
		t.Fatalf("5h usage = %v valid=%v, want 56", pct5h, ok)
	}
	pct7d, ok := acc.GetUsagePercent7d()
	if !ok || pct7d != 31 {
		t.Fatalf("partial 5h frame cleared 7d snapshot: usage=%v valid=%v", pct7d, ok)
	}
}

func TestApplyCodexRateLimitsFrameTeamMonthlyWindow(t *testing.T) {
	acc := newUsageFrameTestAccount()
	acc.PlanType = "team"
	observedAt := time.Unix(1_700_000_000, 0)
	frame := gjson.Parse(`{
		"type":"codex.rate_limits",
		"rate_limits":{
			"primary":{"used_percent":44,"window_minutes":43200,"reset_at":1702592000},
			"secondary":null
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.rate_limits", frame, observedAt)

	if _, ok := acc.GetUsagePercent5h(); ok {
		t.Fatal("monthly window must not be classified as 5h")
	}
	pct7d, ok := acc.GetUsagePercent7d()
	if !ok || pct7d != 44 {
		t.Fatalf("7d usage = %v valid=%v, want 44", pct7d, ok)
	}
	if got := acc.GetWindow7dSeconds(); got != 43200*60 {
		t.Fatalf("Window7dSeconds = %d, want %d", got, 43200*60)
	}
}

func TestApplyCodexRateLimitsFramePreservesKnownReset(t *testing.T) {
	acc := newUsageFrameTestAccount()
	observedAt := time.Unix(1_700_000_000, 0)
	resetAt := observedAt.Add(2 * time.Hour)
	acc.SetUsageSnapshot5hAt(20, resetAt, observedAt)
	frame := gjson.Parse(`{
		"type":"codex.rate_limits",
		"rate_limits":{
			"primary":{"used_percent":70,"window_minutes":300},
			"secondary":null
		}
	}`)

	ApplyCodexUsageEventFrame(nil, acc, "codex.rate_limits", frame, observedAt.Add(time.Minute))

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 70 {
		t.Fatalf("5h usage = %v valid=%v, want 70", pct5h, ok)
	}
	if !acc.GetReset5hAt().Equal(resetAt) {
		t.Fatalf("Reset5hAt = %v, want preserved %v", acc.GetReset5hAt(), resetAt)
	}
}

func TestApplyCodexIdleUsageFrameUpdatesMemoryOnly(t *testing.T) {
	acc := newUsageFrameTestAccount()
	now := time.Now()
	acc.SetUsageSnapshot5hAt(20, now.Add(time.Hour), now)
	acc.SetUsagePercent7d(10)

	ApplyCodexIdleUsageFrame(acc, "codex.rate_limits", []byte(`{
		"type":"codex.rate_limits",
		"rate_limits":{
			"primary":{"used_percent":75,"window_minutes":300,"reset_at":1700000900},
			"secondary":null
		}
	}`))

	pct5h, ok := acc.GetUsagePercent5h()
	if !ok || pct5h != 75 {
		t.Fatalf("idle frame 5h usage = %v valid=%v, want 75", pct5h, ok)
	}
	pct7d, ok := acc.GetUsagePercent7d()
	if !ok || pct7d != 10 {
		t.Fatalf("idle partial frame cleared 7d snapshot: usage=%v valid=%v", pct7d, ok)
	}
}

func TestParseCodexUsageHeaderObservationAcceptsResetAt(t *testing.T) {
	now := time.Now()
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("x-codex-primary-used-percent", "33")
	resp.Header.Set("x-codex-primary-window-minutes", "300")
	resp.Header.Set("x-codex-primary-reset-at", strconv.FormatInt(now.Add(15*time.Minute).Unix(), 10))

	observation := parseCodexUsageHeaderObservation(resp)
	if !observation.w5h.valid {
		t.Fatal("reset-at header did not produce a valid 5h window")
	}
	if observation.w5h.resetSec < 14*60 || observation.w5h.resetSec > 16*60 {
		t.Fatalf("resetSec = %v, want about 900", observation.w5h.resetSec)
	}
}
