import * as React from "react"
import { cva, type VariantProps } from "class-variance-authority"

import { cn } from "@/lib/utils"

const labelVariants = cva(
  "text-sm font-medium leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-70"
)

// 002 T072 bundle work (FR-027, NFR-005): a native <label> with the behaviour of the Radix label
// it replaces (a double click on the label does not select its text), without Radix's
// primitive/slot runtime in the form routes.
const Label = React.forwardRef<
  HTMLLabelElement,
  React.LabelHTMLAttributes<HTMLLabelElement> & VariantProps<typeof labelVariants>
>(({ className, onMouseDown, ...props }, ref) => (
  <label
    ref={ref}
    className={cn(labelVariants(), className)}
    onMouseDown={(e) => {
      // Only a label click, not one on a control inside it (as Radix does).
      const target = e.target as HTMLElement
      if (target.closest("button, input, select, textarea")) return
      onMouseDown?.(e)
      if (!e.defaultPrevented && e.detail > 1) e.preventDefault()
    }}
    {...props}
  />
))
Label.displayName = "Label"

export { Label }
