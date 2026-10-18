package proxy

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCodexAccessRestrictedPeekPreservesBody(t *testing.T) {
	body := []byte(`{"error":{"code":"codex_access_restricted","message":"restricted"}}`)
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	if !isCodexAccessRestrictedResponse(resp) {
		t.Fatal("expected codex_access_restricted to be detected")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("restored body = %q, want %q", got, body)
	}
}

func TestCodexAccessRestrictedPeekDoesNotBufferWholeBody(t *testing.T) {
	prefix := `{"error":{"code":"other","padding":"`
	suffix := `"}}`
	padding := strings.Repeat("x", 2<<20)
	body := []byte(prefix + padding + suffix)
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	if isCodexAccessRestrictedResponse(resp) {
		t.Fatal("non-restricted error must not be detected")
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("restored body length = %d, want %d", len(got), len(body))
	}
}
