# Task 2 Report: backend integration and continuation failure semantics

## Status

Complete on branch `codex/issue-456-cache-budget`.

Task 2 is implemented on top of Task 1 commits `abfd1fb` and `34e7f7e`.

## Scope delivered

- Kept `cache.TokenCache` unchanged and added the optional
  `cache.BoundedResponseContextReader` capability.
- Made `SetResponseContextCache` retain a response-context backend only when
  `SharedAcrossInstances()` is true. Memory mode therefore has one
  authoritative bounded local copy and never calls
  `MemoryTokenCache.SetResponseContext` or `GetResponseContext`.
- Preserved the Redis response-context key, TTL, and exact
  `{"items":[...]}` value format.
- Added an official Redis bounded reader using one `GETRANGE 0 <limit>`
  request, where the inclusive end requests one byte past the accepted wire
  size. Oversized values are rejected before full fetch or JSON decode.
- Added a default 64 MiB reconstruction limit to the internal runtime cache
  configuration. Task 3 remains responsible for persistence and admin wiring.
- Unified normal writes and backend hydration through the canonical pair-safe
  trim, logical-size calculation, and local admission path.
- Made Redis receive both small and locally oversized canonical contexts.
- Made locally oversized Redis values serve the current lookup without local
  promotion; repeated lookups read Redis again.
- Added typed internal lookup results for local/backend hits, promotion,
  ordinary miss, expiry, known eviction, known oversize, reconstruction-limit
  rejection, corrupt backend values, and backend transport errors.
- Added payload-free Memory-mode unavailable markers. They are owner-isolated,
  bounded to 2,000 entries, capped by the cache TTL, LRU-cleaned on overflow,
  removed by successful same-key admission, and removed by the cleanup pass.
- Added detailed internal Responses and compact preparation while preserving
  all existing exported preparation signatures.
- Preserved native downstream Responses WebSocket preparation: it keeps
  `previous_response_id` and performs no local response-cache lookup.
- Added `response_context_unavailable` and mapped it to HTTP 409.
- For Codex-only HTTP requests, returned:
  - 409 for known eviction/oversize, corrupt or too-large backend data, and
    dependent ordinary/expired misses.
  - 503 for a backend transport failure only when the current input has a
    dependent tool output.
- Deferred the final 409/503 decision until account filtering/routing.
  Relay-style attempts retain the raw/OpenAI body and
  `previous_response_id`; mixed pools restrict unavailable continuations to
  relay accounts. The same behavior applies to `/v1/responses/compact`.
- Kept completion-cache write failures log-only, so an already-started or
  completed downstream response is not converted into an error.

## TDD evidence

### Initial RED

Commands:

```text
go test ./cache -run 'TestRedis(BoundedResponseContextReader|ResponseContextWireCompatibility)' -count=1
go test ./api -run 'TestHTTPStatusCodeForCommonAPIErrors' -count=1
go test ./proxy -run 'Test(ResponseCacheMemory|ResponseCacheShared|ResponseCacheBackend|ResponseCacheReconstruction|ResponseCacheMemoryKnown|PrepareResponsesBodyDetailed|PrepareCompactResponsesBodyDetailed|PrepareResponsesWebSocketBodyDoesNotLookup|ResponsesContinuation|ResponsesCompactContinuation|ResponsesDependent)' -count=1
```

All three exited with status 1 for the expected missing Task 2 behavior:

```text
cache: GetResponseContextBounded undefined; ResponseContextRead* undefined
api: ErrCodeResponseContextUnavailable undefined
proxy: cache.ResponseContextReadResult, getResponseCacheResult,
       responseCacheLookup*, detailed preparation, markers, and
       reconstruction configuration undefined
```

### Additional RED from self-review

Command:

```text
go test ./proxy -run '^TestResponseCacheOversizeReplacementRemovesStaleLocalEntry$' -count=1
```

Result: exit status 1. An oversized same-key replacement left the old local
entry visible:

```text
replacement lookup ... Kind:hit Source:local,
want newest oversize marker instead of stale local hit
```

The admission path now removes the replaced entry before deciding whether the
new candidate can be admitted, while still avoiding the retained deep copy for
rejected candidates.

## Final GREEN evidence

### Cache and API

Command:

```text
go test ./cache ./api -count=1
```

Output:

```text
ok  github.com/codex2api/cache  0.626s
ok  github.com/codex2api/api    0.284s
```

### Focused proxy

Command:

```text
go test ./proxy -run 'ResponseCache|PreviousResponse|ResponsesCompact' -count=1
```

Output:

```text
ok  github.com/codex2api/proxy  0.557s
```

