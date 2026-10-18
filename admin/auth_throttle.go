package admin

import (
	"sync"
	"time"
)

const (
	adminAuthFailureWindow = 10 * time.Minute
	adminAuthBlockDuration = 10 * time.Minute
	adminAuthMaxFailures   = 5
	adminAuthMaxEntries    = 4096
)

// adminAuthThrottle 记录按 IP 的管理端鉴权失败次数。
// 只在失败时计数，成功请求不受影响；窗口内失败达到阈值后阻断一段时间。
type adminAuthThrottle struct {
	mu      sync.Mutex
	entries map[string]*adminAuthThrottleEntry
}

type adminAuthThrottleEntry struct {
	failures     int
	windowStart  time.Time
	blockedUntil time.Time
	lastSeen     time.Time
}

func newAdminAuthThrottle() *adminAuthThrottle {
	return &adminAuthThrottle{entries: make(map[string]*adminAuthThrottleEntry)}
}

// retryAfter 返回该 IP 剩余的阻断时间；未阻断时返回 0。
func (t *adminAuthThrottle) retryAfter(ip string, now time.Time) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[ip]
	if entry == nil || !now.Before(entry.blockedUntil) {
		return 0
	}
	return entry.blockedUntil.Sub(now)
}

// recordFailure 记录一次失败；达到阈值时开始阻断并返回阻断时长。
func (t *adminAuthThrottle) recordFailure(ip string, now time.Time) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	entry := t.entries[ip]
	if entry == nil || now.Sub(entry.windowStart) > adminAuthFailureWindow {
		// 新来源也要触发清理：攻击者可以给大量 IP 各留 1–4 次失败，
		// 只在达到阻断阈值时清理会让表无限增长。
		t.evictLocked(now)
		entry = &adminAuthThrottleEntry{windowStart: now}
		t.entries[ip] = entry
	}
	entry.lastSeen = now
	if now.Before(entry.blockedUntil) {
		return entry.blockedUntil.Sub(now)
	}
	entry.failures++
	if entry.failures < adminAuthMaxFailures {
		return 0
	}
	entry.failures = 0
	entry.windowStart = now
	entry.blockedUntil = now.Add(adminAuthBlockDuration)
	return adminAuthBlockDuration
}

func (t *adminAuthThrottle) reset(ip string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.entries, ip)
	t.mu.Unlock()
}

// evictLocked 在写入新条目之前清理，保证插入后不超过上限。
func (t *adminAuthThrottle) evictLocked(now time.Time) {
	if len(t.entries) < adminAuthMaxEntries {
		return
	}
	for ip, entry := range t.entries {
		if !now.Before(entry.blockedUntil) && now.Sub(entry.lastSeen) > adminAuthFailureWindow {
			delete(t.entries, ip)
		}
	}
	for len(t.entries) >= adminAuthMaxEntries {
		var oldestIP string
		var oldest time.Time
		for ip, entry := range t.entries {
			if oldestIP == "" || entry.lastSeen.Before(oldest) {
				oldestIP, oldest = ip, entry.lastSeen
			}
		}
		delete(t.entries, oldestIP)
	}
}
