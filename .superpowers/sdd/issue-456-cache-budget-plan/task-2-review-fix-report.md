# Task 2 Review-Fix Report

## Status

Complete on branch `codex/issue-456-cache-budget`.

This review-fix is a separate follow-up to Task 2 commit `b1ddda3`; that
commit was not amended.

## Findings fixed

1. The Redis bounded-reader allowance used the logical raw-item byte budget as
   if it were the stored wire byte budget. `encoding/json` expands `<`, `>`,
   and `&` to six-byte Unicode escapes and also escapes U+2028/U+2029, so an
   entry at the exact logical boundary could be rejected before decode.
2. The first normalization implementation restored every textual `\u2028` and
   `\u2029` sequence after encoding. That also matched the second backslash in
   literal business text such as `\\u2028`, changing its meaning and producing
   invalid JSON.
3. Responses and compact continuation failures could win over an already
   established scope-budget rejection, returning 409/503 instead of the
   request's real 429 routing reason.
4. Moving the compact scope check before waiting fixed finding 3 but changed
   ordinary compact scheduling: one scope-blocked account could force an
   immediate 429 even while another eligible account was only temporarily
   busy.

## Implementation

- Added a shared response-context JSON normalization path. It preserves JSON
  structure, object order, whitespace, and number spelling while normalizing
  string spellings to the logical bytes used for admission and reconstruction
  checks.
- Made Redis writes and reads use that logical representation.
- Added a saturating wire-size upper bound: six times the logical byte budget,
  plus the unchanged `{"items":[...]}` wrapper and maximum comma overhead.
  Redis still performs the bound before deserialization.
- Restored JSONP separator escapes only when the candidate backslash is not
  itself escaped, determined from the parity of immediately preceding
  backslashes. Literal `\\u2028`/`\\u2029` text therefore remains literal.
- Applied normalization before local admission and before backend
  reconstruction-size enforcement.
- In Responses, kept scope-budget 429 ahead of the final continuation 409/503
  after account routing.
- In compact, limited that priority change to the immediate
  `continuationUnavailable && !relayContinuationAttempted` branch. Ordinary
  compact requests still call `WaitForSessionAvailableWithFilter` and only
  evaluate the final scope reason after the wait fails.

## RED evidence

### Real Redis writer and logical/wire boundary

Before the implementation, the new real-writer round-trip test did not compile
because the shared normalization and wire-limit functions did not exist:

```text
undefined: NormalizeResponseContextItems
undefined: ResponseContextWireLimit
```

### Scope priority

Command:

```text
go test ./proxy -run TestResponsesContinuationScopeBudget429PrecedesCacheUnavailable -count=1
```

Both Responses and compact returned 409 instead of the expected scope-budget
429.

### Literal Unicode-escape text

Command:

```text
go test ./cache -run TestNormalizeResponseContextItemsPreservesLiteralUnicodeEscapeTextAndIsIdempotent -count=1
```

The first implementation failed because normalization changed literal
`\\u2028`/`\\u2029` text into a backslash followed by an actual separator,
leaving invalid JSON.

### Ordinary compact wait semantics

Command:

```text
go test ./proxy -run TestResponsesCompactNormalRequestWaitsForTemporarilyBusyAccountBeforeScope429 -count=1
```

The intermediate implementation returned 429. The regression fixture has one
scope-blocked relay and one eligible relay held at concurrency capacity for 75
ms; the expected behavior is to wait for the latter and return 200.

## Final GREEN evidence

### Cache and API

```text
go test ./cache ./api -count=1
ok  github.com/codex2api/cache  0.623s
ok  github.com/codex2api/api    0.289s
```

### Focused proxy

```text
go test ./proxy -run 'ResponseCache|PreviousResponse|ResponsesCompact' -count=1
ok  github.com/codex2api/proxy  0.593s
```

### Race

```text
go test -race ./proxy -run 'ResponseCache|PreviousResponse' -count=1
ok  github.com/codex2api/proxy  1.885s
```

### Full scoped regression

```text
go test ./proxy ./cache ./api -count=1
ok  github.com/codex2api/proxy  7.156s
ok  github.com/codex2api/cache  1.565s
ok  github.com/codex2api/api    0.971s
```

### Static checks

```text
go vet ./proxy ./cache ./api
git diff --check
```

Both completed with exit status 0 and no output.

## Regression coverage added

- A real production `SetResponseContext` to bounded
  `GetResponseContextBounded` round trip at the exact logical boundary with at
  least 50 `<>&` repetitions and U+2028/U+2029.
- JSON structure, whitespace, object order, number spelling, HTML escapes,
  ordinary Unicode escapes, and JSONP separator normalization.
- Literal `\\u2028`/`\\u2029`, escaped backslashes, JSON validity, semantic
  preservation, and normalization idempotence.
- Wire-limit worst-case expansion and saturation.
- Scope-budget 429 priority over continuation 409/503 for Responses and
  compact.
- Ordinary compact wait semantics with a scope-blocked account and a separate
  temporarily busy eligible account.

## Files changed

- `cache/redis.go`
- `cache/response_context.go`
- `cache/response_context_bounded_test.go`
- `proxy/handler.go`
- `proxy/response_cache.go`
- `proxy/response_cache_handler_test.go`
- `.superpowers/sdd/issue-456-cache-budget-plan/task-2-review-fix-report.md`

## Self-review

- Verified the Redis key, TTL behavior, and `{"items":[...]}` record wrapper
  remain unchanged.
- Verified the official Redis path still rejects over-limit data before JSON
  deserialization.
- Verified normalization is byte-idempotent and preserves decoded string
  semantics for escaped backslashes and literal Unicode-escape text.
- Verified logical reconstruction size is checked before pair-safe item
  trimming.
- Verified Responses and compact report scope 429 before continuation 409/503
  only after eligible routing proves no account can be selected.
- Verified ordinary compact requests retain their wait-before-final-scope
  behavior.
- Verified no settings persistence, admin/frontend work, Docker, push, PR,
  merge, or `main` changes were made.

## Memory lookup

No memory-derived fact, memory entry, rollout summary, or rollout ID was used
for this review-fix. All findings, implementation decisions, and verification
came from the current branch, review feedback, code, and tests.

## Remaining boundary

The six-times logical allowance is intentionally a conservative proven upper
bound rather than an exact allocation. It prevents pre-deserialization false
rejection while preserving a finite Redis read ceiling. Third-party shared
backends that do not implement `BoundedResponseContextReader` remain subject
to their existing post-materialization compatibility path.
