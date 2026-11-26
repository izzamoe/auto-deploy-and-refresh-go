"use client";

// The Radix part of ConfirmDialog, loaded on demand (T070): Radix Dialog and its dependencies
// are not part of the initial JS of any route.
import type { ReactNode, RefObject } from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { useI18n } from "@/i18n";

export interface ConfirmDialogContentProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  body: string;
  confirmLabel: string;
  /** Default `common.cancel`. */
  cancelLabel?: string;
  onConfirm: () => void;
  busy?: boolean;
  /** Extra content between the text and the buttons (e.g. a checkbox). */
  children?: ReactNode;
  /** Focus goes back here when the dialog closes (the element that opened it). */
  returnFocus: RefObject<HTMLElement | null>;
}

export function ConfirmDialogContent({ open, onOpenChange, title, body, confirmLabel, cancelLabel, onConfirm, busy = false, children, returnFocus }: ConfirmDialogContentProps) {
  const { t } = useI18n();
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="rounded-card"
        onCloseAutoFocus={(e) => {
          const el = returnFocus.current;
          if (el && el.isConnected) {
            e.preventDefault();
            el.focus();
          }
        }}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{body}</DialogDescription>
        </DialogHeader>
        {children}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
            {cancelLabel ?? t("common.cancel")}
          </Button>
          <Button type="button" variant="destructive" disabled={busy} onClick={onConfirm}>
            {confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
