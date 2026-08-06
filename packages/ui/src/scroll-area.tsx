import * as ScrollAreaPrimitive from "@radix-ui/react-scroll-area";
import { forwardRef } from "react";

import { cx } from "./base";

export const ScrollArea = forwardRef<HTMLDivElement, ScrollAreaPrimitive.ScrollAreaProps>(function ScrollArea({ className, children, ...props }, ref) {
  return <ScrollAreaPrimitive.Root ref={ref} className={cx("yu-scroll-area", className)} {...props}><ScrollAreaPrimitive.Viewport className="yu-scroll-viewport">{children}</ScrollAreaPrimitive.Viewport><ScrollAreaPrimitive.Scrollbar orientation="vertical" className="yu-scrollbar"><ScrollAreaPrimitive.Thumb className="yu-scroll-thumb" /></ScrollAreaPrimitive.Scrollbar></ScrollAreaPrimitive.Root>;
});
