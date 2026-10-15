package proxy

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/codex2api/api"
	"github.com/gin-gonic/gin"
)

// strictResponsesCompatibilityHeader lets clients opt in to fail-closed
// validation when the Codex upstream cannot preserve an official Responses
// field. The default remains backward compatible because the Codex channel
// still needs to normalize fields that the upstream rejects.
const strictResponsesCompatibilityHeader = "X-Codex2API-Strict-Compatibility"

// These top-level fields are currently normalized away by the Codex relay.
// Keep this list explicit: adding a field here is a compatibility decision,
// not a generic JSON sanitizer change.
var codexRelayResponsesCompatibilityFields = []string{
	"max_output_tokens",
	"max_tokens",
	"max_completion_tokens",
	"background",
	"conversation",
	"context_management",
	"temperature",
	"top_p",
	"frequency_penalty",
	"presence_penalty",
	"logprobs",
	"top_logprobs",
	"n",
	"seed",
	"stop",
	"user",
	"logit_bias",
	"metadata",
	"stream_options",
	"truncation",
	"disable_response_storage",
	"prompt_cache_retention",
	"safety_identifier",
	"verbosity",
}

func strictResponsesCompatibilityRequested(c *gin.Context) bool {
	if c != nil {
		switch strings.ToLower(strings.TrimSpace(c.GetHeader(strictResponsesCompatibilityHeader))) {
		case "1", "true", "yes", "on":
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CODEX_STRICT_RESPONSES_COMPAT"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// validateCodexRelayResponsesCompatibility returns an actionable error before
// translation can silently change the request. It is deliberately opt-in so
// existing clients keep the historical Codex normalization behavior until
// they have migrated to a capability-aware route.
func validateCodexRelayResponsesCompatibility(rawBody []byte) *api.APIError {
	var body map[string]json.RawMessage
	if len(rawBody) == 0 || json.Unmarshal(rawBody, &body) != nil {
		return nil
	}
	issues := make([]map[string]string, 0, len(codexRelayResponsesCompatibilityFields))
	for _, field := range codexRelayResponsesCompatibilityFields {
		if _, present := body[field]; !present {
			continue
		}
		issues = append(issues, map[string]string{
			"field":  field,
			"reason": "Codex relay currently cannot preserve this official Responses field",
		})
	}
	if len(issues) == 0 {
		return nil
	}
	return api.NewAPIErrorWithDetails(
		api.ErrCodeUnsupportedFeature,
		"请求包含当前 Codex relay 无法原样保留的 Responses 字段",
		api.ErrorTypeInvalidRequest,
		map[string]any{
			"transport": "codex-relay",
			"fields":    issues,
			"hint":      "移除这些字段，或使用支持原生 Responses 的上游配置",
		},
	)
}
