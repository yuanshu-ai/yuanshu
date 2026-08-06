import * as SelectPrimitive from "@radix-ui/react-select";
import { forwardRef } from "react";

import { cx } from "./base";

export const Select = SelectPrimitive.Root;
export const SelectGroup = SelectPrimitive.Group;
export const SelectValue = SelectPrimitive.Value;
export const SelectTrigger = forwardRef<HTMLButtonElement, SelectPrimitive.SelectTriggerProps>(function SelectTrigger({ className, children, ...props }, ref) {
  return <SelectPrimitive.Trigger ref={ref} className={cx("yu-select-trigger", className)} {...props}>{children}<SelectPrimitive.Icon className="yu-select-icon">⌄</SelectPrimitive.Icon></SelectPrimitive.Trigger>;
});
export const SelectContent = forwardRef<HTMLDivElement, SelectPrimitive.SelectContentProps>(function SelectContent({ className, children, position = "popper", ...props }, ref) {
  return <SelectPrimitive.Portal><SelectPrimitive.Content ref={ref} position={position} className={cx("yu-select-content", className)} {...props}><SelectPrimitive.Viewport className="yu-select-viewport">{children}</SelectPrimitive.Viewport></SelectPrimitive.Content></SelectPrimitive.Portal>;
});
export const SelectItem = forwardRef<HTMLDivElement, SelectPrimitive.SelectItemProps>(function SelectItem({ className, children, ...props }, ref) {
  return <SelectPrimitive.Item ref={ref} className={cx("yu-select-item", className)} {...props}><SelectPrimitive.ItemText>{children}</SelectPrimitive.ItemText><SelectPrimitive.ItemIndicator className="yu-select-check">✓</SelectPrimitive.ItemIndicator></SelectPrimitive.Item>;
});
export const SelectLabel = SelectPrimitive.Label;
export const SelectSeparator = SelectPrimitive.Separator;
