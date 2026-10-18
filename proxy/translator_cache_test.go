package proxy

import (
	"container/list"
	"testing"
	"time"
)

func newTestRequestCache(maxBytes, maxEntryBytes int, ttl time.Duration) *requestCache {
	return &requestCache{
		order:         list.New(),
		items:         make(map[[32]byte]*list.Element),
		maxBytes:      maxBytes,
		maxEntryBytes: maxEntryBytes,
		ttl:           ttl,
	}
}

func TestRequestCacheEnforcesByteBudget(t *testing.T) {
	c := newTestRequestCache(10, 10, time.Minute)
	c.put([32]byte{1}, openAIRequest{}, 6)
	c.put([32]byte{2}, openAIRequest{}, 6)
	if len(c.items) != 1 {
		t.Fatalf("entries = %d, want 1 after byte-budget eviction", len(c.items))
	}
	if c.bytes > 10 {
		t.Fatalf("bytes = %d, want <= 10", c.bytes)
	}
	if _, ok := c.get([32]byte{1}); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, ok := c.get([32]byte{2}); !ok {
		t.Fatal("newest entry should remain")
	}
}

func TestRequestCacheRejectsOversizedEntry(t *testing.T) {
	c := newTestRequestCache(100, 8, time.Minute)
	c.put([32]byte{1}, openAIRequest{}, 9)
	if len(c.items) != 0 {
		t.Fatal("entry larger than maxEntryBytes must not be cached")
	}
}

func TestRequestCacheExpiresEntries(t *testing.T) {
	c := newTestRequestCache(100, 100, time.Nanosecond)
	c.put([32]byte{1}, openAIRequest{}, 1)
	time.Sleep(time.Millisecond)
	if _, ok := c.get([32]byte{1}); ok {
		t.Fatal("expired entry must not be returned")
	}
	if len(c.items) != 0 {
		t.Fatal("expired entry should be removed on access")
	}
}
