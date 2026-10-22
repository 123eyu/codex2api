package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// antigravityQuotaTestAccount 构造最小可参与的 Antigravity 运行时账号（纯内存，
// 不触库、不联网），用于 FakeClock 驱动的调度循环用例。
func antigravityQuotaTestAccount(id int64) *Account {
	return &Account{
		DBID:                 id,
		UpstreamType:         UpstreamAntigravity,
		AccessToken:          "access-token",
		RefreshToken:         "refresh-token",
		AntigravityProjectID: "project-id",
		HealthTier:           HealthTierHealthy,
	}
}

type antigravityQuotaCall struct {
	dbID int64
	at   time.Time
}

// antigravityQuotaHarness 用 FakeClock 驱动调度循环：假时钟只由测试推进，
// 因此每次刷新时观察到的 clock.Now() 完全确定，可直接断言名义槽位与退避时刻。
type antigravityQuotaHarness struct {
	t            *testing.T
	clock        *FakeClock
	store        *Store
	scheduler    *antigravityQuotaScheduler
	parkObserved func()
	parkCh       chan struct{}

	mu         sync.Mutex
	calls      []antigravityQuotaCall
	refreshErr error
}

func newAntigravityQuotaHarness(t *testing.T, accounts ...*Account) *antigravityQuotaHarness {
	t.Helper()
	return newAntigravityQuotaHarnessWithPhase(t, time.Duration(0), false, accounts...)
}

// newAntigravityQuotaHarnessWithPhase 允许固定 theta（随机相位在本批用例里会把
// 槽位推到覆盖窗外，破坏确定性）；fixedPhase=false 时走生产构造路径。
func newAntigravityQuotaHarnessWithPhase(t *testing.T, theta time.Duration, fixedPhase bool, accounts ...*Account) *antigravityQuotaHarness {
	t.Helper()
	store := &Store{}
	store.accounts = append(store.accounts, accounts...)
	// 预热一次账号快照：循环内每次快照都走已发布的指针，不重复回落到 s.accounts。
	store.publishAccountSnapshot(append([]*Account(nil), store.accounts...))
	clock := NewFakeClock(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))
	h := &antigravityQuotaHarness{t: t, clock: clock, store: store, parkCh: make(chan struct{}, 64)}
	h.parkObserved = func() {
		select {
		case h.parkCh <- struct{}{}:
		default:
		}
	}
	var scheduler *antigravityQuotaScheduler
	if fixedPhase {
		scheduler = newAntigravityQuotaSchedulerWithPhase(store, clock, h.refresh, clock.Now(), theta)
	} else {
		scheduler = newAntigravityQuotaScheduler(store, clock, h.refresh)
	}
	scheduler.parkObserved = h.parkObserved
	h.scheduler = scheduler
	store.antigravityQuotaScheduler = scheduler
	return h
}

func (h *antigravityQuotaHarness) refresh(_ context.Context, acc *Account) error {
	h.mu.Lock()
	h.calls = append(h.calls, antigravityQuotaCall{dbID: acc.DBID, at: h.clock.Now()})
	err := h.refreshErr
	h.mu.Unlock()
	h.store.WakeAntigravityQuotaScheduler()
	return err
}

// start 启动调度循环，并在用例结束时保证循环与已派发的一次性 goroutine 全部退出。
func (h *antigravityQuotaHarness) start() {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		h.scheduler.Run(ctx)
	}()
	h.t.Cleanup(func() {
		cancel()
		h.scheduler.Stop()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			h.t.Error("antigravity quota scheduler did not exit after Stop")
		}
		h.scheduler.Wait()
	})
	h.settle()
}

// settle 等调度循环把当前挂起的唤醒/到点事件消费完并重新挂起（parked）。
// 时钟只由测试推进，因此循环 parked 时定时器 deadline 与名义槽位严格对齐。
func (h *antigravityQuotaHarness) settle() {
	for {
		select {
		case <-h.parkCh:
		case <-time.After(30 * time.Millisecond):
			return
		}
	}
}

// advance 以固定步长推进假时钟，每步都等循环重新挂起。
func (h *antigravityQuotaHarness) advance(steps int, step time.Duration) {
	h.t.Helper()
	for i := 0; i < steps; i++ {
		h.clock.Advance(step)
		h.settle()
	}
}

func (h *antigravityQuotaHarness) setRefreshErr(err error) {
	h.mu.Lock()
	h.refreshErr = err
	h.mu.Unlock()
}

func (h *antigravityQuotaHarness) callsFor(dbID int64) []antigravityQuotaCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []antigravityQuotaCall
	for _, call := range h.calls {
		if call.dbID == dbID {
			out = append(out, call)
		}
	}
	return out
}

