package auth

import (
	"context"
	"log"
	"time"
)

// persistUsageCooldown retries a failed usage-owned cooldown write without
// treating the retry as a new failure or changing the runtime deadline.
func (s *Store) persistUsageCooldown(acc *Account) {
	if s == nil || s.db == nil || acc == nil {
		return
	}
	acc.usageCooldownPersistMu.Lock()
	defer acc.usageCooldownPersistMu.Unlock()
	acc.mu.RLock()
	pending := acc.usageCooldownPersistPending && acc.Status == StatusCooldown
	reason, until := acc.CooldownReason, acc.CooldownUtil
	acc.mu.RUnlock()
	if !pending {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.db.SetCooldown(ctx, acc.DBID, reason, until); err != nil {
		log.Printf("[账号 %d] 持久化用量冷却失败: %v", acc.DBID, err)
		return
	}
	acc.mu.Lock()
	if acc.Status == StatusCooldown && acc.CooldownReason == reason && acc.CooldownUtil.Equal(until) {
		acc.usageCooldownPersistPending = false
	}
	acc.mu.Unlock()
}
