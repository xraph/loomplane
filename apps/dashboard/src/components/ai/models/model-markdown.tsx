// A model card from Hugging Face, as its author wrote it in Markdown. The
// card is someone else's text, so raw HTML in it is dropped, never
// rendered, and links open in a new tab without a referrer.
import Markdown, { type Components } from "react-markdown"
import remarkGfm from "remark-gfm"

import { cn } from "@/lib/utils"

const components: Components = {
  a: ({ href, children }) => (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="text-primary underline-offset-2 hover:underline"
    >
      {children}
    </a>
  ),
  // Images point anywhere on the web, so a card can't make the page load
  // them: the alt text shows instead.
  img: ({ alt }) =>
    alt ? <span className="text-muted-foreground">[{alt}]</span> : null,
  table: ({ children }) => (
    <div className="my-3 max-w-full overflow-x-auto rounded-lg ring-1 ring-border">
      <table className="w-full text-[12.5px]">{children}</table>
    </div>
  ),
  th: ({ children }) => (
    <th className="border-b bg-subtle px-2.5 py-1.5 text-left font-medium">
      {children}
    </th>
  ),
  td: ({ children }) => (
    <td className="border-b px-2.5 py-1.5 align-top">{children}</td>
  ),
  pre: ({ children }) => (
    <pre className="my-3 max-w-full overflow-x-auto rounded-lg bg-muted p-3 font-mono text-[12px] leading-relaxed">
      {children}
    </pre>
  ),
  code: ({ children, className }) => (
    <code
      className={cn(
        "rounded bg-muted px-1 py-0.5 font-mono text-[12px] [pre_&]:bg-transparent [pre_&]:p-0",
        className
      )}
    >
      {children}
    </code>
  ),
}

export function ModelMarkdown({
  markdown,
  className,
}: {
  markdown: string
  className?: string
}) {
  return (
    <div
      className={cn(
        "min-w-0 text-[13.5px] leading-relaxed break-words",
        "[&_h1]:mt-5 [&_h1]:mb-2 [&_h1]:text-lg [&_h1]:font-semibold",
        "[&_h2]:mt-5 [&_h2]:mb-2 [&_h2]:text-base [&_h2]:font-semibold",
        "[&_h3]:mt-4 [&_h3]:mb-1.5 [&_h3]:text-[14px] [&_h3]:font-semibold",
        "[&_li]:my-0.5 [&_ol]:my-2 [&_ol]:list-decimal [&_ol]:pl-5 [&_p]:my-2 [&_ul]:my-2 [&_ul]:list-disc [&_ul]:pl-5",
        "[&_blockquote]:my-3 [&_blockquote]:border-l-2 [&_blockquote]:pl-3 [&_blockquote]:text-muted-foreground",
        "[&_hr]:my-4 [&>*:first-child]:mt-0",
        className
      )}
    >
      <Markdown remarkPlugins={[remarkGfm]} skipHtml components={components}>
        {markdown}
      </Markdown>
    </div>
  )
}
