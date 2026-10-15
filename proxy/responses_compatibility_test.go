package proxy

import (
	"testing"

	"github.com/codex2api/api"
)

func TestValidateCodexRelayResponsesCompatibility(t *testing.T) {
	err := validateCodexRelayResponsesCompatibility([]byte(`{"model":"gpt-6-astra","input":"hi","metadata":{"tenant":"demo"},"context_management":{"compact_threshold":1000}}`))
	if err == nil || err.Code != api.ErrCodeUnsupportedFeature {
		t.Fatalf("error = %#v, want unsupported_feature", err)
	}
	details, ok := err.Details.(map[string]any)
	if !ok {
		t.Fatalf("details type = %T, want map[string]any", err.Details)
	}
	fields, ok := details["fields"].([]map[string]string)
	if !ok || len(fields) != 2 {
		t.Fatalf("details = %#v, want two unsupported fields", err.Details)
	}
}

func TestValidateCodexRelayResponsesCompatibilityAllowsPreservedFields(t *testing.T) {
	if err := validateCodexRelayResponsesCompatibility([]byte(`{"model":"gpt-6-astra","input":"hi","tools":[],"reasoning":{"effort":"high"}}`)); err != nil {
		t.Fatalf("unexpected compatibility error: %#v", err)
	}
}
