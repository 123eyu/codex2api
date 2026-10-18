package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/codex2api/database"
	"github.com/tidwall/gjson"
)

// TestUsageFramePersistSkipsSupersededJob verifies the worker re-checks the
// observation order under the snapshot-write lock: a queued older frame must
// not overwrite a newer WHAM/frame observation that was applied while the job
// waited for the database.
func TestUsageFramePersistSkipsSupersededJob(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "superseded.db")
	db, err := database.New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("database.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	id, err := db.InsertAccountWithCredentials(ctx, "superseded", map[string]any{
		"access_token": "at-test", "plan_type": "plus",
	}, "")
	if err != nil {
		t.Fatalf("InsertAccountWithCredentials: %v", err)
	}
	store := NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2})
	t.Cleanup(store.Stop)
	account := &Account{DBID: id, AccessToken: "at-test", PlanType: "plus", Status: StatusReady}
	store.AddAccount(account)

	t0 := time.Now().Add(-time.Minute)
	account.SetUsageSnapshot(10, t0)

	// Block the worker inside the write lock so the newer observation is
	// applied before its superseded check runs.
	store.usageSnapshotPersistMu.Lock()
	store.EnqueueUsageFramePersist(account, t0)
	t1 := t0.Add(30 * time.Second)
	if !account.ApplyUsageObservation(t1, func() { account.SetUsageSnapshot(80, t1) }) {
		t.Fatal("newer observation was rejected")
	}
	store.usageSnapshotPersistMu.Unlock()

	time.Sleep(200 * time.Millisecond)
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = conn.Close() }()
	var raw string
	if err := conn.QueryRow(`SELECT credentials FROM accounts WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	if got := gjson.Get(raw, "codex_7d_used_percent").Float(); got == 10 {
		t.Fatalf("superseded job wrote the older snapshot: %v", got)
	}
}
