"use client";

import { useState } from "react";
import type { FocusEvent, MouseEvent, ReactNode } from "react";

/**
 * Text tooltip for an icon-only control of the sidebar rail (shown on md+, where the rail is
 * vertical). It appears on hover and on keyboard focus, closes with Escape, and is
 * `position: fixed` so the scrollable rail never clips it. The control keeps its own
 * `aria-label`; the tooltip is aria-hidden so the name is not announced twice.
 */
export function RailTip({ label, children, className = "inline-flex" }: { label: string; children: ReactNode; className?: string }) {
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);

  function show(e: MouseEvent<HTMLElement> | FocusEvent<HTMLElement>) {
    const box = e.currentTarget.getBoundingClientRect();
    setPos({ top: box.top + box.height / 2, left: box.right + 12 });
  }

  return (
    <span
      className={className}
      onMouseEnter={show}
      onMouseLeave={() => setPos(null)}
      onFocus={show}
      onBlur={() => setPos(null)}
      onKeyDown={(e) => {
        if (e.key === "Escape") setPos(null);
      }}
    >
      {children}
      {pos && (
        <span
          aria-hidden="true"
          data-testid="rail-tip"
          style={{ top: pos.top, left: pos.left }}
          className="pointer-events-none fixed z-50 hidden -translate-y-1/2 whitespace-nowrap rounded-lg bg-nav px-2.5 py-1.5 text-xs font-medium text-nav-foreground shadow-card md:block"
        >
          {label}
        </span>
      )}
    </span>
  );
}
