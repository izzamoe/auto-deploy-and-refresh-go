import * as React from "react"

import { cn } from "@/lib/utils"

// 002 T072 bundle work (FR-027, NFR-005): the `asChild` slot of <Button>, with the merge rules
// of the Radix Slot it replaces, for the one shape the admin uses (a single element child, a
// link): the slot's props apply to the child, the child's own props win, event handlers of both
// run (child first), styles merge, class names join, and both refs receive the node.
type AnyProps = Record<string, unknown>
type AnyHandler = (...args: unknown[]) => unknown

type NodeRef = React.Ref<HTMLElement> | undefined

function setRef(ref: NodeRef, node: HTMLElement | null) {
  if (typeof ref === "function") ref(node)
  else if (ref) (ref as React.RefObject<HTMLElement | null>).current = node
}

const Slot = React.forwardRef<HTMLElement, React.HTMLAttributes<HTMLElement> & { children?: React.ReactNode }>(
  ({ children, ...slotProps }, forwardedRef) => {
    const child = React.isValidElement<AnyProps>(children) ? children : null
    const childRef = child ? (child.props.ref as NodeRef) : undefined
    const ref = React.useCallback(
      (node: HTMLElement | null) => {
        setRef(forwardedRef, node)
        setRef(childRef, node)
      },
      [forwardedRef, childRef]
    )
    if (!child) return null
    const own = child.props
    const merged: AnyProps = { ...slotProps, ...own }
    for (const [key, value] of Object.entries(slotProps as AnyProps)) {
      const mine = own[key]
      if (/^on[A-Z]/.test(key) && typeof value === "function" && typeof mine === "function") {
        merged[key] = (...args: unknown[]) => {
          ;(mine as AnyHandler)(...args)
          ;(value as AnyHandler)(...args)
        }
      } else if (key === "style") {
        merged.style = { ...(value as object), ...(mine as object) }
      } else if (key === "className") {
        merged.className = cn(value as string, mine as string)
      }
    }
    merged.ref = ref
    return React.cloneElement(child, merged)
  }
)
Slot.displayName = "Slot"

export { Slot }
