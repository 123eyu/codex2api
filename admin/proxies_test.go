package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
)

func newAdminProxyTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "codex2api.db"))
	if err != nil {
		t.Fatalf("database.New(sqlite) returned error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newAdminProxyTestStore(t *testing.T, db *database.DB) *auth.Store {
	t.Helper()
	store := auth.NewStore(db, nil, nil)
	store.SetProxyPoolEnabled(true)
	if err := store.ReloadProxyPool(); err != nil {
		t.Fatalf("ReloadProxyPool returned error: %v", err)
	}
	t.Cleanup(store.Stop)
	return store
}

func TestPersistProxyTestResultRefreshesRuntimePool(t *testing.T) {
	db := newAdminProxyTestDB(t)
	ctx := context.Background()
	id, err := db.InsertProxy(ctx, "http://proxy.example:8080", "")
	if err != nil {
		t.Fatalf("InsertProxy returned error: %v", err)
	}
	store := newAdminProxyTestStore(t, db)
	handler := &Handler{db: db, store: store}

	if err := handler.persistProxyTestResult(ctx, id, database.ProxyTestStatusError, "", "", 0); err != nil {
		t.Fatalf("persistProxyTestResult(error) returned error: %v", err)
	}
	if got := store.NextProxy(); got != "" {
		t.Fatalf("NextProxy after error = %q, want empty", got)
	}

	if err := handler.persistProxyTestResult(ctx, id, database.ProxyTestStatusSuccess, "1.2.3.4", "US", 100); err != nil {
		t.Fatalf("persistProxyTestResult(success) returned error: %v", err)
	}
	if got := store.NextProxy(); got != "http://proxy.example:8080" {
		t.Fatalf("NextProxy after successful retest = %q, want proxy URL", got)
	}
}

func TestTestProxyInvalidURLPersistsErrorStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminProxyTestDB(t)
	ctx := context.Background()
	id, err := db.InsertProxy(ctx, "http://proxy.example:8080", "")
	if err != nil {
		t.Fatalf("InsertProxy returned error: %v", err)
	}
	if err := db.UpdateProxyTestResult(ctx, id, database.ProxyTestStatusSuccess, "1.2.3.4", "US", 100); err != nil {
		t.Fatalf("seed successful test result: %v", err)
	}
	store := newAdminProxyTestStore(t, db)
	handler := &Handler{db: db, store: store}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/admin/proxies/test",
		strings.NewReader(fmt.Sprintf(`{"id":%d,"url":"://bad"}`, id)),
	)
	ginCtx.Request.Header.Set("Content-Type", "application/json")

	handler.TestProxy(ginCtx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Success || payload.Error == "" {
		t.Fatalf("response = %#v, want failed proxy test", payload)
	}

	rows, err := db.ListProxies(ctx)
	if err != nil {
		t.Fatalf("ListProxies returned error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListProxies returned %d rows, want 1", len(rows))
	}
	row := rows[0]
	if row.TestStatus != database.ProxyTestStatusError || row.TestIP != "" || row.TestLocation != "" || row.TestLatencyMs != 0 {
		t.Fatalf("persisted proxy result = %#v, want cleared error state", row)
	}
	if got := store.NextProxy(); got != "" {
		t.Fatalf("NextProxy after failed test = %q, want empty", got)
	}
}

func TestCleanErrorProxiesHandlerSynchronizesRuntimeAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newAdminProxyTestDB(t)
	ctx := context.Background()
	errorURL := "http://error.example:8080"
	errorID, err := db.InsertProxy(ctx, errorURL, "")
	if err != nil {
		t.Fatalf("InsertProxy(error) returned error: %v", err)
	}
	if err := db.UpdateProxyTestResult(ctx, errorID, database.ProxyTestStatusError, "", "", 0); err != nil {
		t.Fatalf("mark proxy error: %v", err)
	}
	accountID, err := db.InsertAccount(ctx, "bound", "rt-bound", errorURL)
	if err != nil {
		t.Fatalf("InsertAccount returned error: %v", err)
	}

	store := newAdminProxyTestStore(t, db)
	store.AddAccount(&auth.Account{
		DBID:     accountID,
		ProxyURL: errorURL,
		Status:   auth.StatusReady,
	})
	handler := &Handler{db: db, store: store}

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/proxies/clean-error", nil)
	handler.CleanErrorProxies(ginCtx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var payload struct {
		Cleaned int `json:"cleaned"`
		Unbound int `json:"unbound"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Cleaned != 1 || payload.Unbound != 1 {
		t.Fatalf("response = %#v, want cleaned=1 unbound=1", payload)
	}

	runtimeAccount := store.FindByID(accountID)
	if runtimeAccount == nil {
		t.Fatal("runtime account not found")
	}
	if got := runtimeAccount.GetProxyURL(); got != "" {
		t.Fatalf("runtime account proxy = %q, want empty", got)
	}
}
