import { useEffect, useMemo } from 'react'
import { api } from '../api'
import type { AccountLiveStateResponse } from '../types'

export function useAccountLiveState(
  ids: number[],
  apply: (response: AccountLiveStateResponse) => void,
  enabled = true,
) {
  const idsKey = useMemo(() => ids.join(','), [ids])

  useEffect(() => {
    if (!enabled || !idsKey) return undefined
    let stopped = false
    let timer: number | undefined
    let controller: AbortController | undefined

    const poll = async () => {
      if (stopped) return
      if (document.hidden) {
        timer = window.setTimeout(poll, 1000)
        return
      }
      controller?.abort()
      controller = new AbortController()
      try {
        const response = await api.getAccountLiveState(
          idsKey.split(',').map(Number),
          controller.signal,
        )
        if (!stopped && !controller.signal.aborted) apply(response)
      } catch {
        // The next tick retries; live counters must never disrupt the page.
      }
      if (!stopped) timer = window.setTimeout(poll, 1000)
    }

    void poll()
    return () => {
      stopped = true
      controller?.abort()
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [apply, enabled, idsKey])
}
