// The agent's answers, in Markdown. They're rendered as the model cards
// are: raw HTML is dropped and never rendered, images show their alt text.
// A link to a dashboard page (a path starting with one slash) goes through
// the router, so it stays in the workspace; any other opens in a new tab
// without a referrer.
import type { Components } from "react-markdown"

import { ModelMarkdown } from "@/components/ai/models/model-markdown"
import { Link } from "@/lib/router"

const linkClass = "text-primary underline-offset-2 hover:underline"

/** dashboardPath splits a link to a dashboard page into the router's
 * parts, or says it isn't one. */
export function dashboardPath(
  href: string | undefined
): { to: string; search: Record<string, string>; hash: string } | undefined {
  if (!href || !href.startsWith("/") || href.startsWith("//")) return undefined
  const url = new URL(href, "http://dashboard.invalid")
  return {
    to: url.pathname,
    search: Object.fromEntries(url.searchParams),
    hash: url.hash.replace(/^#/, ""),
  }
}

const components: Components = {
  a: ({ href, children }) => {
    const page = dashboardPath(href)
    if (page)
      return (
        // The agent names a dashboard path; the router prefixes the
        // workspace.
        <Link
          to={page.to as "/"}
          search={page.search as never}
          hash={page.hash || undefined}
          className={linkClass}
        >
          {children}
        </Link>
      )
    return (
      <a href={href} target="_blank" rel="noreferrer" className={linkClass}>
        {children}
      </a>
    )
  },
}

export function AgentMarkdown({ text }: { text: string }) {
  return (
    <ModelMarkdown
      markdown={text}
      components={components}
      className="text-[13.5px]"
    />
  )
}
