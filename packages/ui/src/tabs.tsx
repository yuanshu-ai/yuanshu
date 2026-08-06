import * as TabsPrimitive from "@radix-ui/react-tabs";
import { forwardRef } from "react";

import { cx } from "./base";

export const Tabs = TabsPrimitive.Root;
export const TabsList = forwardRef<HTMLDivElement, TabsPrimitive.TabsListProps>(function TabsList({ className, ...props }, ref) {
  return <TabsPrimitive.List ref={ref} className={cx("yu-tabs-list", className)} {...props} />;
});
export const TabsTrigger = forwardRef<HTMLButtonElement, TabsPrimitive.TabsTriggerProps>(function TabsTrigger({ className, ...props }, ref) {
  return <TabsPrimitive.Trigger ref={ref} className={cx("yu-tabs-trigger", className)} {...props} />;
});
export const TabsContent = forwardRef<HTMLDivElement, TabsPrimitive.TabsContentProps>(function TabsContent({ className, ...props }, ref) {
  return <TabsPrimitive.Content ref={ref} className={cx("yu-tabs-content", className)} {...props} />;
});
