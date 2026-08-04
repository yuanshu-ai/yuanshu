import type { SVGProps } from "react";

export type IconName = "home" | "tasks" | "bell" | "settings" | "node" | "folder" | "plus" | "back" | "send" | "stop" | "refresh" | "copy" | "terminal" | "tool" | "file" | "warning" | "check" | "chevron" | "search" | "lock" | "details" | "close";

export function Icon({ name, ...props }: { name: IconName } & SVGProps<SVGSVGElement>) {
  const common = { fill: "none", stroke: "currentColor", strokeWidth: 1.7, strokeLinecap: "round" as const, strokeLinejoin: "round" as const };
  return <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false" {...props} {...common}>{paths[name]}</svg>;
}

const paths: Record<IconName, React.ReactNode> = {
  home: <><path d="M3.5 10.7 12 3.8l8.5 6.9" /><path d="M5.5 9.5v10h13v-10M9.5 19.5v-6h5v6" /></>,
  tasks: <><rect x="4" y="4" width="16" height="16" rx="2" /><path d="m8 9 1.4 1.4L12 7.8M14 9h3M8 15h9" /></>,
  bell: <><path d="M6.5 10a5.5 5.5 0 0 1 11 0c0 4 1.5 5 2 6h-15c.5-1 2-2 2-6" /><path d="M10 19h4" /></>,
  settings: <><circle cx="12" cy="12" r="3" /><path d="M19 12a7 7 0 0 0-.1-1l2-1.5-2-3.4-2.4 1a8 8 0 0 0-1.8-1L14.4 3h-4.8l-.3 3.1a8 8 0 0 0-1.8 1l-2.4-1-2 3.4 2 1.5a7 7 0 0 0 0 2l-2 1.5 2 3.4 2.4-1a8 8 0 0 0 1.8 1l.3 3.1h4.8l.3-3.1a8 8 0 0 0 1.8-1l2.4 1 2-3.4-2-1.5a7 7 0 0 0 .1-1Z" /></>,
  node: <><rect x="4" y="5" width="16" height="12" rx="2" /><path d="M8 21h8M12 17v4" /></>,
  folder: <path d="M3.5 6.5h6l2 2h9v10h-17z" />,
  plus: <path d="M12 5v14M5 12h14" />,
  back: <path d="m14.5 5-7 7 7 7" />,
  send: <><path d="m4 4 17 8-17 8 3-8z" /><path d="M7 12h14" /></>,
  stop: <rect x="7" y="7" width="10" height="10" rx="1" />,
  refresh: <><path d="M20 7v5h-5" /><path d="M18.4 16a8 8 0 1 1 .6-8l1 4" /></>,
  copy: <><rect x="8" y="8" width="11" height="11" rx="2" /><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3" /></>,
  terminal: <><rect x="3" y="4" width="18" height="16" rx="2" /><path d="m7 9 3 3-3 3M13 15h4" /></>,
  tool: <><path d="M14 6a4 4 0 0 0-5 5L3.5 16.5l4 4L13 15a4 4 0 0 0 5-5l-3 3-3-3z" /></>,
  file: <><path d="M6 3h8l4 4v14H6z" /><path d="M14 3v5h5" /></>,
  warning: <><path d="M12 3 2.8 20h18.4z" /><path d="M12 9v5M12 17.2v.1" /></>,
  check: <path d="m5 12 4 4 10-10" />,
  chevron: <path d="m8 10 4 4 4-4" />,
  search: <><circle cx="10.5" cy="10.5" r="6.5" /><path d="m15.5 15.5 5 5" /></>,
  lock: <><rect x="5" y="10" width="14" height="11" rx="2" /><path d="M8 10V7a4 4 0 0 1 8 0v3" /></>,
  details: <><path d="M4 6h16M4 12h16M4 18h16" /><path d="M8 4v4M15 10v4M11 16v4" /></>,
  close: <path d="m6 6 12 12M18 6 6 18" />,
};
