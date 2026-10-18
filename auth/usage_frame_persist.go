package auth

import (
	"context"
	"log"
	"sync"
	"time"
)

// usageFramePersistJob is a snapshot of the fields needed to persist one
// frame-driven usage observation. Values are captured at enqueue time so the
// write is coherent even if newer frames arrive while it is queued.
type usageFramePersistJob struct {
	dbID        int64
	observedAt  time.Time
	pct5h       float64
	reset5hAt   time.Time
	has5h       bool
	updatedAt5h time.Time
	pct7d       float64
	reset7dAt   time.Time
	has7d       bool
}

// usageFramePersistQueue coalesces frame snapshots per account and writes them
// on a worker goroutine. The upstream read path only captures and enqueues, so
// a slow or locked database cannot stall a streaming response.
type usageFramePersistQueue struct {
	mu      sync.Mutex
	pending map[int64]usageFramePersistJob
	wake    chan struct{}
}

func newUsageFramePersistQueue() *usageFramePersistQueue {
	return &usageFramePersistQueue{
		pending: make(map[int64]usageFramePersistJob),
		wake:    make(chan struct{}, 1),
	}
}

func (q *usageFramePersistQueue) start(s *Store) {
	if q == nil || s == nil || s.db == nil {
		return
	}
	s.wg.Add(1)
	go q.run(s)
}

func (q *usageFramePersistQueue) run(s *Store) {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			// Usage snapshots are a cache that WHAM can rebuild; drop pending
			// jobs instead of delaying shutdown on a blocked database.
			return
		case <-q.wake:
			for {
				job, ok := q.take()
				if !ok {
					break
				}
				q.persist(s, job)
			}
		}
	}
}

func (q *usageFramePersistQueue) enqueue(job usageFramePersistJob) {
	q.mu.Lock()
	q.pending[job.dbID] = job
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *usageFramePersistQueue) take() (usageFramePersistJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for id, job := range q.pending {
		delete(q.pending, id)
		return job, true
	}
	return usageFramePersistJob{}, false
}

func (q *usageFramePersistQueue) persist(s *Store, job usageFramePersistJob) {
	if s == nil || s.db == nil || job.dbID == 0 {
		return
	}
	acc := s.FindByID(job.dbID)
	if acc == nil {
		return
	}
	// A WHAM probe or a newer frame may have been applied after this job was
	// queued; in that case the newer observation (and its own persistence path)
	// owns the snapshot. Writing this older job would roll usage back.
	acc.mu.RLock()
	superseded := acc.usageObservedAt.After(job.observedAt)
	acc.mu.RUnlock()
	if superseded {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	switch {
	case job.has7d && job.has5h:
		err = s.db.UpdateUsageSnapshotFull(ctx, job.dbID, job.pct7d, job.reset7dAt, job.pct5h, job.reset5hAt, job.observedAt, job.updatedAt5h)
	case job.has7d:
		err = s.db.UpdateUsageSnapshot(ctx, job.dbID, job.pct7d, job.observedAt)
	case job.has5h:
		err = s.db.UpdateUsageSnapshot5h(ctx, job.dbID, job.pct5h, job.reset5hAt, job.observedAt)
	default:
		return
	}
	if err != nil {
		// Leave the throttle marker untouched; the next frame observation sees
		// that no successful write happened and enqueues again.
		log.Printf("[账号 %d] 异步持久化用量快照失败: %v", job.dbID, err)
		return
	}
	acc.MarkUsageFramePersisted(job.observedAt, job.pct5h, job.has5h, job.pct7d, job.has7d)
}

// EnqueueUsageFramePersist schedules a frame snapshot write off the upstream
// read path. The latest snapshot per account wins while a write is queued.
func (s *Store) EnqueueUsageFramePersist(acc *Account, observedAt time.Time) {
	if s == nil || acc == nil || s.usageFramePersist == nil || acc.DBID == 0 {
		return
	}
	select {
	case <-s.stopCh:
		return
	default:
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	snap := acc.GetAccountLiveUsageSnapshot()
	if !snap.UsagePercent5hValid && !snap.UsagePercent7dValid {
		return
	}
	s.usageFramePersist.enqueue(usageFramePersistJob{
		dbID:        acc.DBID,
		observedAt:  observedAt,
		pct5h:       snap.UsagePercent5h,
		reset5hAt:   snap.Reset5hAt,
		has5h:       snap.UsagePercent5hValid,
		updatedAt5h: snap.UsageUpdatedAt5h,
		pct7d:       snap.UsagePercent7d,
		reset7dAt:   snap.Reset7dAt,
		has7d:       snap.UsagePercent7dValid,
	})
}
