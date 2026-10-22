package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ErrAntigravityQuotaSummaryIncomplete 表示一次 groups summary 观测没有拿到可用的
// 配额事实（空响应或合并未推进，Spec §3.2）：调用方不得据此推进成功，已落库的观测
// 必须原样保留。
var ErrAntigravityQuotaSummaryIncomplete = errors.New("antigravity quota summary response is incomplete")

const (
	// antigravityQuotaSummarySource 是 groups summary 在 source_metadata 里的来源键，
	// 与 database 侧 Clear/Merge 使用同一命名。
	antigravityQuotaSummarySource = "groups"
	// antigravityQuotaRefreshOpType 是后台 summary 在控制面协调器里的操作类别：走
	// 后台通道（占共享许可），与 manual_refresh / http_401_recovery 区分（Spec §4.2）。
	antigravityQuotaRefreshOpType = "background_summary"
)

// antigravityQuotaSummaryTTLSeconds 把名义刷新间隔折算成快照 TTL 秒数：
// max(1h, 2*I)——TTL 至少覆盖两个名义周期，正常巡检节奏下 known_zero 观测不会
// 因 TTL 先到期而提前解除阻断（只能由可信 resetAt 或更新的观测改变，Spec §3.3）。
func antigravityQuotaSummaryTTLSeconds(interval time.Duration) int {
	ttl := 2 * interval
	if floor := time.Hour; floor > ttl {
		ttl = floor
	}
	seconds := int(ttl / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

// RefreshAntigravityQuotaSummary 对单个 AG OAuth 账号执行一次 groups summary 观测：
// 预约 → 网络 → 合并 → 提交 → 运行时投影。调用方负责调度与退避。
//
// 本函数只读 summary，绝不写 access_token/refresh_token，也不得调用 client.Sync
// （全量同步会轮换 token，属另一条通道）。返回 ErrAntigravityQuotaSummaryIncomplete
// 之外的错误一律不推进成功；Latest-Success-Wins 拒绝（applied=false）返回 nil，
// 它是正常结果而不是失败，不得触发退避。
func (s *Store) RefreshAntigravityQuotaSummary(ctx context.Context, account *Account) error {
	if s == nil || s.db == nil {
		return errors.New("antigravity quota summary refresh requires a database-backed store")
	}
	if account == nil {
		return errors.New("antigravity quota summary refresh requires an account")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	account.mu.RLock()
	dbID := account.DBID
	projectID := strings.TrimSpace(account.AntigravityProjectID)
	account.mu.RUnlock()
	if dbID == 0 {
		return errors.New("antigravity quota summary refresh requires a persisted account id")
	}
	if projectID == "" {
		return fmt.Errorf("Antigravity account %d has no project id for quota summary", dbID)
	}
	if kind := account.AntigravityAuthKind(); kind != AntigravityAuthKindOAuth {
		return fmt.Errorf("antigravity quota summary refresh requires an OAuth account, got kind %q", kind)
	}
	if atomic.LoadInt32(&account.Disabled) != 0 || atomic.LoadInt32(&account.DispatchPaused) != 0 {
		return fmt.Errorf("Antigravity account %d is not refreshable (disabled or dispatch paused)", dbID)
	}

	coordinator := s.antigravityFlightCoordinator()
	lease, err := coordinator.AcquireFlight(ctx, dbID, antigravityQuotaRefreshOpType)
	if err != nil {
		return fmt.Errorf("acquire Antigravity quota summary flight: %w", err)
	}
	defer lease.Release()
	releasePermit, err := coordinator.AcquirePermit(ctx, antigravityQuotaRefreshOpType)
	if err != nil {
		return fmt.Errorf("acquire Antigravity quota summary permit: %w", err)
	}
	defer releasePermit()

	row, err := s.db.GetAccountByID(ctx, dbID)
	if err != nil {
		return fmt.Errorf("load Antigravity account %d: %w", dbID, err)
	}
	if reason, _ := antigravityPersistedHardFence(row); reason != "" {
		return fmt.Errorf("Antigravity account %d carries a durable hard fence: %s", dbID, reason)
	}
	// 身份栅栏 fail-closed：OAuth 账号的正常路径必有 credential family，缺 family 属
	// 异常状态。database 侧的 scope fence 在 expectedScope 为空时不做身份校验（那是
	// 调用方的责任），因此校验必须由本调用方在预约与联网之前完成——绝不把配额观测写进
	// 身份不明的 durable 行。
	familyScope := strings.TrimSpace(row.GetCredential("credential_family_id"))
	if familyScope == "" {
		return fmt.Errorf("Antigravity account %d has no credential family for quota summary identity fence", dbID)
	}

	proxyURL, ok := s.ResolveUsableProxyForAccount(account)
	if !ok {
		return ErrAntigravityNoUsableProxy
	}

	interval := time.Duration(ConfiguredAntigravitySettings().GetQuotaRefreshIntervalMinutes()) * time.Minute
	ttlSeconds := antigravityQuotaSummaryTTLSeconds(interval)

	seq, epoch, err := s.db.ReserveAntigravityQuotaSource(ctx, row.ID, antigravityQuotaSummarySource, row.CredentialGeneration, familyScope)
	if err != nil {
		return fmt.Errorf("reserve Antigravity quota summary slot account=%d: %w", dbID, err)
	}
	// 观测时标取预约事务提交的那一刻（Spec §3.1）：网络往返耗时不得刷新它，否则
	// TTL/reset 基线会随延迟漂移，覆盖窗判定也会跟着漂。
	observedAt := time.Now().UTC()

	client, err := NewAntigravityClient(proxyURL)
	if err != nil {
		return fmt.Errorf("build Antigravity client for account %d: %w", dbID, err)
	}
	groups, ok, failure := client.fetchQuotaSummary(ctx, row.GetCredential("access_token"), row.GetCredential("project_id"))
	if !ok {
		// 只带状态码与已脱敏的消息：绝不把 token 或整段响应正文写进日志/错误。
		return fmt.Errorf("fetch Antigravity quota summary account=%d status=%d message=%s", dbID, failure.Status, failure.Message)
	}
	if len(groups) == 0 {
		return fmt.Errorf("%w: account %d returned no quota groups", ErrAntigravityQuotaSummaryIncomplete, dbID)
	}

	var prev *AntigravityQuotaSnapshot
	if raw := strings.TrimSpace(row.GetCredential("antigravity_quota")); raw != "" {
		var snapshot AntigravityQuotaSnapshot
		if err := json.Unmarshal([]byte(raw), &snapshot); err == nil {
			prev = &snapshot
		}
	}
	// 非空且成功解析的 summary 即完整权威清单（Spec §3.2）：未返回的桶允许删除。
	summaryIntegrity := true
	next, advanced := ApplyQuotaGroupsV2(prev, groups, summaryIntegrity, observedAt, ttlSeconds, seq)
	if !advanced {
		return fmt.Errorf("%w: account %d summary did not advance the snapshot", ErrAntigravityQuotaSummaryIncomplete, dbID)
	}
	next.UpdatedAt = observedAt
	if next.SourceMetadata == nil {
		next.SourceMetadata = make(map[string]SourceMeta, 1)
	}
	meta := next.SourceMetadata[antigravityQuotaSummarySource]
	meta.Producer = antigravityQuotaRefreshOpType
	meta.Trigger = antigravityQuotaRefreshOpType
	meta.CredentialFamilyID = familyScope
	meta.QuotaIdentityScope = familyScope
	meta.Generation = row.CredentialGeneration
	// applied_seq 与提交栅栏即将写入的值同源（本枚预约序号），内存投影因此与
	// durable 行同口径可信（硬门禁要求 applied_seq > 0 且 observed_at 非零）。
	meta.AppliedSeq = seq
	meta.ObservedAt = observedAt
	meta.SummaryIntegrity = summaryIntegrity
	meta.PresenceVerified = summaryIntegrity
	next.SourceMetadata[antigravityQuotaSummarySource] = meta

	raw, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("encode Antigravity quota snapshot account=%d: %w", dbID, err)
	}

	applied, err := s.db.CommitAntigravityQuotaSource(ctx, row.ID, antigravityQuotaSummarySource, seq, epoch, row.CredentialGeneration, familyScope, string(raw))
	if err != nil {
		return fmt.Errorf("commit Antigravity quota summary account=%d: %w", dbID, err)
	}
	if !applied {
		// Latest-Success-Wins 拒绝：更晚预约的观测已落库，本次结果作废即正确结局。
		return nil
	}

	account.mu.Lock()
	if account.CredentialGeneration == row.CredentialGeneration {
		account.applyAntigravityQuotaSchedulingLocked(string(raw))
	}
	account.mu.Unlock()
	s.fastSchedulerUpdate(account)
	s.notifySchedulerAvailability()
	return nil
}
