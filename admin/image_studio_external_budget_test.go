package admin

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
)

func TestNormalizeExternalImageJobInputsTotalBudget(t *testing.T) {
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("a"), 40))
	req := &imageGenerationJobPayload{InputImages: []string{img, img}}
	if err := normalizeExternalImageJobInputsWithLimit(context.Background(), req, 150); err == nil {
		t.Fatal("expected total input budget error")
	}
	req = &imageGenerationJobPayload{InputImages: []string{img, img}}
	if err := normalizeExternalImageJobInputsWithLimit(context.Background(), req, 300); err != nil {
		t.Fatalf("unexpected error under budget: %v", err)
	}
}
