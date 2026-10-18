package proxy

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/codex2api/api"
	"github.com/codex2api/proxy/internal/jsonbody"
)

// rejectDuplicateJSONField rejects duplicate top-level array fields before the
// item-count guard. gjson selects the first duplicate, while encoding/json
// decodes the last one, so a small first array could hide a massive second one.
func rejectDuplicateJSONField(c *gin.Context, rawBody []byte, field string) bool {
	if c == nil || len(rawBody) == 0 || !gjson.ValidBytes(rawBody) {
		return false
	}
	root := jsonbody.View(rawBody)
	if !root.IsObject() {
		return false
	}
	seen := false
	duplicate := false
	root.ForEach(func(key, _ gjson.Result) bool {
		if key.String() != field {
			return true
		}
		if seen {
			duplicate = true
			return false
		}
		seen = true
		return true
	})
	if !duplicate {
		return false
	}
	api.SendError(c, api.NewAPIError(
		api.ErrCodeInvalidRequest,
		fmt.Sprintf("duplicate top-level field %q is not allowed", field),
		api.ErrorTypeInvalidRequest,
	))
	return true
}
