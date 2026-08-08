package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCompareAndSwapPromptFilterReviewAPIKeysRejectsStaleSnapshot(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "review-keys.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.UpdateSystemSettings(ctx, &SystemSettings{PromptFilterReviewAPIKey: "key-one\nkey-two"}); err != nil {
		t.Fatalf("UpdateSystemSettings: %v", err)
	}
	swapped, err := db.CompareAndSwapPromptFilterReviewAPIKeys(ctx, "key-one\nkey-two", "key-two")
	if err != nil || !swapped {
		t.Fatalf("first swap = %t, %v; want true, nil", swapped, err)
	}
	swapped, err = db.CompareAndSwapPromptFilterReviewAPIKeys(ctx, "key-one\nkey-two", "key-one")
	if err != nil || swapped {
		t.Fatalf("stale swap = %t, %v; want false, nil", swapped, err)
	}
	settings, err := db.GetSystemSettings(ctx)
	if err != nil || settings.PromptFilterReviewAPIKey != "key-two" {
		t.Fatalf("stored keys = %q, %v; want key-two", settings.PromptFilterReviewAPIKey, err)
	}
}
