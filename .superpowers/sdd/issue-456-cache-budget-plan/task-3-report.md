# Task 3 Report: persisted and fleet-convergent response-cache settings

## Status

Complete on branch `codex/issue-456-cache-budget`.

Task 3 is implemented on top of the independently approved Task 2 commits
`b1ddda3` and `0ea5910`.

## Scope delivered

- Added exactly three persisted response-cache byte budgets and one read-only
  generation:
  - `response_cache_local_max_bytes`
  - `response_cache_local_max_entry_bytes`
  - `response_cache_reconstruct_max_bytes`
  - `response_cache_config_generation`
- Added the fields to fresh and compatibility migrations:
  - PostgreSQL uses `BIGINT NOT NULL` with the specified defaults.
  - SQLite uses `INTEGER NOT NULL` with the specified defaults.
- Kept the fields out of `SystemSettings` and the large
  `UpdateSystemSettings` upsert so older/stale full-setting writes cannot
  overwrite cache configuration.
- Added narrow database defaults, validation, read, and transactional partial
  update methods.
- Made the narrow update ensure the singleton row, serialize writers, merge
  pointers against current committed values, validate the merged result,
  increment generation only for a real change, update all four fields
  atomically, and return only after commit.
- Used PostgreSQL `SELECT ... FOR UPDATE` and the existing SQLite write
  semaphore plus immediate-transaction behavior. Concurrent distinct SQLite
  connections are covered with a real file-backed test.
- Added explicit generation-overflow handling without partial persistence.
- Added generation-aware runtime application under the response-cache mutex.
  It changes only the three budgets, preserves the fixed 2,000-entry limit,
  10-minute absolute TTL, and 200-item limit, and reuses
  `enforceConfigLocked()` for immediate shrinking.
- Added thread-safe applied-configuration and synchronization-status
  snapshots without resetting cache statistics.
- Added a five-second production poller with at-most-three-second reads,
  last-good retention, failure/recovery tracking, newer-generation-only
  application, and cancellation tied to both the application context and
  `DB.RunBackgroundTask` lifecycle.
- Added startup loading before request serving and poller registration on the
  application background context.
- Extended admin settings GET/PUT with the three byte fields and generation.
  Explicit generation writes, including JSON number, string, and `null`, are
  rejected. Explicit byte ranges are validated before storage access; the
  merged total/entry constraint is validated again against persisted state.
- Admin cache updates persist through the narrow transaction, apply the
  committed snapshot locally only after persistence succeeds, and return the
  complete committed values and generation.

## TDD evidence

### Initial database RED

Command:

```text
go test ./database -run 'ResponseCacheSettings|SQLiteResponseCacheSettingsMigration' -count=1
```

Result: exit status 1 for the expected missing database types, constants,
validation, narrow read, and narrow update methods.

### Initial runtime/poller RED

Command:

```text
go test ./proxy -run 'ResponseCacheConfig|ResponseCache.*Generation|ResponseCache.*Poll' -count=1
```

Result: exit status 1 for the expected missing applied-generation state,
runtime apply/snapshot functions, startup loader, poller, and sync status.

### Initial admin RED

Command:

```text
go test ./admin -run 'Settings.*ResponseCache|ResponseCache.*Settings' -count=1
```

Result: exit status 1 for the expected missing settings request/response
fields and narrow store integration.

### Regression RED from full scoped verification

The first full scoped run exposed an existing validation-order contract:
`TestUpdateSettingsRejectsAutoResetCreditsWindowOutOfRange` constructs a
lightweight `Handler{}` and expects pure request validation to return 400
before database access. The early cache read caused a nil dereference. Moving
the cache read after all existing pure validations restored the contract.

An additional focused test then captured the cache-specific parse order:

```text
go test ./admin -run 'TestResponseCacheSettingsInvalidExplicitValueIsRejectedBeforeStoreRead' -count=1
```

Expected RED:

```text
status = 500, want 400; body={"error":"响应缓存设置存储不可用"}
```

After adding pointer-only range validation before storage access, the test
passed while merged validation remained in the database transaction.

## Final GREEN evidence

### Focused database, proxy, and admin

Commands:

```text
go test ./database -run 'ResponseCacheSettings|SQLiteResponseCacheSettingsMigration' -count=1
go test ./proxy -run 'ResponseCacheConfig|ResponseCache.*Generation|ResponseCache.*Poll' -count=1
go test ./admin -run 'Settings.*ResponseCache|ResponseCache.*Settings' -count=1
```

Output:

```text
ok  github.com/codex2api/database  0.948s
ok  github.com/codex2api/proxy     2.447s
ok  github.com/codex2api/admin     1.290s
```

### Combined settings/cache selection

Command:

```text
go test ./database ./admin ./proxy -run 'ResponseCache|Settings' -count=1
```

Output:

```text
ok  github.com/codex2api/database  1.603s
ok  github.com/codex2api/admin     2.198s
ok  github.com/codex2api/proxy     1.090s
```

### Race

Command:

```text
go test -race ./database ./admin ./proxy -run 'ResponseCache|Settings' -count=1
```

Output:

```text
ok  github.com/codex2api/database  11.989s
ok  github.com/codex2api/admin     20.925s
ok  github.com/codex2api/proxy      4.756s
```

