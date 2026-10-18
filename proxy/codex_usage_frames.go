package proxy

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/codex2api/auth"
	"github.com/tidwall/gjson"
)

const (
	codexRateLimitsFrameType       = "codex.rate_limits"
	codexResponseMetadataFrameType = "codex.response.metadata"
	responseMetadataFrameType      = "response.metadata"
)

// codexUsageFramePersistIntervalNanos bounds how often a WS frame observation
// is written to the database. The in-memory snapshot is updated on every frame;
// persistence only matters for restart recovery, so a burst across a large pool
// must not turn every turn into a write. Operators can tune the floor with
// CODEX_WS_USAGE_FRAME_PERSIST_SECONDS (default 30; threshold crossings, 5pp
// deltas and 100% still force a write regardless of the interval).
var codexUsageFramePersistIntervalNanos = func() *atomic.Int64 {
	value := &atomic.Int64{}
	value.Store(int64(auth.UsageFramePersistInterval))
	return value
}()

// SetCodexUsageFramePersistInterval overrides the frame persistence cadence.
// Values <= 0 restore the default. Call before serving traffic.
func SetCodexUsageFramePersistInterval(interval time.Duration) {
	if interval <= 0 {
		interval = auth.UsageFramePersistInterval
	}
	codexUsageFramePersistIntervalNanos.Store(int64(interval))
}

func codexUsageFramePersistInterval() time.Duration {
	return time.Duration(codexUsageFramePersistIntervalNanos.Load())
}

// ApplyCodexUsageEventFrame ingests the preflight metadata frames that the
// upstream Codex WebSocket sends in place of response headers. On the WS
// transport there are no x-codex-* response headers per turn, so without this
// path the account usage snapshot only advances on WHAM probes. Frames are
// already on the wire (the gateway forwards them downstream), so this adds no
// upstream requests and no new request headers.
func ApplyCodexUsageEventFrame(store *auth.Store, account *auth.Account, eventType string, frame gjson.Result, observedAt time.Time) {
	if account == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case codexRateLimitsFrameType:
		applyCodexRateLimitsFrame(store, account, frame, observedAt)
	case codexResponseMetadataFrameType, responseMetadataFrameType:
		applyCodexResponseMetadataFrame(store, account, frame, observedAt)
	}
}

// applyCodexRateLimitsFrame maps the codex.rate_limits event onto the same
// synthetic headers the HTTP transport exposes. Only the default "codex" limit
// is applied to the account's 5h/7d snapshots; metered limits (for example a
// Spark or reserve bucket) must not overwrite the main windows.
func applyCodexRateLimitsFrame(store *auth.Store, account *auth.Account, frame gjson.Result, observedAt time.Time) {
	headers := codexRateLimitsHeaders(frame, observedAt)
	if len(headers) == 0 {
		return
	}
	applyCodexUsageHeaders(store, account, headers, observedAt)
}

func codexRateLimitsHeaders(frame gjson.Result, observedAt time.Time) http.Header {
	if name := firstCodexFrameString(frame, "metered_limit_name", "limit_name"); name != "" && !isDefaultCodexLimitName(name) {
		return nil
	}
	headers := make(http.Header)
	if plan := strings.TrimSpace(frame.Get("plan_type").String()); plan != "" {
		headers.Set("x-codex-plan-type", plan)
	}
	setCodexFrameWindow(headers, "x-codex-primary-", frame.Get("rate_limits.primary"), observedAt)
	setCodexFrameWindow(headers, "x-codex-secondary-", frame.Get("rate_limits.secondary"), observedAt)
	// The frame carries a compact credits snapshot. Only adopt it when both
	// booleans are present, matching the sparse-header contract: a missing flag
	// must not overwrite a richer WHAM observation with false.
	if credits := frame.Get("credits"); credits.IsObject() {
		hasCredits := credits.Get("has_credits")
		unlimited := credits.Get("unlimited")
		if hasCredits.Exists() && unlimited.Exists() {
			headers.Set("x-codex-credits-has-credits", hasCredits.String())
			headers.Set("x-codex-credits-unlimited", unlimited.String())
			if balance := strings.TrimSpace(credits.Get("balance").String()); balance != "" {
				headers.Set("x-codex-credits-balance", balance)
			}
		}
	}
	return headers
}

// applyCodexResponseMetadataFrame adopts the x-codex-* headers embedded in a
// codex.response.metadata frame. Other headers are intentionally dropped: some
// upstream metadata frames also carry cookies or request identifiers that must
// not be persisted or logged.
func applyCodexResponseMetadataFrame(store *auth.Store, account *auth.Account, frame gjson.Result, observedAt time.Time) {
	headers := codexResponseMetadataHeaders(frame, observedAt)
	if len(headers) == 0 {
		return
	}
	applyCodexUsageHeaders(store, account, headers, observedAt)
}

