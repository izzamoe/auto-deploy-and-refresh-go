"use client";

// Destructive confirmation: focus-trapped Radix dialog, Escape cancels, nothing runs before the
// click. The dialog itself is loaded on demand (T070, FR-027): triggers spread `confirmIntent`
// so hovering/focusing them prefetches it, and it opens on click. Focus returns to the element
// that opened it. A failed load closes it again and the shell asks for a reload (RV-22).
import { useLayoutEffect, useRef } from "react";
import type { ReactNode } from "react";
import { lazyModule, useLazyModule } from "./lazy";

const confirmModule = lazyModule(() => import("./ConfirmDialogContent"));

/** Prefetch on intent (pointerenter/focus of a trigger). */
export function preloadConfirmDialog() {
  void confirmModule.load();
}

/** Spread on the button that opens a ConfirmDialog. */
export const confirmIntent = { onPointerEnter: preloadConfirmDialog, onFocus: preloadConfirmDialog } as const;

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  body: string;
  confirmLabel: string;
  cancelLabel?: string;
  onConfirm: () => void;
  busy?: boolean;
  children?: ReactNode;
}

export function ConfirmDialog(props: Props) {
  const { open, onOpenChange } = props;
  const mod = useLazyModule(confirmModule, open, () => onOpenChange(false));
  // The element that had focus when the dialog was asked for (its trigger).
  const returnFocus = useRef<HTMLElement | null>(null);
  const wasOpen = useRef(false);
  useLayoutEffect(() => {
    if (open && !wasOpen.current && typeof document !== "undefined") {
      returnFocus.current = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null;
    }
    wasOpen.current = open;
  }, [open]);

  if (!mod) return null;
  return <mod.ConfirmDialogContent {...props} returnFocus={returnFocus} />;
}
