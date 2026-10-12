// The service graph: one horizontal lane per cloud (the planes in
// Loomplane), with traffic flowing left to right from domains through
// frontends and services to the resources they use.
import {
  BoxIcon,
  DatabaseIcon,
  GlobeIcon,
  type LucideIcon,
  MegaphoneIcon,
  ZapIcon,
} from "lucide-react"
import { useEffect, useMemo, useRef, useState } from "react"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import { cn } from "@/lib/utils"
import { ProviderGlyph } from "@/mock/components/bits"
import {
  provider as findProvider,
  type ProviderId,
  type Resource,
  resources,
  type Service,
  services,
} from "@/mock/data"

export type GraphSelection =
  { kind: "service"; id: string } | { kind: "resource"; id: string }

type GNode = {
  id: string
  kind: "domain" | "service" | "resource"
  col: number
  lane: ProviderId
  x: number
  y: number
  w: number
  h: number
  service?: Service
  resource?: Resource
  domain?: string
}

type GEdge = { from: string; to: string; tentative: boolean }

const laneOrder: ProviderId[] = ["vercel", "aws", "gcp", "render"]
// Columns stretch to fill the card; these are the floors.
const minColW = 230
const maxNodeW = 216
const nodeH = 56
const domainH = 32
const gapY = 12
const padX = 20
const labelH = 40
const padBottom = 18
const columns = ["Entry points", "Frontends", "Services", "Resources"]

export const resourceIcon: Record<Resource["kind"], LucideIcon> = {
  Postgres: DatabaseIcon,
  Redis: ZapIcon,
  Queue: BoxIcon,
  Bucket: BoxIcon,
  Topic: MegaphoneIcon,
}

function layout(projectId: string, colW: number, nodeW: number) {
  const svc = services.filter((s) => s.project === projectId)
  const res = resources.filter((r) => r.project === projectId)
  const draft: Omit<GNode, "x" | "y">[] = []
  for (const s of svc) {
    for (const d of s.domains ?? []) {
      draft.push({
        id: `domain:${d}`,
        kind: "domain",
        col: 0,
        lane: s.provider,
        w: nodeW,
        h: domainH,
        domain: d,
      })
    }
    draft.push({
      id: s.id,
      kind: "service",
      col: s.kind === "frontend" ? 1 : 2,
      lane: s.provider,
      w: nodeW,
      h: nodeH,
      service: s,
    })
  }
  for (const r of res) {
    draft.push({
      id: r.id,
      kind: "resource",
      col: 3,
      lane: r.provider,
      w: nodeW,
      h: nodeH,
      resource: r,
    })
  }

  const lanes = laneOrder.filter((l) => draft.some((n) => n.lane === l))
  const nodes: GNode[] = []
  const bands: { lane: ProviderId; y: number; h: number }[] = []
  let y = 0
  for (const lane of lanes) {
    let tallest = 0
    for (let col = 0; col < columns.length; col++) {
      const stack = draft.filter((n) => n.lane === lane && n.col === col)
      let cy = y + labelH
      for (const n of stack) {
        nodes.push({ ...n, x: padX + col * colW, y: cy })
        cy += n.h + gapY
      }
      tallest = Math.max(tallest, cy - gapY - (y + labelH))
    }
    const h = labelH + tallest + padBottom
    bands.push({ lane, y, h })
    y += h
  }

  const edges: GEdge[] = []
  for (const s of svc) {
    for (const d of s.domains ?? []) {
      edges.push({ from: `domain:${d}`, to: s.id, tentative: false })
    }
    for (const dep of s.dependsOn) {
      const target = res.find((r) => r.id === dep)
      if (!nodes.some((n) => n.id === dep)) continue
      edges.push({
        from: s.id,
        to: dep,
        tentative: target?.status === "unconfirmed",
      })
    }
  }
  return {
    nodes,
    edges,
    bands,
    width: padX * 2 + (columns.length - 1) * colW + nodeW,
    height: y,
  }
}

function edgePath(a: GNode, b: GNode) {
  const x1 = a.x + a.w
  const y1 = a.y + a.h / 2
  const x2 = b.x
  const y2 = b.y + b.h / 2
  if (x2 > x1) {
    const dx = Math.max((x2 - x1) / 2, 24)
    return `M${x1} ${y1}C${x1 + dx} ${y1} ${x2 - dx} ${y2} ${x2} ${y2}`
  }
  // Same column: loop out to the right and back in.
  const r = 28
  return `M${x1} ${y1}C${x1 + r} ${y1} ${b.x + b.w + r} ${y2} ${b.x + b.w} ${y2}`
}