### Race

Command:

```text
go test -race ./proxy -run 'ResponseCache|PreviousResponse' -count=1
```

Output:

```text
ok  github.com/codex2api/proxy  1.873s
```

This selection covers concurrent backend promotion, local admission, LRU
eviction, Memory markers, returned-value isolation, and stats snapshots.

### Full scoped regression

Command:

```text
go test ./proxy ./cache ./api -count=1
```

Output:

```text
ok  github.com/codex2api/proxy  7.665s
ok  github.com/codex2api/cache  1.149s
ok  github.com/codex2api/api    0.978s
```

### Vet and diff

Commands:

```text
go vet ./proxy ./cache ./api
git diff --check
```

Both completed with exit status 0 and no output.

## Test coverage added

- Memory response backend receives zero response-context reads/writes.
- Shared backend receives small and locally oversized canonical writes.
- Small backend hits promote locally; second lookup is local.
- L1-oversized backend hits serve without promotion; second lookup is remote.
- Reconstruction logical-byte exact boundary and one byte over.
- Third-party non-bounded backend checks the complete logical payload before
  pair-safe trimming; JSON decode errors classify as corrupt.
- Backend miss, corrupt value, too-large value, and transport error remain
  distinct.
- Redis `GETRANGE` uses the expected inclusive bound and does not decode an
  over-limit value.
- Redis empty/null item records preserve the legacy miss behavior.
- Redis key and `{"items":[...]}` wire compatibility.
- Memory oversize, count eviction, byte eviction, marker cap, marker TTL
  cleanup, same-key recovery, and owner isolation.
- Concurrent backend promotion, eviction, marker creation, and stats reads.
- Detailed preparation injects local history and removes
  `previous_response_id` on Codex bodies.
- Complete client-supplied call context bypasses lookup.
- Ordinary no-output miss and no-output backend error preserve legacy routing.
- Codex-only dependent miss/corrupt/too-large/known unavailable returns 409.
- Codex-only dependent backend error returns 503.
- Relay-only and mixed pools preserve `previous_response_id`.
- Backend-error relay fallback and compaction-trigger relay fallback.
- Compact parity.
- Native Responses WebSocket preparation performs no lookup and preserves
  `previous_response_id`.
- API status mapping for `response_context_unavailable`.

## Files changed

- `api/errors.go`
- `api/validation_test.go`
- `cache/cache.go`
- `cache/redis.go`
- `cache/response_context_bounded_test.go`
- `proxy/handler.go`
- `proxy/response_cache.go`
- `proxy/response_cache_backend_test.go`
- `proxy/response_cache_budget_test.go`
- `proxy/response_cache_handler_test.go`
- `proxy/response_cache_preparation_test.go`
- `proxy/response_cache_test.go`
- `proxy/translator.go`
- `.superpowers/sdd/issue-456-cache-budget-plan/task-2-report.md`

## Self-review

- Verified no method was added to `cache.TokenCache`.
- Verified the Memory response map remains for interface compatibility but is
  unreachable through proxy response-context integration.
- Verified local admission rejects before allocating the retained per-item deep
  copy.
- Verified normal writes and backend hydration use the same canonical
  pair-safe trim and admission path.
- Verified backend logical reconstruction size is checked before the 200-item
  trim, preventing a large prefix from being hidden by trimming.
- Verified Redis-local eviction never becomes final unavailability while the
  shared backend can still answer.
- Verified marker values contain only key, reason, expiry, and list metadata;
  no context payload is retained.
- Verified error details include only the field and reason, never the response
  ID.
- Verified 409/503 is emitted only after the eligible relay filter has been
  queried and never by native Responses WebSocket preparation.
- Verified the relay path continues to derive its body from the raw/OpenAI
  request and preserves `previous_response_id`.
- Verified no persisted settings, DB/admin/frontend/Ops, compression, P1/P2,
  Docker, push, PR, merge, or `main` changes were made.

## Memory lookup

A quick pass ran `rg` against
`/Users/kyx/.codex/memories/MEMORY.md` for issue/cache keywords. It returned no
matches. No memory entry, memory-derived fact, rollout summary, or rollout ID
was used; implementation and verification came from the current plan, brief,
reports, code, and tests.

## Concerns / follow-up boundaries

- The official Redis backend guarantees the pre-deserialization bound.
  Third-party shared `TokenCache` implementations without the optional bounded
  reader remain compatible but can only be checked after their existing
  `GetResponseContext` has materialized the value.
- Settings persistence and fleet convergence for the reconstruction/local
  budgets remain Task 3 scope.
- Cache observability counters and UI remain Task 4 scope.
