import * as React from "react"

import { cn } from "@/lib/utils"

// 002 T070 (FR-027, NFR-005): a native `<button role="switch">` with the API and the markup of
// the Radix switch it replaces (checked/defaultChecked/onCheckedChange, aria-checked,
// data-state, label association through `id`), without Radix's runtime in the route bundle.
// Every switch of the admin is controlled by React state (no native form submission).
type SwitchProps = Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "onChange" | "value" | "defaultValue"> & {
  checked?: boolean
  defaultChecked?: boolean
  onCheckedChange?: (checked: boolean) => void
}

const Switch = React.forwardRef<HTMLButtonElement, SwitchProps>(
  ({ className, checked, defaultChecked = false, onCheckedChange, onClick, disabled, ...props }, ref) => {
    const [uncontrolled, setUncontrolled] = React.useState(defaultChecked)
    const on = checked ?? uncontrolled
    const state = on ? "checked" : "unchecked"
    return (
      <button
        type="button"
        role="switch"
        aria-checked={on}
        data-state={state}
        data-disabled={disabled ? "" : undefined}
        disabled={disabled}
        value="on"
        className={cn(
          "peer inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent shadow-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-input",
          className
        )}
        {...props}
        onClick={(e) => {
          onClick?.(e)
          if (e.defaultPrevented) return
          const next = !on
          if (checked === undefined) setUncontrolled(next)
          onCheckedChange?.(next)
        }}
        ref={ref}
      >
        <span
          data-state={state}
          className={cn(
            "pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform data-[state=checked]:translate-x-4 data-[state=unchecked]:translate-x-0"
          )}
        />
      </button>
    )
  }
)
Switch.displayName = "Switch"

export { Switch }