export function ServiceGraph({
  projectId,
  selected,
  onSelect,
}: {
  projectId: string
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
  const colW = Math.max(
    minColW,
    Math.floor((frameW - padX * 2 - maxNodeW) / (columns.length - 1))
  )
  const nodeW = Math.min(maxNodeW, colW - 48)
  const g = useMemo(
    () => layout(projectId, colW, nodeW),
    [projectId, colW, nodeW]
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
              key={c}
              className="py-2.5"
              style={{
                width: i === columns.length - 1 ? nodeW + padX : colW,
                paddingLeft: i === 0 ? padX : 0,
              }}
            >
              {c}
            </span>
          ))}
        </div>
        <div className="canvas-dots relative" style={{ height: g.height }}>
          {g.bands.map((b, i) => {
            const p = findProvider(b.lane)
            return (
              <div
                key={b.lane}
                className={cn(
                  "absolute inset-x-0 flex items-start gap-2 px-5 pt-3 text-xs",
                  i > 0 && "border-t border-dashed",
                  i % 2 === 1 && "bg-muted/30"
                )}
                style={{ top: b.y, height: b.h }}
              >
                <ProviderGlyph id={b.lane} />
                <span className="font-medium">{p.name}</span>
                <span className="text-muted-foreground">
                  {p.regions.join(", ")}
                </span>
              </div>
            )
          })}
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
              const on =
                related?.has(e.from) &&
                related.has(e.to) &&
                focus !== null &&
                (e.from === focus || e.to === focus)
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
    "absolute flex h-auto items-center justify-start gap-2.5 rounded-xl border bg-background px-3 text-left text-sm font-normal whitespace-normal shadow-[0_1px_2px_0_rgb(0_0_0/0.05),0_4px_12px_-6px_rgb(0_0_0/0.08)] transition-[opacity,box-shadow,border-color] duration-150 outline-none hover:bg-background focus-visible:ring-3 focus-visible:ring-ring/50",
    dim && "opacity-45",
    active && "border-foreground/60 ring-3 ring-foreground/10"
  )
  if (n.kind === "domain") {
    return (
      <Badge
        variant="outline"
        style={style}
        className={cn(
          "absolute justify-start gap-2 rounded-full border-dashed bg-card px-3 font-mono text-xs font-normal transition-opacity",
          dim && "opacity-45"
        )}
        onMouseEnter={() => onHover(n.id)}
        onMouseLeave={() => onHover(null)}
      >
        <GlobeIcon className="size-3.5! text-muted-foreground" />
        <span className="truncate">{n.domain}</span>
      </Badge>
    )
  }
  const hoverProps = {
    onMouseEnter: () => onHover(n.id),
    onMouseLeave: () => onHover(null),
    onFocus: () => onHover(n.id),
    onBlur: () => onHover(null),
  }
  if (n.kind === "resource" && n.resource) {
    const r = n.resource
    const Icon = resourceIcon[r.kind]
    const unconfirmed = r.status === "unconfirmed"
    return (
      <Button
        variant="outline"
        style={style}
        className={cn(
          common,
          "hover:border-foreground/30",
          unconfirmed && "border-dashed border-weld bg-weld/5 shadow-none"
        )}
        onClick={() => onSelect({ kind: "resource", id: r.id })}
        {...hoverProps}
      >
        <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted">
          <Icon className="size-3.5 text-muted-foreground" />
        </span>
        <span className="grid min-w-0 flex-1 leading-tight">
          <span className="truncate font-mono text-[13px] font-medium">
            {r.name}
          </span>
          <span className="truncate text-xs text-muted-foreground">
            {unconfirmed
              ? "Unconfirmed, needs review"
              : r.status === "provisioning"
                ? "Provisioning"
                : r.engine}
          </span>
        </span>
        {r.status === "provisioning" && (
          <span className="size-1.5 shrink-0 animate-pulse rounded-full bg-weld motion-reduce:animate-none" />
        )}
      </Button>
    )
  }
  const s = n.service as Service
  return (
    <Button
      variant="outline"
      style={style}
      className={cn(common, "hover:border-foreground/30")}
      onClick={() => onSelect({ kind: "service", id: s.id })}
      {...hoverProps}
    >
      <span className="grid min-w-0 flex-1 leading-tight">
        <span className="flex items-center gap-1.5">
          <span className="truncate font-medium">{s.name}</span>
          <span
            className={cn(
              "size-1.5 shrink-0 rounded-full",
              s.health === "healthy" && "bg-overdye",
              s.health === "degraded" && "bg-weld",
              s.health === "deploying" &&
                "animate-pulse bg-weld motion-reduce:animate-none"
            )}
          />
        </span>
        <span className="truncate text-xs text-muted-foreground">
          {s.framework}, {s.runtime}
        </span>
      </span>
      <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-[10px] font-medium text-muted-foreground">
        {s.language === "TypeScript"
          ? "TS"
          : s.language === "JavaScript"
            ? "JS"
            : s.language}
      </span>
    </Button>
  )
}
