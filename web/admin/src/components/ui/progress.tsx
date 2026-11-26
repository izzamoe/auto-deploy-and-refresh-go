import * as React from "react"

import { cn } from "@/lib/utils"

// 002 T070 (FR-027, NFR-005): the markup of the Radix progress it replaces (role="progressbar",
// aria-valuemin/max/now, aria-valuetext, data-state/data-value/data-max), without Radix's
// runtime in the route bundle. `value` null/undefined = indeterminate.
type ProgressProps = React.HTMLAttributes<HTMLDivElement> & {
  value?: number | null
  max?: number
}

const Progress = React.forwardRef<HTMLDivElement, ProgressProps>(({ className, value, max = 100, ...props }, ref) => {
  const known = typeof value === "number" && Number.isFinite(value)
  const v = known ? Math.min(Math.max(value, 0), max) : null
  const state = v === null ? "indeterminate" : v === max ? "complete" : "loading"
  return (
    <div
      ref={ref}
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={v ?? undefined}
      aria-valuetext={v === null ? undefined : `${Math.round((v / max) * 100)}%`}
      data-state={state}
      data-value={v ?? undefined}
      data-max={max}
      className={cn(
        "relative h-2 w-full overflow-hidden rounded-full bg-primary/20",
        className
      )}
      {...props}
    >
      <div
        data-state={state}
        data-value={v ?? undefined}
        data-max={max}
        className="h-full w-full flex-1 bg-primary transition-all"
        style={{ transform: `translateX(-${100 - (value || 0)}%)` }}
      />
    </div>
  )
})
Progress.displayName = "Progress"

export { Progress }
