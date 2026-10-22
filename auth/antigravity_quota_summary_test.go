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

// quotaSourceMetaFromPersisted 解析落库 antigravity_quota 里 groups 来源的 applied_seq
// 与 observed_at，用于直接断言 durable 行的协议簿记。
func quotaSourceMetaFromPersisted(t *testing.T, raw string) (int64, time.Time) {
	t.Helper()
	var doc struct {
		SourceMetadata struct {
			Groups struct {
				AppliedSeq int64     `json:"applied_seq"`
				ObservedAt time.Time `json:"observed_at"`
			} `json:"groups"`
		} `json:"source_metadata"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &doc); err != nil {
		t.Fatalf("decode persisted antigravity_quota %q: %v", raw, err)
	}
	return doc.SourceMetadata.Groups.AppliedSeq, doc.SourceMetadata.Groups.ObservedAt
}

// TestRefreshAntigravityQuotaSummaryObservedAtIsReservationTime 断言观测时标取自预约
// 事务（Spec §3.1）：慢响应（这里 1.2s）绝不把它一起拖后，否则 TTL/reset 基线与覆盖窗
// 判定都会随网络延迟漂移。
func TestRefreshAntigravityQuotaSummaryObservedAtIsReservationTime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summary" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(1200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, antigravityQuotaSummaryBody(time.Now().UTC().Add(2*time.Hour)))
	}))
	defer server.Close()
	useAntigravityTestEndpoints(t, server.URL)

	store, db, account, accountID := newAntigravityRefreshTestAccount(t, nil)
	ctx := context.Background()

	started := time.Now().UTC()
	if err := store.RefreshAntigravityQuotaSummary(ctx, account); err != nil {
		t.Fatalf("RefreshAntigravityQuotaSummary: %v", err)
	}
	completed := time.Now().UTC()
	if elapsed := completed.Sub(started); elapsed < time.Second {
		t.Fatalf("the slow-response fixture did not take effect (elapsed=%v); this guard would be vacuous", elapsed)
	}

	row, err := db.GetAccountByID(ctx, accountID)
	if err != nil {
		t.Fatalf("reload account row: %v", err)
	}
	_, observedAt := quotaSourceMetaFromPersisted(t, row.GetCredential("antigravity_quota"))
	if observedAt.IsZero() {
		t.Fatal("persisted groups metadata must carry observed_at")
	}
	if observedAt.After(completed) {
		t.Fatalf("observed_at %v is later than the response completion %v", observedAt, completed)
	}
	if lag := completed.Sub(observedAt); lag < time.Second {
		t.Fatalf("observed_at must be the reservation instant, not the response instant: observed_at=%v sits only %v before completion %v",
			observedAt, lag, completed)
	}
	if early := started.Sub(observedAt); early > time.Second {
		t.Fatalf("observed_at %v predates the refresh call %v by %v", observedAt, started, early)
	}
}

// TestRefreshAntigravityQuotaSummaryRejectedCommitReturnsNil 断言 Latest-Success-Wins
// 拒绝（applied=false）是正常结局而不是失败：本方观测被更晚预约的观测覆盖时返回 nil，
// 调用方据此绝不触发退避。
func TestRefreshAntigravityQuotaSummaryRejectedCommitReturnsNil(t *testing.T) {
	store, db, account, accountID := newAntigravityRefreshTestAccount(t, nil)
	ctx := context.Background()

	var hijacked atomic.Bool
	var hijackSeq atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/summary" {
			http.NotFound(w, r)
			return
		}
		// 在 refresh 仍停留于网络阶段时，抢先预约并提交一枚更晚序号的观测：refresh
		// 自己随后拿着更小的序号提交，必然被 applied_seq 栅栏拒绝。
		if !hijacked.Swap(true) {
			row, err := db.GetAccountByID(context.Background(), accountID)
			if err != nil {
				t.Errorf("reload account row inside the summary window: %v", err)
				http.Error(w, "fixture reload failed", http.StatusInternalServerError)
				return
			}
			family := row.GetCredential("credential_family_id")
			seq, epoch, err := db.ReserveAntigravityQuotaSource(context.Background(), accountID, "groups", row.CredentialGeneration, family)
			if err != nil {
				t.Errorf("fixture reservation: %v", err)
				http.Error(w, "fixture reservation failed", http.StatusInternalServerError)
				return
			}
			applied, err := db.CommitAntigravityQuotaSource(context.Background(), accountID, "groups", seq, epoch, row.CredentialGeneration, family, `{"schema_version":2}`)
			if err != nil || !applied {
				t.Errorf("fixture commit applied=%v err=%v", applied, err)
				http.Error(w, "fixture commit failed", http.StatusInternalServerError)
				return
			}
			hijackSeq.Store(seq)
		}
		// 让被抢提交真正先于本方提交落库。
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, antigravityQuotaSummaryBody(time.Now().UTC().Add(2*time.Hour)))
	}))
	defer server.Close()
	useAntigravityTestEndpoints(t, server.URL)

	if err := store.RefreshAntigravityQuotaSummary(ctx, account); err != nil {
		t.Fatalf("a Latest-Success-Wins rejection must not surface as a failure, got %v", err)
	}
	if seq := hijackSeq.Load(); seq == 0 {
		t.Fatal("the fixture never hijacked the commit window")
	}

	row, err := db.GetAccountByID(ctx, accountID)
	if err != nil {
		t.Fatalf("reload account row: %v", err)
	}
	appliedSeq, _ := quotaSourceMetaFromPersisted(t, row.GetCredential("antigravity_quota"))
	if appliedSeq != hijackSeq.Load() {
		t.Fatalf("the rejected commit must not move applied_seq: durable=%d winner=%d", appliedSeq, hijackSeq.Load())
	}
}

// TestRefreshAntigravityQuotaSummaryRejectsAccountWithoutFamilyScope 断言缺少凭据
// family 身份时 fail-closed：OAuth 账号的正常路径必有 family，身份不明的账号一律不
// 预约、不联网（否则配额观测会写进身份不明的 durable 行）。
func TestRefreshAntigravityQuotaSummaryRejectsAccountWithoutFamilyScope(t *testing.T) {
	var summaryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summaryCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, antigravityQuotaSummaryBody(time.Now().UTC().Add(2*time.Hour)))
	}))
	defer server.Close()
	useAntigravityTestEndpoints(t, server.URL)

	store, db, account, accountID := newAntigravityRefreshTestAccount(t, map[string]any{"credential_family_id": ""})
	row, err := db.GetAccountByID(context.Background(), accountID)
	if err != nil {
		t.Fatalf("reload account row: %v", err)
	}
	if family := strings.TrimSpace(row.GetCredential("credential_family_id")); family != "" {
		t.Fatalf("fixture must carry no credential family, got %q", family)
	}

	err = store.RefreshAntigravityQuotaSummary(context.Background(), account)
	if err == nil {
		t.Fatal("an account without a credential family must be rejected fail-closed")
	}
	if calls := summaryCalls.Load(); calls != 0 {
		t.Fatalf("the fail-closed path must not reach the network, summary calls=%d", calls)
	}
}
