package admin

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
)

var (
	errWhamDailyUsageUnavailable  = errors.New("官方用量统计不可用")
	errWhamDailyUsageUnauthorized = errors.New("上游鉴权失败（401），请先刷新账号后重试")
	errWhamDailyUsageRateLimited  = errors.New("上游限流（429），请稍后重试")
)

func upstreamDailyUsageError(status int, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 200 {
		snippet = snippet[:200]
	}
	if snippet == "" {
		return fmt.Errorf("上游返回 %d", status)
	}
	return fmt.Errorf("上游返回 %d：%s", status, snippet)
}

const (
	// whamDailyUsageProbeInterval 是快照轮次间隔。上游保留 7 天且当天数据未结算，
	// 6 小时一轮既能当天多次刷新，也远低于「漏跑到数据过期」的风险。
	whamDailyUsageProbeInterval = 6 * time.Hour

	// whamDailyUsageProbeConcurrency 限制并发，避免大号池同时打上游。
	whamDailyUsageProbeConcurrency = 4

	// whamDailyUsageKeepDays 是本地快照保留期。上游只有 7 天，本地留一年足够看趋势。
	whamDailyUsageKeepDays = 365
)

var whamDailyUsageProbeOnce sync.Once

// StartWhamDailyUsageProbe 周期性把官方结算用量落库。
//
// 上游 daily-workspace-usage-counts 只保留 7 天，过期即永久丢失，所以这个任务是
// 长期历史的唯一来源；漏跑超过 7 天就会留下永久空洞。
func (h *Handler) StartWhamDailyUsageProbe(ctx context.Context) {
	if h == nil || h.db == nil || h.store == nil {
		return
	}
	whamDailyUsageProbeOnce.Do(func() {
		go func() {
			// 启动后先等一会：避开进程刚起来时的账号加载与刷新高峰。
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Minute):
			}
			h.runWhamDailyUsageProbe(ctx)
			ticker := time.NewTicker(whamDailyUsageProbeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					h.runWhamDailyUsageProbe(ctx)
				}
			}
		}()
	})
}

func (h *Handler) runWhamDailyUsageProbe(ctx context.Context) {
	targets := h.whamDailyUsageProbeTargets()
	if len(targets) == 0 {
		return
	}
	started := time.Now()
	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		okCount   int
		failCount int
	)
	sem := make(chan struct{}, whamDailyUsageProbeConcurrency)
	for _, account := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(acc *auth.Account) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			_, err := h.syncWhamDailyUsage(reqCtx, acc)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failCount++
				// 401 在这里不裁决账号状态：wham 侧鉴权失败不代表账号不可用
				// （与 usage 探针的既有语义一致），只记录等下一轮。
				log.Printf("官方用量快照失败 account=%d: %v", acc.DBID, err)
				return
			}
			okCount++
		}(account)
	}
	wg.Wait()

	if okCount > 0 {
		pruneCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if err := h.db.PruneAccountDailyUsage(pruneCtx, whamDailyUsageKeepDays); err != nil {
			log.Printf("清理官方用量快照失败: %v", err)
		}
		cancel()
	}
	log.Printf("官方用量快照完成 成功=%d 失败=%d 耗时=%s", okCount, failCount, time.Since(started).Round(time.Millisecond))
}

// whamDailyUsageProbeTargets 挑出该拉统计的账号：只有 OAuth 登录的 Codex 账号
// 才有 wham 端点权限，中转/Grok 账号没有。
func (h *Handler) whamDailyUsageProbeTargets() []*auth.Account {
	if h == nil || h.store == nil {
		return nil
	}
	accounts := h.store.Accounts()
	out := make([]*auth.Account, 0, len(accounts))
	for _, account := range accounts {
		if account == nil || account.DBID <= 0 {
			continue
		}
		// 中转账号(Responses API / Grok)走的不是 ChatGPT 后端，没有 wham 端点。
		if account.IsOpenAIResponsesAPI() || account.IsGrokAPI() {
			continue
		}
		if account.GetAccessToken() == "" {
			continue
		}
		out = append(out, account)
	}
	return out
}
