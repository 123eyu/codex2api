import type { AccountLiveStateResponse } from '../types'

// Account rows carry a large optional usage/status projection from the list
// API. The live poll refreshes the same fields in place so the card's bars,
// status badge and countdown react to per-turn WS usage observations without a
// list reload. Missing keys mean "older backend: keep the list value"; an
// explicit null means "the window is no longer valid: clear it".
export type AccountLiveMergeable = {
  id: number
  active_requests?: number
  occupied_requests?: number
  session_slot_buffer_enabled?: boolean
  dynamic_concurrency_limit?: number
  base_concurrency_effective?: number
  status?: string
  using_credits?: boolean
  usage_percent_5h?: number | null
  reset_5h_at?: string | null
  codex_5h_usage_updated_at?: string | null
  usage_percent_7d?: number | null
  reset_7d_at?: string | null
  codex_usage_updated_at?: string | null
}

export function mergeAccountLiveState<T extends AccountLiveMergeable>(
  current: T[],
  response: AccountLiveStateResponse,
): T[] {
  let changed = false
  const next = current.map((account) => {
    const live = response.accounts[String(account.id)]
    if (!live) return account

    const activeRequests = live.active_requests ?? 0
    const occupiedRequests = live.occupied_requests ?? activeRequests
    const slotBufferEnabled = response.session_slot_buffer_enabled === true
    // Concurrency caps are optional so an older backend keeps the list values.
    const dynamicLimit = typeof live.dynamic_concurrency_limit === 'number'
      ? live.dynamic_concurrency_limit
      : account.dynamic_concurrency_limit
    const baseConcurrency = typeof live.base_concurrency_effective === 'number'
      ? live.base_concurrency_effective
      : account.base_concurrency_effective
    // Usage/status fields follow the same contract: undefined keeps the row,
    // null clears it. The list API omits some fields for Antigravity rows.
    const status = live.status === undefined ? account.status : live.status
    const usingCredits = live.using_credits === undefined
      ? account.using_credits
      : live.using_credits
    const usage5h = live.usage_percent_5h === undefined
      ? account.usage_percent_5h
      : live.usage_percent_5h
    const reset5h = live.reset_5h_at === undefined
      ? account.reset_5h_at
      : (live.reset_5h_at ?? undefined)
    const updated5h = live.codex_5h_usage_updated_at === undefined
      ? account.codex_5h_usage_updated_at
      : (live.codex_5h_usage_updated_at ?? undefined)
    const usage7d = live.usage_percent_7d === undefined
      ? account.usage_percent_7d
      : live.usage_percent_7d
    const reset7d = live.reset_7d_at === undefined
      ? account.reset_7d_at
      : (live.reset_7d_at ?? undefined)
    const updated7d = live.codex_usage_updated_at === undefined
      ? account.codex_usage_updated_at
      : (live.codex_usage_updated_at ?? undefined)

    if (
      (account.active_requests ?? 0) === activeRequests &&
      (account.occupied_requests ?? account.active_requests ?? 0) === occupiedRequests &&
      (account.session_slot_buffer_enabled ?? false) === slotBufferEnabled &&
      account.dynamic_concurrency_limit === dynamicLimit &&
      account.base_concurrency_effective === baseConcurrency &&
      account.status === status &&
      account.using_credits === usingCredits &&
      account.usage_percent_5h === usage5h &&
      account.reset_5h_at === reset5h &&
      account.codex_5h_usage_updated_at === updated5h &&
      account.usage_percent_7d === usage7d &&
      account.reset_7d_at === reset7d &&
      account.codex_usage_updated_at === updated7d
    ) return account

    changed = true
    return {
      ...account,
      active_requests: activeRequests,
      occupied_requests: occupiedRequests,
      session_slot_buffer_enabled: slotBufferEnabled,
      dynamic_concurrency_limit: dynamicLimit,
      base_concurrency_effective: baseConcurrency,
      status,
      using_credits: usingCredits,
      usage_percent_5h: usage5h,
      reset_5h_at: reset5h,
      codex_5h_usage_updated_at: updated5h,
      usage_percent_7d: usage7d,
      reset_7d_at: reset7d,
      codex_usage_updated_at: updated7d,
    }
  })
  return changed ? next : current
}
