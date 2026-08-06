import * as DropdownMenuPrimitive from "@radix-ui/react-dropdown-menu";
import { forwardRef } from "react";

import { cx } from "./base";

export const DropdownMenu = DropdownMenuPrimitive.Root;
export const DropdownMenuTrigger = DropdownMenuPrimitive.Trigger;
export const DropdownMenuGroup = DropdownMenuPrimitive.Group;
export const DropdownMenuItem = forwardRef<HTMLDivElement, DropdownMenuPrimitive.DropdownMenuItemProps>(function DropdownMenuItem({ className, ...props }, ref) {
  return <DropdownMenuPrimitive.Item ref={ref} className={cx("yu-menu-item", className)} {...props} />;
});
export const DropdownMenuContent = forwardRef<HTMLDivElement, DropdownMenuPrimitive.DropdownMenuContentProps>(function DropdownMenuContent({ className, ...props }, ref) {
  return <DropdownMenuPrimitive.Portal><DropdownMenuPrimitive.Content ref={ref} className={cx("yu-menu-content", className)} {...props} /></DropdownMenuPrimitive.Portal>;
});
export const DropdownMenuSeparator = DropdownMenuPrimitive.Separator;
