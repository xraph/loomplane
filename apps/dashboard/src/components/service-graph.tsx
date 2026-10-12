// The service graph: what an analysis found, drawn left to right from the
// services, frontends first, to the resources they need. A need that comes
// through a shared package is drawn from the service that uses the package.
// Frontends share the services' column because an analysis finds no links
// between them, and a column of their own would draw their edges straight
// through the services. Clouds aren't lanes yet: nothing is placed on one
// until deployments arrive.
import {
  BoxIcon,
  DatabaseIcon,
  type LucideIcon,
  MegaphoneIcon,
  SearchIcon,
  ZapIcon,
} from "lucide-react"
import { createElement, useEffect, useMemo, useRef, useState } from "react"

import type { Graph, Resource, Service } from "@/client/forge/loomplane/types"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  clusters,
  connections,
  ecosystemLabel,
  kindLabel,
  resourceLabel,
} from "@/lib/architecture"
import { cn } from "@/lib/utils"

export type GraphSelection =
  { kind: "service"; id: string } | { kind: "resource"; id: string }

type GNode = {
  id: string
  kind: "service" | "resource"
  col: number
  x: number
  y: number
  w: number
  h: number
  service?: Service
  resource?: Resource
}

type GEdge = { from: string; to: string; tentative: boolean }

// Columns stretch to fill the card; these are the floors.
const minColW = 260
const maxNodeW = 236
const nodeH = 56
const gapY = 12
const padX = 20
const padTop = 18
const padBottom = 18

const frontendKinds = new Set(["web-app", "static-site"])

const resourceIcons: Record<string, LucideIcon> = {
  postgres: DatabaseIcon,
  mysql: DatabaseIcon,
  mongodb: DatabaseIcon,
  sqlserver: DatabaseIcon,
  clickhouse: DatabaseIcon,
  redis: ZapIcon,
  memcached: ZapIcon,
  rabbitmq: MegaphoneIcon,
  kafka: MegaphoneIcon,
  nats: MegaphoneIcon,
  "object-storage": BoxIcon,
  search: SearchIcon,
}

/** ResourceGlyph draws the icon for a kind of resource, such as postgres. */
export function ResourceGlyph({
  kind,
  className,
}: {
  kind: string
  className?: string
}) {
  return createElement(resourceIcons[kind] ?? BoxIcon, { className })
}

const ecosystemShort: Record<string, string> = {
  go: "Go",
  rust: "Rust",
  node: "Node",
  container: "Docker",
}

/** needsReview reports whether a resource is only a suggestion so far. */
export function needsReview(r: Resource): boolean {
  return r.needsConfirmation || r.confidence === "possible"
}

type Column = { title: string; items: Omit<GNode, "x" | "y" | "col">[] }

/** columnsOf groups the graph's nodes, leaving out columns with nothing in them. */
function columnsOf(graph: Graph, nodeW: number): Column[] {
  // Frontends first, then the other deployable services, then what isn't
  // deployed: the order people read an application in.
  const rank = (s: Service) =>
    !s.deployable ? 2 : frontendKinds.has(s.kind) ? 0 : 1
  const ordered = [...graph.services].sort((a, b) => rank(a) - rank(b))
  const asNode = (s: Service) => ({
    id: s.id,
    kind: "service" as const,
    w: nodeW,
    h: nodeH,
    service: s,
  })
  // With several repositories, each gets its own column, so you can see
  // which one a service lives in.
  const groups = clusters(graph)
  const serviceColumns =
    groups.length > 1
      ? groups.map((g) => ({
          title: g.alias,
          items: [...g.services].sort((a, b) => rank(a) - rank(b)).map(asNode),
        }))
      : [{ title: "Services", items: ordered.map(asNode) }]
  return [
    ...serviceColumns,
    {
      title: "Resources",
      items: graph.resources.map((r) => ({
        id: r.id,
        kind: "resource" as const,
        w: nodeW,
        h: nodeH,
        resource: r,
      })),
    },
  ].filter((c) => c.items.length > 0)
}

function layout(columns: Column[], graph: Graph, colW: number, nodeW: number) {
  const nodes: GNode[] = []
  let tallest = nodeH
  columns.forEach((c, col) => {
    let y = padTop
    for (const n of c.items) {
      nodes.push({ ...n, col, x: padX + col * colW, y })
      y += n.h + gapY
    }
    tallest = Math.max(tallest, y - gapY)
  })

  const edges: GEdge[] = []
  for (const s of graph.services) {
    for (const need of connections(graph, s.id).needs) {
      edges.push({
        from: s.id,
        to: need.resource.id,
        tentative: need.confidence === "possible" || needsReview(need.resource),
      })
    }
  }
  return {
    nodes,
    edges,
    width: padX * 2 + Math.max(columns.length - 1, 0) * colW + nodeW,
    height: tallest + padBottom,
  }
}

function edgePath(a: GNode, b: GNode) {
  const x1 = a.x + a.w
  const y1 = a.y + a.h / 2
  const x2 = b.x
  const y2 = b.y + b.h / 2
  const dx = Math.max((x2 - x1) / 2, 24)
  return `M${x1} ${y1}C${x1 + dx} ${y1} ${x2 - dx} ${y2} ${x2} ${y2}`
}

