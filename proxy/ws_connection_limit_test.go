package proxy

import "testing"

func TestWSConnectionLimiterPerKeyAndGlobal(t *testing.T) {
	l := newWSConnectionLimiter(3, 2)
	r1, ok := l.acquire(1)
	if !ok {
		t.Fatal("first per-key connection rejected")
	}
	if _, ok := l.acquire(1); !ok {
		t.Fatal("second per-key connection rejected")
	}
	if _, ok := l.acquire(1); ok {
		t.Fatal("third per-key connection should be rejected")
	}
	if _, ok := l.acquire(2); !ok {
		t.Fatal("other key should still be admitted")
	}
	if _, ok := l.acquire(3); ok {
		t.Fatal("global limit should reject")
	}
	r1()
	r1() // idempotent
	if _, ok := l.acquire(1); !ok {
		t.Fatal("released slot should be reusable")
	}
}