This selection covers concurrent disjoint database updates, poll/admin
generation races, local cache reads/writes/eviction, statistics, and
configuration/status snapshots.

### Full scoped regression

Command:

```text
go test ./proxy ./cache ./admin ./database -count=1
```

Output:

```text
ok  github.com/codex2api/proxy     8.200s
ok  github.com/codex2api/cache     0.647s
ok  github.com/codex2api/admin    14.269s
ok  github.com/codex2api/database  5.721s
```

### Full repository regression

Command:

```text
go test ./... -count=1
```

Result: all packages passed, including the root package, admin, API, auth,
cache, database, proxy, WebSocket relay, and security packages.

### Vet and diff

Commands:

```text
go vet ./database ./admin ./proxy
git diff --check
```

Both completed with exit status 0 and no output.

## Test coverage added

### Database

- Fresh SQLite defaults, exact column types/defaults, generation 1, and a
  no-row narrow read that does not insert.
- Legacy SQLite schema reopen and idempotent four-column migration.
- Three-field round trip, single-field partial update, nil update, same-value
  update, and exact generation behavior.
- Every exact lower/upper bound plus one below/above, entry greater than final
  total, non-positive generation, and rollback preservation.
- Two distinct SQLite database handles concurrently updating disjoint fields
  without lost updates, ending at generation 3.
- Large `UpdateSystemSettings` upsert preserving narrow values/generation.
- Generation `MaxInt64` overflow rollback.
- PostgreSQL transactional read includes `FOR UPDATE`; SQLite does not.

### Runtime and poller

- Generation 1 startup application, newer immediate shrink, equal/older
  no-op, and unchanged fixed count/TTL/item settings.
- Cache statistics survive reconfiguration.
- Poll convergence, failure retention/error visibility, later recovery/error
  clearing, and last successful sync time.
- Injected bounded read timeout.
- Application cancellation stops future reads and does not become a sync
  error.
- Startup load and real SQLite/database-lifecycle convergence.
- Registration refusal after database draining begins.
- An old poll result cannot roll back a newer admin application.
- Concurrent apply/cache operations/statistics/config/status snapshots under
  the race detector.

### Admin

- GET returns all three byte values and generation.
- Partial PUT returns and applies the complete committed snapshot.
- All exact boundaries accepted; one below/above rejected.
- Invalid explicit values rejected before any store read.
- Merged entry-greater-than-total rejected without persistence/runtime change.
- Explicit generation number, string, and `null` rejected.
- Unrelated partial PUT preserves values and generation.
- Persistence seam proves runtime is unchanged while the transaction runs,
  applies only the returned committed snapshot after success, and remains
  unchanged after failure.
- Existing pure request validation remains ahead of storage access.

## Files changed

- `admin/handler.go`
- `admin/response_cache_settings_test.go`
- `database/postgres.go`
- `database/sqlite.go`
- `database/response_cache_settings.go`
- `database/response_cache_settings_test.go`
- `main.go`
- `proxy/response_cache.go`
- `proxy/response_cache_settings.go`
- `proxy/response_cache_settings_test.go`
- `.superpowers/sdd/issue-456-cache-budget-plan/task-3-report.md`

## Self-review

- Verified `SystemSettings` and the large settings upsert do not contain the
  four new fields.
- Verified no-row reads return defaults without inserting; only the narrow
  update ensures the singleton row.
- Verified the entire SQLite ensure/read/merge/update/commit sequence is under
  the existing write lock and immediate transaction; PostgreSQL locks the row.
- Verified nil and same-value updates do not increment generation, real
  changes increment exactly once, and overflow cannot wrap.
- Verified validation occurs both before admin storage access for explicit
  ranges and inside the transaction for the merged final configuration.
- Verified admin persistence completes before local application and only the
  committed snapshot is applied.
- Verified runtime application holds the cache mutex across generation check,
  three-budget replacement, generation update, and immediate enforcement.
- Verified fixed count/TTL/item settings and all existing cache statistics are
  preserved.
- Verified poll reads are bounded, canceled parents are checked before calling
  the reader, cancellation is not recorded as failure, and database shutdown
  waits for poller exit.
- Verified startup applies generation 1 before serving and the poller is tied
  to the shared application background context.
- Verified explicit JSON `null` cannot bypass the read-only generation check.
- Verified no Ops response, frontend, translations, documentation,
  compression, cache namespace, P1/P2, Docker, push, PR, or merge work was
  added.

## Memory lookup

A quick pass searched `/Users/kyx/.codex/memories/MEMORY.md` for relevant
issue/cache-setting history. Only unrelated older response-cache material was
found. No memory entry, memory-derived fact, rollout summary, or rollout ID was
used; implementation and verification came from the current plan, brief,
Task 1/2 reports, code, and tests.

## Concerns / follow-up boundaries

- The current environment did not provide a live PostgreSQL integration
  target. PostgreSQL column types, migration SQL, placeholders, and
  `FOR UPDATE` selection were reviewed and query behavior is covered where the
  repository test environment permits; SQLite received the live migration and
  concurrency coverage.
- Fleet convergence is intentionally eventual: a healthy instance observes a
  committed generation on the fixed five-second poll cadence.
- Sync status is tracked now but remains intentionally absent from Ops output
  until Task 4.
- Frontend settings inputs and observability remain Task 4 scope.
