package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// antigravityQuotaSummaryBody 生成一份最小可用的 groups summary 响应：
// weekly 已知零 + 未来 resetTime（V2 presence 齐备，可参与硬门禁）。
func antigravityQuotaSummaryBody(resetAt time.Time) string {
	return fmt.Sprintf(`{"groups":[{"displayName":"Weekly","buckets":[{"window":"weekly","remainingFraction":0,"resetTime":%q}]}]}`,
		resetAt.UTC().Format(time.RFC3339))
}

// antigravityQuotaFactsForTest 把落库的 antigravity_quota 文本解析成可比较的文档，
// 并剔除预约/提交协议自行维护的时间与序号簿记（current_attempt_seq / last_attempt_at
// / last_success_at）：预约事务本身必然推进它们，「不被清空」断言只针对配额事实本体。
func antigravityQuotaFactsForTest(t *testing.T, raw string) map[string]any {
	t.Helper()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode persisted antigravity_quota %q: %v", raw, err)
	}
	if meta, ok := doc["source_metadata"].(map[string]any); ok {
		if groups, ok := meta["groups"].(map[string]any); ok {
			delete(groups, "current_attempt_seq")
			delete(groups, "last_attempt_at")
			delete(groups, "last_success_at")
		}
	}
	return doc
}

func TestRefreshAntigravityQuotaSummaryPersistsAndPublishes(t *testing.T) {
	var summaryCalls atomic.Int32
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/summary":
			summaryCalls.Add(1)
			_, _ = io.WriteString(w, antigravityQuotaSummaryBody(time.Now().UTC().Add(2*time.Hour)))
		case "/token":
			// 只读 summary 绝不允许触发换票（轮换 RT 属全量同步职责）。
			tokenCalls.Add(1)
			http.Error(w, `{"error":"token endpoint must not be reached"}`, http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	useAntigravityTestEndpoints(t, server.URL)

	store, db, account, accountID := newAntigravityRefreshTestAccount(t, nil)
	ctx := context.Background()
	epochBefore, err := db.GetAntigravityQuotaEpoch(ctx, accountID)
	if err != nil {
		t.Fatalf("read quota epoch: %v", err)
	}

	if err := store.RefreshAntigravityQuotaSummary(ctx, account); err != nil {
		t.Fatalf("RefreshAntigravityQuotaSummary: %v", err)
	}
	if calls := summaryCalls.Load(); calls != 1 {
		t.Fatalf("expected exactly one summary request, got %d", calls)
	}
	if calls := tokenCalls.Load(); calls != 0 {
		t.Fatalf("summary refresh must never rotate tokens, token endpoint calls=%d", calls)
	}

	row, err := db.GetAccountByID(ctx, accountID)
	if err != nil {
		t.Fatalf("reload account row: %v", err)
	}
	raw := row.GetCredential("antigravity_quota")
	if !strings.Contains(raw, `"schema_version":2`) {
		t.Fatalf("persisted snapshot must be V2, got %s", raw)
	}
	if !strings.Contains(raw, `"source_metadata"`) || !strings.Contains(raw, `"groups"`) {
		t.Fatalf("persisted snapshot must carry groups source metadata, got %s", raw)
	}
	if got := row.GetCredential("access_token"); got != "access-old" {
		t.Fatalf("summary refresh must not touch the access token, got %q", got)
	}

	epochAfter, err := db.GetAntigravityQuotaEpoch(ctx, accountID)
	if err != nil {
		t.Fatalf("read quota epoch after refresh: %v", err)
	}
	if epochAfter != epochBefore {
		t.Fatalf("summary refresh must not advance the quota epoch, before=%d after=%d", epochBefore, epochAfter)
	}

	account.mu.RLock()
	groups := len(account.AntigravityQuota.Groups)
	account.mu.RUnlock()
	if groups == 0 {
		t.Fatalf("runtime projection must publish quota groups, snapshot=%+v", account.AntigravityQuota)
	}
}

func TestRefreshAntigravityQuotaSummaryEmptyResponseKeepsPrevious(t *testing.T) {
	var body atomic.Value
	body.Store(antigravityQuotaSummaryBody(time.Now().UTC().Add(2 * time.Hour)))
	var summaryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summary" {
			http.NotFound(w, r)
			return
		}
		summaryCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer server.Close()
	useAntigravityTestEndpoints(t, server.URL)

	store, db, account, accountID := newAntigravityRefreshTestAccount(t, nil)
	ctx := context.Background()
	if err := store.RefreshAntigravityQuotaSummary(ctx, account); err != nil {
		t.Fatalf("first RefreshAntigravityQuotaSummary: %v", err)
	}

	row, err := db.GetAccountByID(ctx, accountID)
	if err != nil {
		t.Fatalf("reload account row: %v", err)
	}
	before := antigravityQuotaFactsForTest(t, row.GetCredential("antigravity_quota"))
	if len(before) == 0 {
		t.Fatal("expected persisted quota facts after the first successful refresh")
	}

	body.Store(`{"groups":[]}`)
	err = store.RefreshAntigravityQuotaSummary(ctx, account)
	if !errors.Is(err, ErrAntigravityQuotaSummaryIncomplete) {
		t.Fatalf("empty summary must return ErrAntigravityQuotaSummaryIncomplete, got %v", err)
	}
	if calls := summaryCalls.Load(); calls != 2 {
		t.Fatalf("expected two summary requests, got %d", calls)
	}

	row, err = db.GetAccountByID(ctx, accountID)
	if err != nil {
		t.Fatalf("reload account row after empty summary: %v", err)
	}
	after := antigravityQuotaFactsForTest(t, row.GetCredential("antigravity_quota"))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("empty summary must not rewrite persisted quota facts:\nbefore=%v\nafter =%v", before, after)
	}
	if raw := row.GetCredential("antigravity_quota"); raw == "" {
		t.Fatal("empty summary must not clear the persisted snapshot")
	}
}

func TestRefreshAntigravityQuotaSummarySkipsDisabledAccount(t *testing.T) {
	var summaryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summaryCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, antigravityQuotaSummaryBody(time.Now().UTC().Add(2*time.Hour)))
	}))
	defer server.Close()
	useAntigravityTestEndpoints(t, server.URL)

	store, _, account, _ := newAntigravityRefreshTestAccount(t, nil)
	atomic.StoreInt32(&account.Disabled, 1)
	defer atomic.StoreInt32(&account.Disabled, 0)

	err := store.RefreshAntigravityQuotaSummary(context.Background(), account)
	if err == nil {
		t.Fatal("disabled account must not be refreshed")
	}
	if calls := summaryCalls.Load(); calls != 0 {
		t.Fatalf("disabled account must not reach the network, summary calls=%d", calls)
	}
}
