package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// newExternalImageJobRouter wires the external job route the same way the
// production server does, so admission checks run in their real order.
func newExternalImageJobRouter(t *testing.T, limits database.APIKeyLimits, apiKeyValue string) (*gin.Engine, *proxy.Handler, *database.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := newTestAdminDB(t)
	tc := cache.NewMemory(1)
	t.Cleanup(func() { _ = tc.Close() })
	store := auth.NewStore(db, tc, nil)
	t.Cleanup(store.Stop)
	handler := NewHandler(store, db, tc, nil, "admin-secret")
	imageProxy := proxy.NewHandler(store, db, nil, nil)
	imageProxy.SetRuntimeCache(tc)
	router := gin.New()
	handler.RegisterExternalImageRoutes(router, imageProxy)

	if _, err := db.InsertAPIKeyWithOptions(context.Background(), database.APIKeyInput{
		Name:   "image-admission",
		Key:    apiKeyValue,
		Limits: limits,
	}); err != nil {
		t.Fatalf("InsertAPIKeyWithOptions: %v", err)
	}
	return router, imageProxy, db
}

// occupyAPIKeyConcurrency takes the key's concurrency slot the way an already
// running job would.
func occupyAPIKeyConcurrency(t *testing.T, imageProxy *proxy.Handler, apiKeyValue string) func() {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/images/jobs", nil)
	ctx.Request.Header.Set("Authorization", "Bearer "+apiKeyValue)
	imageProxy.APIKeyAuthMiddleware()(ctx)
	if ctx.IsAborted() {
		t.Fatalf("API key preflight failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	release, ok := imageProxy.AcquireAPIKeyConcurrency(ctx)
	if !ok || release == nil {
		t.Fatal("failed to occupy the API-key concurrency slot")
	}
	return release
}

// A saturated key must be refused at enqueue. Accepting the request and letting
// the background job fail instead would turn MaxConcurrency into a generator of
// failed job rows rather than a limit.
func TestExternalImageJobRefusesEnqueueWhenConcurrencySlotIsTaken(t *testing.T) {
	const apiKeyValue = "sk-image-admission-full"
	router, imageProxy, db := newExternalImageJobRouter(t,
		database.APIKeyLimits{MaxConcurrency: 1}, apiKeyValue)

	release := occupyAPIKeyConcurrency(t, imageProxy, apiKeyValue)
	defer release()

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/jobs",
		strings.NewReader(`{"prompt":"draw a cat","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer "+apiKeyValue)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 at enqueue; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "concurrency") {
		t.Fatalf("body = %s, want a concurrency limit message", recorder.Body.String())
	}
	page, err := db.ListImageGenerationJobs(context.Background(), 1, 20, 0)
	if err != nil {
		t.Fatalf("ListImageGenerationJobs: %v", err)
	}
	if len(page.Jobs) != 0 {
		t.Fatalf("refused request still persisted jobs: %+v", page.Jobs)
	}
}

// The enqueue slot covers the whole job, so the in-process upstream calls must
// inherit it. Taking a second slot for the same key would make a job with
// MaxConcurrency=1 deadlock against itself and fail every time.
func TestExternalImageJobDoesNotDoubleCountItsOwnConcurrencySlot(t *testing.T) {
	const apiKeyValue = "sk-image-admission-self"
	router, _, db := newExternalImageJobRouter(t,
		database.APIKeyLimits{MaxConcurrency: 1}, apiKeyValue)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/images/jobs",
		strings.NewReader(`{"prompt":"draw a cat","model":"gpt-image-2"}`))
	req.Header.Set("Authorization", "Bearer "+apiKeyValue)
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", recorder.Code, recorder.Body.String())
	}

	// The job runs without upstream accounts and therefore fails, but it must
	// not fail because it could not obtain its own concurrency slot.
	deadline := time.Now().Add(10 * time.Second)
	for {
		page, err := db.ListImageGenerationJobs(context.Background(), 1, 20, 0)
		if err != nil {
			t.Fatalf("ListImageGenerationJobs: %v", err)
		}
		if len(page.Jobs) != 1 {
			t.Fatalf("jobs = %d, want the accepted job persisted", len(page.Jobs))
		}
		job := page.Jobs[0]
		if job.Status != database.ImageJobQueued && job.Status != database.ImageJobRunning {
			if strings.Contains(strings.ToLower(job.ErrorMessage), "concurrency") {
				t.Fatalf("job failed on its own concurrency slot: %s", job.ErrorMessage)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach a terminal state: status=%s", job.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestImageJobTimeoutScalesWithRequestedOutputs(t *testing.T) {
	if got := imageJobTimeout(1); got != imageJobBaseTimeout {
		t.Fatalf("imageJobTimeout(1) = %s, want %s", got, imageJobBaseTimeout)
	}
	if got := imageJobTimeout(4); got != 4*imageJobBaseTimeout {
		t.Fatalf("imageJobTimeout(4) = %s, want %s", got, 4*imageJobBaseTimeout)
	}
	// Unset and out-of-range counts must still produce a usable deadline.
	if got := imageJobTimeout(0); got != imageJobBaseTimeout {
		t.Fatalf("imageJobTimeout(0) = %s, want the single-output budget", got)
	}
	if got := imageJobTimeout(99); got != maxImageJobOutputCount*imageJobBaseTimeout {
		t.Fatalf("imageJobTimeout(99) = %s, want the clamped budget", got)
	}
}

func TestNormalizeImageUpscalerContentTypeRejectsNonImageTypes(t *testing.T) {
	tests := map[string]string{
		"":                         "image/png",
		"application/octet-stream": "image/png",
		"image/webp":               "image/webp",
		"IMAGE/PNG; charset=utf-8": "image/png",
		"text/html":                "",
		"application/javascript":   "",
		"text/html; charset=utf-8": "",
	}
	for input, want := range tests {
		if got := normalizeImageUpscalerContentType(input); got != want {
			t.Fatalf("normalizeImageUpscalerContentType(%q) = %q, want %q", input, got, want)
		}
	}
}