func (h *antigravityQuotaHarness) calledIDs() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]int64, 0, len(h.calls))
	for _, call := range h.calls {
		out = append(out, call.dbID)
	}
	return out
}

func (h *antigravityQuotaHarness) totalCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

// TestAntigravityQuotaSchedulerEligibility 断言资格集合：只有启用中的 OAuth 且已
// 绑定 project 的账号参与巡检；禁用、暂停调度与 API-Key 账号一律不派发（Spec §5.3.1）。
func TestAntigravityQuotaSchedulerEligibility(t *testing.T) {
	healthy := antigravityQuotaTestAccount(101)
	disabled := antigravityQuotaTestAccount(102)
	paused := antigravityQuotaTestAccount(103)
	apiKey := antigravityQuotaTestAccount(104)
	apiKey.APIKey = "AIza-demo-api-key"
	apiKey.RefreshToken = ""
	apiKey.AccessToken = ""
	noProject := antigravityQuotaTestAccount(105)
	noProject.AntigravityProjectID = ""

	atomic.StoreInt32(&disabled.Disabled, 1)
	atomic.StoreInt32(&paused.DispatchPaused, 1)

	h := newAntigravityQuotaHarness(t, apiKey, paused, disabled, healthy, noProject)
	h.start()
	// 覆盖三个完整周期，任何一次错误派发都会留下记录。
	h.advance(180, time.Minute)

	if got := len(h.callsFor(healthy.DBID)); got == 0 {
		t.Fatalf("eligible OAuth account must be refreshed, calls=%d", got)
	}
	for _, id := range h.calledIDs() {
		if id != healthy.DBID {
			t.Fatalf("only the eligible OAuth account may be refreshed, got calls=%v", h.calledIDs())
		}
	}
	// 单账号资格集合 n=1：三个周期内至多三次，不得重复补发（Spec §5.3 无历史补偿）。
	if got := len(h.callsFor(healthy.DBID)); got > 3 {
		t.Fatalf("one account must be refreshed at most once per interval, got %d calls", got)
	}
}

// TestAntigravityQuotaSchedulerSpacing 断言均匀分散：N=2、I=60m 时每个账号一个
// 周期恰好一次，且同一账号相邻两次刷新严格相差一个周期。
func TestAntigravityQuotaSchedulerSpacing(t *testing.T) {
	first := antigravityQuotaTestAccount(201)
	second := antigravityQuotaTestAccount(202)

	h := newAntigravityQuotaHarness(t, first, second)
	h.start()
	h.advance(190, time.Minute)

	interval := time.Duration(ConfiguredAntigravitySettings().GetQuotaRefreshIntervalMinutes()) * time.Minute
	for _, acc := range []*Account{first, second} {
		calls := h.callsFor(acc.DBID)
		if len(calls) < 3 {
			t.Fatalf("account %d: expected at least 3 refreshes in 190m, got %d", acc.DBID, len(calls))
		}
		if len(calls) > 4 {
			t.Fatalf("account %d: expected at most one refresh per interval, got %d", acc.DBID, len(calls))
		}
		for i := 1; i < len(calls); i++ {
			gap := calls[i].at.Sub(calls[i-1].at)
			if diff := gap - interval; diff > time.Second || diff < -time.Second {
				t.Fatalf("account %d: gap between attempt %d and %d is %v, want %v", acc.DBID, i, i+1, gap, interval)
			}
		}
	}

	// 两账号槽位必须错开（Δ = I/N = 30m），不得挤在同一时刻。
	for _, a := range h.callsFor(first.DBID) {
		for _, b := range h.callsFor(second.DBID) {
			if a.at.Equal(b.at) {
				t.Fatalf("N=2 slots must be phase-shifted by Δ=I/2, both refreshed at %v", a.at)
			}
		}
	}
}

