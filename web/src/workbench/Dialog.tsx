import type { ReactNode } from "react";

import { DialogContent, DialogDescription, DialogFooter, Dialog as DialogRoot, DialogTitle } from "@yuanshu/ui/dialog";

export function Dialog({ title, children, actions, onClose, destructive = false, className = "" }: { title: string; children: ReactNode; actions: ReactNode; onClose: () => void; destructive?: boolean; className?: string }) {
  return <DialogRoot open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className={`dialog-panel ${destructive ? "destructive" : ""} ${className}`}>
      <DialogTitle>{title}</DialogTitle>
      <DialogDescription asChild><div className="dialog-content">{children}</div></DialogDescription>
      <DialogFooter className="dialog-actions">{actions}</DialogFooter>
    </DialogContent>
  </DialogRoot>;
}
