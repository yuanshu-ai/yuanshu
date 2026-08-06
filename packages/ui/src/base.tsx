import { forwardRef, type ButtonHTMLAttributes, type HTMLAttributes } from "react";

export function cx(...values: Array<string | false | null | undefined>) {
  return values.filter(Boolean).join(" ");
}

type ButtonVariant = "primary" | "secondary" | "quiet" | "danger" | "warning";
type ButtonSize = "default" | "small" | "icon";

export const Button = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: ButtonSize }>(function Button({ className, variant = "secondary", size = "default", ...props }, ref) {
  return <button ref={ref} className={cx("yu-button", `yu-button-${variant}`, `yu-button-${size}`, className)} {...props} />;
});

export function Badge({ children, variant = "quiet", className, ...props }: HTMLAttributes<HTMLSpanElement> & { variant?: ButtonVariant }) {
  return <span className={cx("yu-badge", `yu-badge-${variant}`, className)} {...props}>{children}</span>;
}

export function Card({ children, className, ...props }: HTMLAttributes<HTMLElement>) {
  return <section className={cx("yu-card", className)} {...props}>{children}</section>;
}

export function Alert({ children, variant = "default", className, ...props }: HTMLAttributes<HTMLDivElement> & { variant?: "default" | "warning" | "danger" | "success" }) {
  return <div role={props.role ?? "status"} className={cx("yu-alert", `yu-alert-${variant}`, className)} {...props}>{children}</div>;
}

export function Skeleton({ className, ...props }: HTMLAttributes<HTMLSpanElement>) {
  return <span aria-hidden="true" className={cx("yu-skeleton", className)} {...props} />;
}
