import * as DialogPrimitive from "@radix-ui/react-dialog";
import { forwardRef, type HTMLAttributes, type ReactNode } from "react";

import { cx } from "./base";

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;
export const DialogPortal = DialogPrimitive.Portal;

export const DialogOverlay = forwardRef<HTMLDivElement, DialogPrimitive.DialogOverlayProps>(function DialogOverlay({ className, ...props }, ref) {
  return <DialogPrimitive.Overlay ref={ref} className={cx("yu-dialog-overlay", className)} {...props} />;
});

export const DialogContent = forwardRef<HTMLDivElement, DialogPrimitive.DialogContentProps>(function DialogContent({ className, children, ...props }, ref) {
  return <DialogPortal><DialogOverlay /><DialogPrimitive.Content ref={ref} className={cx("yu-dialog-content", className)} {...props}>{children}</DialogPrimitive.Content></DialogPortal>;
});

export function DialogHeader({ children, className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cx("yu-dialog-header", className)} {...props}>{children}</div>;
}

export const DialogTitle = forwardRef<HTMLHeadingElement, DialogPrimitive.DialogTitleProps>(function DialogTitle({ className, ...props }, ref) {
  return <DialogPrimitive.Title ref={ref} className={cx("yu-dialog-title", className)} {...props} />;
});

export const DialogDescription = forwardRef<HTMLParagraphElement, DialogPrimitive.DialogDescriptionProps>(function DialogDescription({ className, ...props }, ref) {
  return <DialogPrimitive.Description ref={ref} className={cx("yu-dialog-description", className)} {...props} />;
});

export function DialogFooter({ children, className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cx("yu-dialog-footer", className)} {...props}>{children}</div>;
}

export const Sheet = Dialog;
export const SheetTrigger = DialogTrigger;
export const SheetClose = DialogClose;
export const SheetTitle = DialogTitle;
export const SheetDescription = DialogDescription;

export const SheetContent = forwardRef<HTMLDivElement, DialogPrimitive.DialogContentProps & { side?: "top" | "right" | "bottom" | "left" }>(function SheetContent({ side = "right", className, children, ...props }, ref) {
  return <DialogPortal><DialogOverlay /><DialogPrimitive.Content ref={ref} className={cx("yu-sheet-content", `yu-sheet-${side}`, className)} {...props}>{children}</DialogPrimitive.Content></DialogPortal>;
});

export type { ReactNode };
