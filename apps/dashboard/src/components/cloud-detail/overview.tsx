// A Cloud at a glance: anything stuck, where services go, which environments
// use it, and what changed last.
import type { UseQueryResult } from "@forge-go/client-react"
import { TriangleAlertIcon } from "lucide-react"
import type { ReactNode } from "react"

import type {
  Connection,
  Pool,
  Project,
  ProjectGrant,
  Server,
  Sync,
  Target,
  Usage,
} from "@/client/forge/loomplane/types"
import {
  CloudSectionHead,
  Row,
  Rows,
} from "@/components/cloud-targets/infrastructure-ui"
import { toneClass } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { cloudLabel } from "@/lib/clouds"
import { formatDate, relative } from "@/lib/format"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

import { changeTitle, syncState } from "./changes"
import {
  DEFAULT_KEY,
  serviceCount,
  servicesPlaced,
  type CloudScope,
} from "./data"
import { EnvironmentsOnCloud } from "./environments"
import { RunsOn } from "./targets"

export type CloudTab =
  | "overview"
  | "targets"
  | "projects"
  | "servers"
  | "capacity"
  | "changes"
  | "settings"

type Problem = {
  key: string
  title: string
  body: string
  action: ReactNode
}

export function Overview({
  scope,
  owner,
  targets,
  connections,
  pools,
  servers,
  usage,
  syncs,
  grants,
  projects,
  onTab,
  onChanged,
}: {
  scope: CloudScope
  owner: string
  targets: Target[]
  connections: Connection[]
  pools?: Pool[]
  servers?: Server[]
  usage: UseQueryResult<Usage>
  syncs: Sync[]
  grants?: ProjectGrant[]
  projects: Project[]
  onTab: (tab: CloudTab) => void
  onChanged: () => void
}) {
  const { cloud, project } = scope
  const now = useNow()
  const placed = servicesPlaced(usage.data)
  const fallback = cloud.defaultPlacement
  // A project limited to chosen targets never reaches the default.
  const showDefault = !!fallback && (!project || placed.has(DEFAULT_KEY))
  const open = (tab: CloudTab, label: string) => (
    <Button size="sm" variant="outline" onClick={() => onTab(tab)}>
      {label}
    </Button>
  )
  const problems: Problem[] = []
  for (const s of syncs) {
    const { title } = changeTitle(s, cloud.id, targets, connections)
    if (s.status === "failed")
      problems.push({
        key: s.id,
        title: `${title} failed`,
        body:
          s.error ||
          "Environments still serve their earlier release. Retry it or review a revert.",
        action: open("changes", "Open changes"),
      })
    for (const env of s.environments ?? [])
      if (
        s.status === "routing_pending" &&
        env.externalRouting &&
        !env.routingEvidence
      )
        problems.push({
          key: `${s.id}:${env.environmentId}`,
          title: `${title}: waiting on routing in ${env.environment}`,
          body: "The new release is live. Point traffic at it, then confirm, and the old placement is cleaned up.",
          action: open("changes", "Confirm routing"),
        })
  }
  const used = new Set([
    ...targets.filter((t) => t.runtime !== "server").map((t) => t.connectionId),
    ...(fallback ? [fallback.connectionId] : []),
  ])
  for (const c of connections)
    if (used.has(c.id) && c.status !== "ready")
      problems.push({
        key: c.id,
        title: `${c.name} needs attention`,
        body:
          c.message ||
          "Loomplane can't use this account right now, so nothing new can be placed on it.",
        action: (
          <Button
            size="sm"
            variant="outline"
            nativeButton={false}
            render={<Link to="/clouds/accounts/$id" params={{ id: c.id }} />}
          >
            Open account
          </Button>
        ),
      })
  for (const s of servers ?? [])
    if (s.draining || !["ready", "pending", "provisioning"].includes(s.status))
      problems.push({
        key: s.id,
        title: s.draining ? `${s.name} is draining` : `${s.name} isn't ready`,
        body: s.draining
          ? "Move what still runs on it before you remove it."
          : "Services can't be placed on it until it passes a health check.",
        action: open("servers", "Open infrastructure"),
      })
  if (!project && targets.length === 0 && !fallback)
    problems.push({
      key: "nowhere",
      title: "Services here have nowhere to run",
      body: "Set a default or add a target before an environment uses this Cloud.",
      action: open("targets", "Open targets"),
    })

  const running = [...placed.values()].reduce((a, b) => a + b, 0)
  const accounts = connections.filter((c) => used.has(c.id))
  const facts: [string, ReactNode][] = [
    ["Owner", owner],
    ...(grants
      ? [["Projects with access", String(grants.length)] as [string, ReactNode]]
      : []),
    ["Running services", usage.data ? String(running) : "…"],
    [
      "Accounts",
      accounts.length
        ? `${accounts.length}, on ${[...new Set(accounts.map((a) => cloudLabel(a.cloud)))].join(" and ")}`
        : "None yet",
    ],
    ["Created", formatDate(cloud.createdAt)],
  ]

  return (
    <div className="grid gap-6">
      {problems.length > 0 && (
        <section aria-label="Needs attention" className="grid gap-2">
          {problems.map((p) => (
            <div
              key={p.key}
              className="flex flex-wrap items-center gap-x-4 gap-y-2 rounded-xl bg-weld-tint/60 px-4 py-3 ring-1 ring-weld/30"
            >
              <TriangleAlertIcon
                aria-hidden="true"
                className="size-4 shrink-0 text-weld-ink"
              />
              <span className="grid min-w-0 flex-[1_1_360px] gap-0.5">
                <span className="text-[13px] font-semibold">{p.title}</span>
                <span className="text-[12.5px] leading-relaxed text-muted-foreground">
                  {p.body}
                </span>
              </span>
              {p.action}
            </div>
          ))}
        </section>
      )}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <section
          aria-label="Where services go"
          className="grid content-start gap-3"
        >
          <CloudSectionHead
            title="Where services go"
            description="A service names a target, or tags that pick one. Otherwise it uses the default."
          />
          {targets.length === 0 && !showDefault ? (
            <p className="rounded-xl px-4 py-3 text-[13px] text-muted-foreground ring-1 ring-border">
              {project
                ? "This project may not place anything on this Cloud yet."
                : "No targets and no default yet."}{" "}
              {!project && (
                <Button
                  variant="link"
                  className="h-auto p-0"
                  onClick={() => onTab("targets")}
                >
                  Add one
                </Button>
              )}
            </p>
          ) : (
            <Rows>
              {targets.map((t) => (
                <PlacementRow
                  key={t.id}
                  label={t.displayName || t.name}
                  slug={t.name}
                  count={usage.data ? (placed.get(t.id) ?? 0) : undefined}
                >
                  <RunsOn target={t} connections={connections} pools={pools} />
                </PlacementRow>
              ))}
              {showDefault && fallback && (
                <PlacementRow
                  label="Default"
                  slug="services with no target"
                  count={
                    usage.data ? (placed.get(DEFAULT_KEY) ?? 0) : undefined
                  }
                >
                  <RunsOn
                    target={{ ...fallback, runtime: "managed" }}
                    connections={connections}
                  />
                </PlacementRow>
              )}
            </Rows>
          )}
        </section>

        <section
          aria-label="About this Cloud"
          className="grid content-start gap-3"
        >
          <h2 className="text-[15px] font-semibold">About</h2>
          <dl className="rounded-xl px-4 py-1 ring-1 ring-border">
            {facts.map(([k, v]) => (
              <div
                key={k}
                className="flex justify-between gap-3 border-b py-2.5 last:border-b-0"
              >
                <dt className="text-[12.5px] text-muted-foreground">{k}</dt>
                <dd className="text-right text-[13px]">{v}</dd>
              </div>
            ))}
          </dl>
        </section>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <EnvironmentsOnCloud
          scope={scope}
          usage={usage}
          syncs={syncs}
          grants={grants}
          projects={projects}
          onChanged={onChanged}
        />
        <section
          aria-label="Latest changes"
          className="grid content-start gap-3"
        >
          <CloudSectionHead title="Latest changes">
            <Button size="sm" variant="ghost" onClick={() => onTab("changes")}>
              All changes
            </Button>
          </CloudSectionHead>
          {syncs.length === 0 ? (
            <p className="rounded-xl px-4 py-3 text-[13px] text-muted-foreground ring-1 ring-border">
              Nothing has changed on this Cloud yet.
            </p>
          ) : (
            <Rows>
              {syncs.slice(0, 3).map((s) => {
                const state = syncState(s)
                return (
                  <Row key={s.id} className="items-start justify-start gap-2.5">
                    <span
                      aria-hidden="true"
                      className={cn(
                        "mt-1.5 size-1.5 shrink-0 rounded-full",
                        toneClass[state.tone].dot
                      )}
                    />
                    <span className="grid min-w-0 gap-0.5">
                      <span className="text-[13px] font-medium">
                        {changeTitle(s, cloud.id, targets, connections).title}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {state.label} · {relative(Date.parse(s.createdAt), now)}
                      </span>
                    </span>
                  </Row>
                )
              })}
            </Rows>
          )}
        </section>
      </div>
    </div>
  )
}

function PlacementRow({
  label,
  slug,
  count,
  children,
}: {
  label: string
  slug: string
  count?: number
  children: ReactNode
}) {
  return (
    <Row className="flex-wrap gap-y-2">
      <span className="grid min-w-0 flex-[1_1_150px] gap-0.5">
        <span className="truncate text-[13px] font-semibold">{label}</span>
        <span className="truncate font-mono text-[11.5px] text-muted-foreground">
          {slug}
        </span>
      </span>
      <span className="min-w-0 flex-[2_1_240px]">{children}</span>
      <span className="w-24 text-right text-[12.5px] text-muted-foreground">
        {count === undefined ? "…" : serviceCount(count)}
      </span>
    </Row>
  )
}
