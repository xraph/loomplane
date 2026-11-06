// The agent's answers, in Markdown. They're rendered as the model cards
// are: raw HTML is dropped and never rendered, images show their alt text.
// A fenced block gets its language, a copy button and colour, and three
// fences draw instead (lib/agent-blocks.ts): ```chart, ```stats and
// ```timeline. A GitHub-style "> [!NOTE]" quote is a callout. A table cell
// that's only a status word shows as a status, a number aligns right, and
// a table copies as CSV.
// A link to a dashboard page (a path starting with one slash) goes through
// the router, so it stays in the workspace, and shows what kind of page it
// is; any other opens in a new tab without a referrer, marked as leaving.
import {
  ArrowUpRightIcon,
  CloudIcon,
  FileTextIcon,
  FolderSimpleIcon,
  GlobeIcon,
  type Icon,
  PlugsIcon,
  RocketLaunchIcon,
  StackIcon,
} from "@phosphor-icons/react"
import type { Components } from "react-markdown"

import { ModelMarkdown } from "@/components/ai/models/model-markdown"
import { CodeBlock } from "@/components/assistant/code-block"
import {
  Callout,
  ChartBlock,
  StatsBlock,
  TimelineBlock,
  ToneDot,
} from "@/components/assistant/rich-blocks"
import { CopyButton } from "@/components/runs"
import {
  isNumeric,
  parseChart,
  parseStats,
  parseTimeline,
  statusTone,
} from "@/lib/agent-blocks"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

const linkClass =
  "text-primary underline decoration-primary/30 underline-offset-2 hover:decoration-primary"

// What kind of page a dashboard path is, by its first segment.
const pageIcons: [prefix: string, icon: Icon][] = [
  ["/projects", FolderSimpleIcon],
  ["/deployments", RocketLaunchIcon],
  ["/release-reports", FileTextIcon],
  ["/agent/reports", FileTextIcon],
  ["/services", StackIcon],
  ["/clouds", CloudIcon],
  ["/domains", GlobeIcon],
  ["/integrations", PlugsIcon],
]

const iconClass = "mr-0.5 inline size-[0.95em] -translate-y-px align-middle"

/** dashboardPath splits a link to a dashboard page into the router's
 * parts, or says it isn't one. */
