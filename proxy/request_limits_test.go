package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

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
