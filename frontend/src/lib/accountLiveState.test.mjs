import assert from "node:assert/strict";
import test from "node:test";
import { mergeAccountLiveState } from "./accountLiveState.ts";

const baseRow = {
  id: 1,
  status: "active",
  using_credits: false,
  usage_percent_5h: 40,
  reset_5h_at: "2026-10-18T17:00:00+08:00",
  codex_5h_usage_updated_at: "2026-10-18T13:00:00+08:00",
  usage_percent_7d: 31,
  reset_7d_at: "2026-10-25T07:00:00+08:00",
  codex_usage_updated_at: "2026-10-18T13:00:00+08:00",
  active_requests: 1,
  occupied_requests: 1,
  dynamic_concurrency_limit: 5,
  base_concurrency_effective: 5,
};

function live(overrides = {}) {
  return {
    session_slot_buffer_enabled: false,
    accounts: {
      "1": {
        active_requests: 1,
        occupied_requests: 1,
        dynamic_concurrency_limit: 5,
        base_concurrency_effective: 5,
        status: "active",
        using_credits: false,
        usage_percent_5h: 40,
        reset_5h_at: baseRow.reset_5h_at,
        codex_5h_usage_updated_at: baseRow.codex_5h_usage_updated_at,
        usage_percent_7d: baseRow.usage_percent_7d,
        reset_7d_at: baseRow.reset_7d_at,
        codex_usage_updated_at: baseRow.codex_usage_updated_at,
        ...overrides,
      },
    },
  };
}

test("no live entry keeps the original array", () => {
  const rows = [baseRow];
  assert.equal(
    mergeAccountLiveState(rows, { accounts: {}, session_slot_buffer_enabled: false }),
    rows,
  );
});

test("unchanged live fields keep the same row object", () => {
  const rows = [baseRow];
  const merged = mergeAccountLiveState(rows, live());
  assert.equal(merged, rows);
  assert.equal(merged[0], baseRow);
});

test("usage change only replaces the affected row", () => {
  const other = { ...baseRow, id: 2 };
  const rows = [baseRow, other];
  const merged = mergeAccountLiveState(rows, live({ usage_percent_5h: 91, status: "quota_paused" }));
  assert.notEqual(merged, rows);
  assert.notEqual(merged[0], baseRow);
  assert.equal(merged[0].usage_percent_5h, 91);
  assert.equal(merged[0].status, "quota_paused");
  assert.equal(merged[1], other);
});

test("older backend without usage keys preserves the list values", () => {
  const rows = [baseRow];
  const merged = mergeAccountLiveState(rows, {
    session_slot_buffer_enabled: false,
    accounts: {
      "1": {
        active_requests: 2,
        occupied_requests: 2,
        dynamic_concurrency_limit: 4,
      },
    },
  });
  assert.equal(merged[0].usage_percent_5h, 40);
  assert.equal(merged[0].reset_5h_at, baseRow.reset_5h_at);
  assert.equal(merged[0].status, "active");
  assert.equal(merged[0].active_requests, 2);
});

test("explicit null clears an invalid window", () => {
  const rows = [baseRow];
  const merged = mergeAccountLiveState(rows, live({
    usage_percent_5h: null,
    reset_5h_at: null,
    codex_5h_usage_updated_at: null,
  }));
  assert.equal(merged[0].usage_percent_5h, null);
  assert.equal(merged[0].reset_5h_at, undefined);
  assert.equal(merged[0].codex_5h_usage_updated_at, undefined);
});
