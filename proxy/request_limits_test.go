package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRejectDuplicateJSONField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"messages":[],"messages":[{"a":1}]}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	if !rejectDuplicateJSONField(c, body, "messages") {
		t.Fatal("expected duplicate top-level field rejection")
	}
	if recorder.Code != 400 {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if rejectDuplicateJSONField(c, []byte(`{"messages":[]}`), "messages") {
		t.Fatal("unexpected rejection for a single field")
	}
}

func TestRejectExcessiveJSONArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"input":[{"a":1},{"a":2},{"a":3}]}`)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	if !rejectExcessiveJSONArray(c, body, "input", 2) {
		t.Fatal("expected rejection when item count exceeds limit")
	}
	if recorder.Code != 400 {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(recorder)
	if rejectExcessiveJSONArray(c, body, "input", 3) {
		t.Fatal("unexpected rejection at the limit")
	}
}