func codexResponseMetadataHeaders(frame gjson.Result, observedAt time.Time) http.Header {
	headers := make(http.Header)
	frame.Get("headers").ForEach(func(key, value gjson.Result) bool {
		name := strings.ToLower(strings.TrimSpace(key.String()))
		if !strings.HasPrefix(name, "x-codex-") {
			return true
		}
		// Convert the absolute reset form to the relative form using the frame's
		// observation time. The header parser otherwise resolves it against the
		// wall clock, which is identical in production but not in deterministic
		// tests and can drift if a frame is replayed later.
		if strings.HasSuffix(name, "-reset-at") {
			epoch := codexFrameInt64(value)
			if epoch > 0 {
				seconds := epoch - observedAt.Unix()
				if seconds < 0 {
					seconds = 0
				}
				prefix := strings.TrimSuffix(name, "reset-at")
				headers.Set(prefix+"reset-after-seconds", strconv.FormatInt(seconds, 10))
			}
			return true
		}
		headers.Set(name, value.String())
		return true
	})
	return headers
}

func applyCodexUsageHeaders(store *auth.Store, account *auth.Account, headers http.Header, observedAt time.Time) {
	if len(headers) == 0 {
		return
	}
	resp := &http.Response{Header: headers}
	// A per-turn frame may omit one window; never let it erase a valid snapshot.
	// WHAM probes remain the authority for window presence/absence. The persist
	// decision runs inside ApplyUsageObservation so a stale/out-of-order frame
	// cannot advance the throttling state without being applied.
	syncCodexUsageState(store, account, resp, observedAt, codexUsageSyncOptions{
		persistUsageSnapshot: true,
		clearAbsent5h:        false,
		shouldPersistUsage: func(pct5h float64, has5h bool, pct7d float64, has7d bool) bool {
			return account.ShouldPersistUsageFrame(observedAt, pct5h, has5h, pct7d, has7d, codexUsageFramePersistInterval())
		},
	})
}

func setCodexFrameWindow(headers http.Header, prefix string, window gjson.Result, observedAt time.Time) {
	if !window.IsObject() {
		return
	}
	used := window.Get("used_percent")
	minutes := window.Get("window_minutes")
	if !used.Exists() || !minutes.Exists() {
		return
	}
	headers.Set(prefix+"used-percent", used.String())
	headers.Set(prefix+"window-minutes", minutes.String())
	if reset := codexFrameResetSeconds(window, observedAt); reset != "" {
		headers.Set(prefix+"reset-after-seconds", reset)
	}
}

// codexFrameResetSeconds accepts both relative and absolute reset shapes so the
// parser tolerates upstream schema drift. The official frame uses reset_at
// (absolute Unix seconds); older/observed fixtures used resets_in_seconds or
// reset_after_seconds.
func codexFrameResetSeconds(window gjson.Result, observedAt time.Time) string {
	for _, key := range []string{"resets_in_seconds", "reset_after_seconds"} {
		if value := window.Get(key); value.Exists() {
			if n := codexFrameInt64(value); n > 0 {
				return strconv.FormatInt(n, 10)
			}
		}
	}
	for _, key := range []string{"reset_at", "resets_at"} {
		if value := window.Get(key); value.Exists() {
			epoch := codexFrameInt64(value)
			if epoch <= 0 {
				continue
			}
			seconds := epoch - observedAt.Unix()
			if seconds < 0 {
				seconds = 0
			}
			return strconv.FormatInt(seconds, 10)
		}
	}
	return ""
}

func codexFrameInt64(value gjson.Result) int64 {
	if value.Type == gjson.Number {
		return value.Int()
	}
	n, err := strconv.ParseInt(strings.TrimSpace(value.String()), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func firstCodexFrameString(frame gjson.Result, paths ...string) string {
	for _, path := range paths {
		if value := strings.TrimSpace(frame.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}

func isDefaultCodexLimitName(name string) bool {
	normalized := strings.NewReplacer("_", "-", " ", "-").Replace(strings.ToLower(strings.TrimSpace(name)))
	return normalized == "codex" || normalized == "gpt-codex"
}

// ApplyCodexIdleUsageFrame is the observer for connection-level metadata frames
// that arrive after a response terminal (the wsrelay read pump drops those
// instead of routing them to a request). It updates only the in-memory usage
// snapshot, with a non-blocking try-lock, so the read pump never waits on a DB
// write. Persistence and the 100% cooldown markings stay on the preflight/WHAM
// paths; this path exists to shrink the window between a turn finishing and
// the scheduler seeing its consumption.
func ApplyCodexIdleUsageFrame(account *auth.Account, eventType string, payload []byte) {
	if account == nil || len(payload) == 0 {
		return
	}
	observedAt := time.Now()
	frame := gjson.ParseBytes(payload)
	var headers http.Header
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case codexRateLimitsFrameType:
		headers = codexRateLimitsHeaders(frame, observedAt)
	case codexResponseMetadataFrameType, responseMetadataFrameType:
		headers = codexResponseMetadataHeaders(frame, observedAt)
	default:
		return
	}
	if len(headers) == 0 {
		return
	}
	observation := parseCodexUsageHeaderObservation(&http.Response{Header: headers})
	if !observation.authoritative {
		return
	}
	account.TryApplyUsageObservation(observedAt, func() {
		// Never clear an absent 5h window from a partial connection-level frame.
		applyCodexUsageHeaderObservation(nil, account, observation, observedAt, false)
	})
}