export function ServiceGraph({
  graph,
  selected,
  onSelect,
}: {
  graph: Graph
  selected: GraphSelection | null
  onSelect: (s: GraphSelection) => void
}) {
  const frame = useRef<HTMLDivElement>(null)
  const [frameW, setFrameW] = useState(0)
  useEffect(() => {
    const el = frame.current
    if (!el) return
    const ro = new ResizeObserver(([entry]) => {
      if (entry) setFrameW(entry.contentRect.width)
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  const count = columnsOf(graph, maxNodeW).length
  const colW = Math.max(
    minColW,
    Math.floor((frameW - padX * 2 - maxNodeW) / Math.max(count - 1, 1))
  )
  const nodeW = Math.min(maxNodeW, colW - 48)
  const columns = useMemo(() => columnsOf(graph, nodeW), [graph, nodeW])
  const g = useMemo(
    () => layout(columns, graph, colW, nodeW),
    [columns, graph, colW, nodeW]
  )
  const [hover, setHover] = useState<string | null>(null)
  const focus = hover ?? selected?.id ?? null
  const related = useMemo(() => {
    if (!focus) return null
    const set = new Set([focus])
    for (const e of g.edges) {
      if (e.from === focus) set.add(e.to)
      if (e.to === focus) set.add(e.from)
    }
    return set
  }, [focus, g.edges])
  const byId = new Map(g.nodes.map((n) => [n.id, n]))

  return (
    <Card ref={frame} className="gap-0 overflow-x-auto py-0">
      <div style={{ width: Math.max(g.width, frameW) }}>
        <div className="flex border-b text-xs text-muted-foreground">
          {columns.map((c, i) => (
            <span
              key={c.title}
              className="py-2.5"
              style={{
                width: i === columns.length - 1 ? nodeW + padX : colW,
                paddingLeft: i === 0 ? padX : 0,
              }}
            >
              {c.title}
            </span>
          ))}
        </div>
        <div className="canvas-dots relative" style={{ height: g.height }}>
          <svg
            className="pointer-events-none absolute inset-0"
            width={g.width}
            height={g.height}
            aria-hidden="true"
          >
            {g.edges.map((e) => {
              const a = byId.get(e.from)
              const b = byId.get(e.to)
              if (!a || !b) return null
              const on = focus !== null && (e.from === focus || e.to === focus)
              return (
                <path
                  key={`${e.from}-${e.to}`}
                  d={edgePath(a, b)}
                  fill="none"
                  strokeWidth={on ? 1.75 : 1.25}
                  strokeDasharray={e.tentative ? "4 4" : undefined}
                  className={cn(
                    "transition-[stroke,opacity] duration-150",
                    on ? "stroke-foreground" : "stroke-foreground/20",
                    focus && !on && "opacity-40"
                  )}
                />
              )
            })}
          </svg>
          {g.nodes.map((n) => (
            <Node
              key={n.id}
              n={n}
              dim={related !== null && !related.has(n.id)}
              active={selected?.id === n.id}
              onHover={setHover}
              onSelect={onSelect}
            />
          ))}
        </div>
      </div>
    </Card>
  )
}

function Node({
  n,
  dim,
  active,
  onHover,
  onSelect,
}: {
  n: GNode
  dim: boolean
  active: boolean
  onHover: (id: string | null) => void
  onSelect: (s: GraphSelection) => void
}) {
  const style = { left: n.x, top: n.y, width: n.w, height: n.h }
  const common = cn(
    "absolute flex h-auto items-center justify-start gap-2.5 rounded-xl border bg-background px-3 text-left text-sm font-normal whitespace-normal shadow-[0_1px_2px_0_rgb(0_0_0/0.05),0_4px_12px_-6px_rgb(0_0_0/0.08)] transition-[opacity,box-shadow,border-color] duration-150 outline-none hover:border-foreground/30 hover:bg-background focus-visible:ring-3 focus-visible:ring-ring/50",
    dim && "opacity-45",
    active && "border-foreground/60 ring-3 ring-foreground/10"
  )
  const hoverProps = {
    onMouseEnter: () => onHover(n.id),
    onMouseLeave: () => onHover(null),
    onFocus: () => onHover(n.id),
    onBlur: () => onHover(null),
  }
  if (n.kind === "resource" && n.resource) {
    const r = n.resource
    const review = needsReview(r)
    return (
      <Button
        variant="outline"
        style={style}
        className={cn(
          common,
          review && "border-dashed border-weld bg-weld/5 shadow-none"
        )}
        onClick={() => onSelect({ kind: "resource", id: r.id })}
        {...hoverProps}
      >
        <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted">
          <ResourceGlyph
            kind={r.kind}
            className="size-3.5 text-muted-foreground"
          />
        </span>
        <span className="grid min-w-0 flex-1 leading-tight">
          <span className="truncate font-medium">{resourceLabel(r.kind)}</span>
          <span className="truncate text-xs text-muted-foreground">
            {review
              ? "Suggested, needs review"
              : r.confidence === "certain"
                ? "Found for certain"
                : "Likely"}
          </span>
        </span>
      </Button>
    )
  }
  const s = n.service as Service
  return (
    <Button
      variant="outline"
      style={style}
      className={cn(
        common,
        !s.deployable && "border-dashed bg-muted/40 shadow-none"
      )}
      onClick={() => onSelect({ kind: "service", id: s.id })}
      {...hoverProps}
    >
      <span className="grid min-w-0 flex-1 leading-tight">
        <span className="truncate font-medium">{s.name}</span>
        <span className="truncate text-xs text-muted-foreground">
          {s.deployable
            ? [kindLabel(s.kind), s.framework].filter(Boolean).join(", ")
            : "Not deployed"}
        </span>
      </span>
      <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
        {ecosystemShort[s.ecosystem] ?? ecosystemLabel(s.ecosystem)}
      </span>
    </Button>
  )
}