// TestAntigravityQuotaSchedulerBackoff 断言失败退避真正生效：第二次尝试不早于
// ComputeBackoff(I, 1)，失败后绝不立即重试。
func TestAntigravityQuotaSchedulerBackoff(t *testing.T) {
	acc := antigravityQuotaTestAccount(301)
	h := newAntigravityQuotaHarness(t, acc)
	h.setRefreshErr(errors.New("upstream 503"))
	h.start()

	// 等到第一次尝试出现（至多一个周期 + 一步）。
	var firstCall time.Time
	for i := 0; i < 130; i++ {
		h.clock.Advance(time.Minute)
		h.settle()
		if calls := h.callsFor(acc.DBID); len(calls) > 0 {
			firstCall = calls[0].at
			break
		}
	}
	if firstCall.IsZero() {
		t.Fatal("expected the first attempt within one interval")
	}
	// 失败后立刻推进一分钟：退避窗口内绝不允许出现第二次尝试。
	h.advance(2, time.Minute)
	if got := len(h.callsFor(acc.DBID)); got != 1 {
		t.Fatalf("must not retry immediately after a failure, calls=%d", got)
	}

	interval := time.Duration(ConfiguredAntigravitySettings().GetQuotaRefreshIntervalMinutes()) * time.Minute
	backoff := ComputeBackoff(interval, 1)
	// 推进到退避之后（留一个周期的余量）。
	for i := 0; i < 400; i++ {
		h.clock.Advance(time.Minute)
		h.settle()
		if len(h.callsFor(acc.DBID)) >= 2 {
			break
		}
	}
	calls := h.callsFor(acc.DBID)
	if len(calls) < 2 {
		t.Fatalf("expected a second attempt after the backoff window, got %d calls", len(calls))
	}
	gap := calls[1].at.Sub(calls[0].at)
	if gap < backoff {
		t.Fatalf("second attempt after %v, want >= %v (backoff for 1 failure)", gap, backoff)
	}
	if gap > backoff+2*interval {
		t.Fatalf("second attempt deferred too far: %v, backoff=%v", gap, backoff)
	}
}

// TestAntigravityQuotaSchedulerDisabledSetting 断言 quota_refresh_enabled=false
// 时调度器不派发、不联网；改回 true 并 Wake 后立即恢复。
func TestAntigravityQuotaSchedulerDisabledSetting(t *testing.T) {
	previous := ConfiguredAntigravitySettings()
	t.Cleanup(func() { SetConfiguredAntigravitySettings(previous) })

	disabled := false
	minutes := 3
	SetConfiguredAntigravitySettings(AntigravitySettings{QuotaRefreshEnabled: &disabled, QuotaRefreshIntervalMinutes: &minutes})

	acc := antigravityQuotaTestAccount(401)
	h := newAntigravityQuotaHarness(t, acc)
	h.start()
	h.advance(200, time.Minute)
	if got := h.totalCalls(); got != 0 {
		t.Fatalf("quota_refresh_enabled=false must not dispatch anything, calls=%d", got)
	}

	enabled := true
	SetConfiguredAntigravitySettings(AntigravitySettings{QuotaRefreshEnabled: &enabled, QuotaRefreshIntervalMinutes: &minutes})
	h.store.WakeAntigravityQuotaScheduler()
	h.settle()
	h.advance(200, time.Minute)
	if got := h.totalCalls(); got == 0 {
		t.Fatal("re-enabling quota refresh must resume dispatching after Wake")
	}
	if got := len(h.callsFor(acc.DBID)); got == 0 {
		t.Fatalf("expected refreshes for the eligible account, got %d", got)
	}
}

// TestAntigravityQuotaSchedulerCoverageWindow 断言启动恢复：已有观测落在当前名义
// 覆盖窗内时跳过本轮（fresh 已覆盖不重复联网），下一轮才刷新。
func TestAntigravityQuotaSchedulerCoverageWindow(t *testing.T) {
	previous := ConfiguredAntigravitySettings()
	minutes := 60
	SetConfiguredAntigravitySettings(AntigravitySettings{QuotaRefreshEnabled: ptr(true), QuotaRefreshIntervalMinutes: &minutes})
	t.Cleanup(func() { SetConfiguredAntigravitySettings(previous) })

	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	acc := antigravityQuotaTestAccount(501)
	acc.AntigravityQuota = AntigravityQuotaSnapshot{
		SchemaVersion: 2,
		SourceMetadata: map[string]SourceMeta{
			"groups": {AppliedSeq: 3, ObservedAt: now.Add(-10 * time.Minute), PresenceVerified: true},
		},
	}

	// theta=0 且 n=1 时名义槽位落在 epoch+I/2（30m）：observed_at=now-10m 落在
	// (nominalDue-I, nominalDue] = (00:00, 12:30] 覆盖窗内 -> 第一轮跳过。
	h := newAntigravityQuotaHarnessWithPhase(t, 0, true, acc)
	h.start()
	h.advance(45, time.Minute)
	if got := h.totalCalls(); got != 0 {
		t.Fatalf("fresh observation inside the coverage window must skip the round, calls=%d", got)
	}

	h.advance(45, time.Minute)
	calls := h.callsFor(acc.DBID)
	if len(calls) == 0 {
		t.Fatal("the next nominal slot after the coverage window must refresh")
	}
	if first := calls[0].at.Sub(now); first < 60*time.Minute {
		t.Fatalf("first refresh at +%v is still inside the covered slot, want > 60m", first)
	}
}

func ptr(v bool) *bool { return &v }
