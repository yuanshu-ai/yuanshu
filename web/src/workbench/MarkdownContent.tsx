import { Children, isValidElement, type ReactNode } from "react";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";

import { Icon } from "./Icon";

export default function MarkdownContent({ value }: { value: string }) {
  return <div className="markdown-content"><ReactMarkdown
    remarkPlugins={[remarkGfm]}
    skipHtml
    urlTransform={(url) => safeURL(url) ? defaultUrlTransform(url) : ""}
    components={{
      img: () => null,
      a: ({ href, children }) => href ? <a href={href} target="_blank" rel="noopener noreferrer">{children}</a> : <span>{children}</span>,
      pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
    }}
  >{value}</ReactMarkdown></div>;
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const text = textContent(children);
  return <div className="code-block"><button type="button" className="copy-button" onClick={() => void copyText(text)} aria-label="复制代码"><Icon name="copy" /></button><pre>{children}</pre></div>;
}

function textContent(value: ReactNode): string {
  return Children.toArray(value).map((child) => {
    if (typeof child === "string" || typeof child === "number") return String(child);
    if (isValidElement<{ children?: ReactNode }>(child)) return textContent(child.props.children);
    return "";
  }).join("");
}

function safeURL(value: string): boolean {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" || parsed.protocol === "http:";
  } catch {
    return false;
  }
}

async function copyText(value: string): Promise<void> {
  if (navigator.clipboard && value) await navigator.clipboard.writeText(value);
}
