package database

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// insertUsageLogs 往缓冲里塞 count 条最简用量日志。
func insertUsageLogs(t *testing.T, db *DB, count int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < count; i++ {
		if err := db.InsertUsageLog(ctx, &UsageLogInput{
			Endpoint:    "/v1/responses",
			Model:       "gpt-5.4",
			StatusCode:  200,
			TotalTokens: 10,
		}); err != nil {
			t.Fatalf("InsertUsageLog(%d) 返回错误: %v", i, err)
		}
	}
}

// TestFlushUsageLogsDrainsBeyondOneBatch 覆盖 flush 的分批语义：flushLogs 每次只取
// usage_log_batch_size 条，FlushUsageLogs 必须循环刷完整个缓冲。
func TestFlushUsageLogsDrainsBeyondOneBatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	// 停掉后台 flusher，让刷新时机完全由用例控制。
	close(db.logStop)
	db.logWg.Wait()
	defer db.conn.Close()

	const batchSize = 10
	const total = 35
	db.SetUsageLogConfig(UsageLogModeFull, batchSize, maxUsageLogFlushIntervalSeconds)
	insertUsageLogs(t, db, total)

	db.flushLogs()
	logs, err := db.ListRecentUsageLogs(context.Background(), total*2)
	if err != nil {
		t.Fatalf("ListRecentUsageLogs 返回错误: %v", err)
	}
	if len(logs) != batchSize {
		t.Fatalf("flushLogs 后落库 %d 条，want %d（单次只应刷一个批次）", len(logs), batchSize)
	}

	db.FlushUsageLogs()
	logs, err = db.ListRecentUsageLogs(context.Background(), total*2)
	if err != nil {
		t.Fatalf("ListRecentUsageLogs 返回错误: %v", err)
	}
	if len(logs) != total {
		t.Fatalf("FlushUsageLogs 后落库 %d 条，want %d", len(logs), total)
	}
	if stats := db.GetUsageLogRuntimeStats(); stats.BufferLength != 0 {
		t.Fatalf("BufferLength = %d，want 0", stats.BufferLength)
	}
}

// TestCloseFlushesEntireUsageLogBuffer 覆盖优雅关闭：Close 会先停掉后台 flusher，
// 此时再走「只刷一个批次 + notifyLogFlush」的路径没人消费信号，超出一个批次的日志
// 会被静默丢弃，所以收尾必须刷完整个缓冲。
func TestCloseFlushesEntireUsageLogBuffer(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}

	const total = 35
	db.SetUsageLogConfig(UsageLogModeFull, maxUsageLogBatchSize, maxUsageLogFlushIntervalSeconds)
	insertUsageLogs(t, db, total)
	db.SetUsageLogConfig(UsageLogModeFull, 10, maxUsageLogFlushIntervalSeconds)

	if err := db.Close(); err != nil {
		t.Fatalf("Close 返回错误: %v", err)
	}

	reopened, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("重新打开数据库返回错误: %v", err)
	}
	defer reopened.Close()

	logs, err := reopened.ListRecentUsageLogs(context.Background(), total*2)
	if err != nil {
		t.Fatalf("ListRecentUsageLogs 返回错误: %v", err)
	}
	if len(logs) != total {
		t.Fatalf("Close 后落库 %d 条，want %d（缓冲里的日志被丢了）", len(logs), total)
	}
}

// TestUsageLogTextClampedToColumnWidth 覆盖超长字段截断：reasoning_effort / service_tier
// 等直接来自下游请求体，超过列宽会让整条批量 INSERT 回滚，失败批次又被放回缓冲区头部，
// 一条脏数据就能永久堵死日志写入。
func TestUsageLogTextClampedToColumnWidth(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "codex2api.db")
	db, err := New("sqlite", dbPath)
	if err != nil {
		t.Fatalf("New(sqlite) 返回错误: %v", err)
	}
	defer db.Close()

	long := strings.Repeat("x", 300)
	if err := db.InsertUsageLog(context.Background(), &UsageLogInput{
		Endpoint:             long,
		Model:                long,
		EffectiveModel:       long,
		ReasoningEffort:      long,
		ServiceTier:          long,
		RequestedServiceTier: long,
		ActualServiceTier:    long,
		Channel:              long,
		StatusCode:           200,
	}); err != nil {
		t.Fatalf("InsertUsageLog 返回错误: %v", err)
	}
	db.FlushUsageLogs()

	logs, err := db.ListRecentUsageLogs(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecentUsageLogs 返回错误: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("len(logs) = %d, want 1", len(logs))
	}
	got := logs[0]
	for _, tc := range []struct {
		name  string
		value string
		max   int
	}{
		{"endpoint", got.Endpoint, usageLogTextMaxLen},
		{"model", got.Model, usageLogTextMaxLen},
		{"effective_model", got.EffectiveModel, usageLogTextMaxLen},
		{"reasoning_effort", got.ReasoningEffort, usageLogTextMaxLen},
		{"service_tier", got.ServiceTier, usageLogTextMaxLen},
		{"requested_service_tier", got.RequestedServiceTier, usageLogTextMaxLen},
		{"actual_service_tier", got.ActualServiceTier, usageLogTextMaxLen},
		{"channel", got.Channel, usageLogChannelMaxLen},
	} {
		if len(tc.value) != tc.max {
			t.Fatalf("%s 长度 = %d，want %d", tc.name, len(tc.value), tc.max)
		}
	}
}

func TestClampUsageLogText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"未超长原样返回", "high", 100, "high"},
		{"刚好等于上限", "abcd", 4, "abcd"},
		{"按字符截断", "abcdef", 4, "abcd"},
		{"多字节按字符而非字节截断", "思考强度很高", 3, "思考强"},
		{"上限非正数不截断", "abc", 0, "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampUsageLogText(tc.in, tc.max); got != tc.want {
				t.Fatalf("clampUsageLogText(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

// TestUsageLogInsertRowsStayUnderPostgresBindLimit 锁住每条 INSERT 的行数上限：
// 行数 * 每行列数必须低于 PostgreSQL 的 65535 个 bind 参数。
func TestUsageLogInsertRowsStayUnderPostgresBindLimit(t *testing.T) {
	if maxUsageLogInsertRowsPerSQL*usageLogInsertColumnCount > postgresMaxBindParams {
		t.Fatalf("单条 INSERT 参数数 = %d，超过 PostgreSQL 上限 %d",
			maxUsageLogInsertRowsPerSQL*usageLogInsertColumnCount, postgresMaxBindParams)
	}
	if maxUsageLogBatchSize*usageLogInsertColumnCount <= postgresMaxBindParams {
		t.Logf("提示：当前 batch size 上限 %d 即使不分片也不会超参数上限", maxUsageLogBatchSize)
	}
}
