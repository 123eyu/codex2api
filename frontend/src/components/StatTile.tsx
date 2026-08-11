import type { ReactNode } from 'react'
import { cn } from '../lib/utils'

export type StatTileTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger'

const SURFACE_TONE: Record<StatTileTone, string> = {
  neutral: 'border-border bg-card/85',
  info: 'border-border bg-card/85',
  success: 'border-border bg-card/85',
  warning: 'border-amber-500/25 bg-amber-500/10',
  danger: 'border-destructive/20 bg-destructive/10',
}

const ICON_TONE: Record<StatTileTone, string> = {
  neutral: 'bg-muted/60 text-muted-foreground',
  info: 'bg-blue-500/10 text-blue-600 dark:bg-blue-500/20 dark:text-blue-300',
  success: 'bg-emerald-500/10 text-emerald-600 dark:bg-emerald-500/20 dark:text-emerald-300',
  warning: 'bg-amber-500/10 text-amber-600 dark:bg-amber-500/20 dark:text-amber-300',
  danger: 'bg-red-500/10 text-red-600 dark:bg-red-500/20 dark:text-red-300',
}

/**
 * 页面级 KPI 磁贴，统一各运维/监控页各自手搓的 SummaryPill。
 * 标签/数值排版跟随 Settings 页 StatusTile 的 house 规格
 * （uppercase tracking-wider 标签 + tabular-nums 数值）。
 * tone 默认只染图标芯片；toneSurface 时整块表面着色，用于健康态直陈。
 */
export function StatTile({
  label,
  value,
  icon,
  tone = 'neutral',
  toneSurface = false,
  className,
}: {
  label: string
  value: ReactNode
  icon?: ReactNode
  tone?: StatTileTone
  toneSurface?: boolean
  className?: string
}) {
  return (
    <div
      className={cn(
        'rounded-lg border px-3 py-2.5 shadow-sm',
        toneSurface ? SURFACE_TONE[tone] : 'border-border bg-card/85',
        className,
      )}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
          {label}
        </div>
        {icon ? (
          <div className={cn('flex size-8 shrink-0 items-center justify-center rounded-lg', ICON_TONE[tone])}>
            {icon}
          </div>
        ) : null}
      </div>
      <div className={cn('mt-2 text-[20px] font-semibold tabular-nums text-foreground', icon && 'leading-none')}>
        {value}
      </div>
    </div>
  )
}