export function dashboardPath(
  href: string | undefined
): { to: string; search: Record<string, string>; hash: string } | undefined {
  // "//host" and "/\host" are other sites, whatever they look like.
  if (!href || !href.startsWith("/") || href.startsWith("//")) return undefined
  if (/\\|%5c/i.test(href)) return undefined
  const url = new URL(href, "http://dashboard.invalid")
  return {
    to: url.pathname,
    search: Object.fromEntries(url.searchParams),
    hash: url.hash.replace(/^#/, ""),
  }
}

/** A node of the parsed answer, as much of it as these renderers need. */
type Node = {
  type: string
  tagName?: string
  value?: string
  properties?: { className?: unknown; dataCallout?: unknown }
  children?: Node[]
}

const textOf = (n: Node): string =>
  n.type === "text" ? (n.value ?? "") : (n.children ?? []).map(textOf).join("")

/** fence reads a <pre>'s code and the language its fence named. */
function fence(pre: Node | undefined): { code: string; lang?: string } {
  const code = pre?.children?.find((c) => c.tagName === "code")
  const classes = code?.properties?.className
  const lang = (Array.isArray(classes) ? classes : [])
    .map(String)
    .find((c) => c.startsWith("language-"))
    ?.slice("language-".length)
  return { code: code ? textOf(code) : pre ? textOf(pre) : "", lang }
}

/** A Markdown syntax node, as much of it as the callout plugin needs. */
type MdNode = {
  type: string
  value?: string
  children?: MdNode[]
  data?: { hProperties?: Record<string, string> }
}

const calloutMark = /^\[!(note|tip|important|warning|caution)\][ \t]*\n?/i

/** remarkCallouts marks a quote that opens with [!NOTE], [!TIP],
 * [!IMPORTANT], [!WARNING] or [!CAUTION] as that callout, and takes the
 * marker out of its text. */
function remarkCallouts() {
  const walk = (n: MdNode) => {
    if (n.type === "blockquote") {
      const para = n.children?.[0]
      // The parser can split "[!NOTE]" over several text nodes (a bracket
      // that isn't a link ends one), so the opening text is read whole.
      const lead: MdNode[] = []
      for (const c of para?.type === "paragraph" ? (para.children ?? []) : []) {
        if (c.type !== "text") break
        lead.push(c)
      }
      const m = calloutMark.exec(lead.map((c) => c.value ?? "").join(""))
      if (m && para) {
        let cut = m[0].length
        for (const c of lead) {
          const take = Math.min(cut, (c.value ?? "").length)
          c.value = (c.value ?? "").slice(take)
          cut -= take
        }
        para.children = (para.children ?? []).filter(
          (c) => c.type !== "text" || c.value
        )
        if (para.children[0]?.type === "break") para.children.shift()
        n.data = { hProperties: { dataCallout: m[1].toLowerCase() } }
      }
    }
    n.children?.forEach(walk)
  }
  return (tree: MdNode) => walk(tree)
}

const remarkPlugins = [remarkCallouts]

/** tableCsv reads a table's rows back as CSV. */
function tableCsv(table: Node | undefined): string {
  const rows: string[][] = []
  const visit = (n: Node) => {
    if (n.tagName === "tr")
      rows.push(
        (n.children ?? [])
          .filter((c) => c.tagName === "td" || c.tagName === "th")
          .map((c) => textOf(c).trim())
      )
    else n.children?.forEach(visit)
  }
  if (table) visit(table)
  return rows
    .map((r) =>
      r
        .map((t) => (/[",\n]/.test(t) ? `"${t.replace(/"/g, '""')}"` : t))
        .join(",")
    )
    .join("\n")
}

/** Fence draws a ```chart, ```stats or ```timeline block, or shows any
 * other fence as code. A block the agent is still writing waits as a
 * placeholder; one that's finished and still can't be read shows its
 * source with a line saying so. */
function Fence({ node, live }: { node: Node | undefined; live: boolean }) {
  const { code, lang } = fence(node)
  const kind = lang?.toLowerCase()
  if (kind !== "chart" && kind !== "stats" && kind !== "timeline")
    return <CodeBlock code={code} lang={lang} />
  if (kind === "chart") {
    const r = parseChart(code)
    if (r.ok) return <ChartBlock spec={r.value} />
  } else if (kind === "stats") {
    const r = parseStats(code)
    if (r.ok) return <StatsBlock stats={r.value} />
  } else {
    const r = parseTimeline(code)
    if (r.ok) return <TimelineBlock events={r.value} />
  }
  if (live)
    return (
      <div className="my-3 flex h-24 items-center justify-center gap-2 rounded-xl bg-subtle text-[12.5px] text-muted-foreground ring-1 ring-border">
        <span className="agent-shimmer">
          {kind === "chart"
            ? "Drawing a chart"
            : kind === "stats"
              ? "Adding up the numbers"
              : "Laying out the timeline"}
        </span>
      </div>
    )
  return (
    <div className="my-3">
      <p className="text-[12px] text-muted-foreground">
        This {kind} couldn&apos;t be drawn, so here it is as written.
      </p>
      <CodeBlock code={code} lang="json" className="mt-1" />
    </div>
  )
}

function makeComponents(live: boolean): Components {
  return {
    pre: ({ node }) => <Fence node={node as Node | undefined} live={live} />,
    blockquote: ({ node, children }) => {
      const kind = (node as Node | undefined)?.properties?.dataCallout
      if (
        kind === "note" ||
        kind === "tip" ||
        kind === "important" ||
        kind === "warning" ||
        kind === "caution"
      )
        return <Callout kind={kind}>{children}</Callout>
      return <blockquote>{children}</blockquote>
    },
    table: ({ node, children }) => (
      <div className="group/table relative my-3 max-w-full overflow-x-auto rounded-lg ring-1 ring-border">
        <div className="absolute top-1 right-1 opacity-0 transition-opacity group-hover/table:opacity-100 focus-within:opacity-100">
          <CopyButton
            value={tableCsv(node as Node | undefined)}
            label="Copy as CSV"
          />
        </div>
        <table className="w-full text-[12.5px] [&_tbody_tr:last-child_td]:border-b-0 [&_tbody_tr:nth-child(even)]:bg-subtle/50">
          {children}
        </table>
      </div>
    ),
    th: ({ children, style }) => (
      <th
        style={style}
        className="border-b bg-subtle px-2.5 py-1.5 text-left font-medium whitespace-nowrap"
      >
        {children}
      </th>
    ),
    td: ({ node, children, style }) => {
      const text = textOf(node as Node)
      const tone = statusTone(text)
      if (tone)
        return (
          <td style={style} className="border-b px-2.5 py-1.5 align-top">
            <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
              <ToneDot tone={tone} />
              {children}
            </span>
          </td>
        )
      const numeric = isNumeric(text)
      return (
        <td
          style={style}
          className={cn(
            "border-b px-2.5 py-1.5 align-top",
            numeric && "text-right font-mono whitespace-nowrap tabular-nums"
          )}
        >
          {children}
        </td>
      )
    },
    a: ({ href, children }) => {
      // react-markdown empties an unsafe address (javascript:, data: and the
      // like): the words stay, as text.
      if (!href) return <span>{children}</span>
      const page = dashboardPath(href)
      const PageIcon = page
        ? pageIcons.find(
            ([p]) => page.to === p || page.to.startsWith(`${p}/`)
          )?.[1]
        : undefined
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
            {PageIcon && <PageIcon aria-hidden className={iconClass} />}
            {children}
          </Link>
        )
      // A path dashboardPath turned down ("//host", "/\host") is no link.
      if (href.startsWith("/")) return <span>{children}</span>
      return (
        <a href={href} target="_blank" rel="noreferrer" className={linkClass}>
          {children}
          <ArrowUpRightIcon
            aria-hidden
            className="ml-0.5 inline size-[0.8em] -translate-y-px align-middle opacity-70"
          />
        </a>
      )
    },
  }
}

const finished = makeComponents(false)
const writing = makeComponents(true)

export function AgentMarkdown({
  text,
  live = false,
}: {
  text: string
  /** Whether the agent is still writing it: an unfinished block waits. */
  live?: boolean
}) {
  return (
    <ModelMarkdown
      markdown={text}
      components={live ? writing : finished}
      remarkPlugins={remarkPlugins}
      className="text-[13.5px]"
    />
  )
}
