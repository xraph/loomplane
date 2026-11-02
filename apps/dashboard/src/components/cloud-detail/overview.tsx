// A Cloud at a glance: anything stuck, where services go, which environments
// use it, and what changed last.
import type { UseQueryResult } from "@forge-go/client-react"
import { AsteriskIcon, TargetIcon } from "lucide-react"
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
  Cell,
  CloudCallout,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { StatusPill } from "@/components/runs"
import { Button } from "@/components/ui/button"
import { cloudLabel } from "@/lib/clouds"
import { formatDate, relative } from "@/lib/format"
import { Link } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

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
  | "infrastructure"
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
        action: open("infrastructure", "Open servers"),
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
  const facts: { label: string; value: ReactNode }[] = [
    { label: "Owner", value: owner },
    ...(grants
      ? [{ label: "Projects with access", value: String(grants.length) }]
      : []),
    { label: "Running services", value: usage.data ? String(running) : "…" },
    {
      label: "Accounts",
      value: accounts.length
        ? `${accounts.length}, on ${[...new Set(accounts.map((a) => cloudLabel(a.cloud)))].join(" and ")}`
        : "None yet",
    },
    { label: "Created", value: formatDate(cloud.createdAt) },
  ]

  return (
    <div className="grid gap-6">
      {problems.length > 0 && (
        <section aria-label="Needs attention" className="grid gap-2.5">
          {problems.map((p) => (
            <CloudCallout
              key={p.key}
              tone="attention"
              title={p.title}
              action={p.action}
            >
              {p.body}
            </CloudCallout>
          ))}
        </section>
      )}

      <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        <section
          aria-label="Where services go"
          className="grid content-start gap-2.5"
        >
          <CloudSectionHead
            title="Where services go"
            description="A service names a target, or tags that pick one. Otherwise it uses the default."
          />
          {targets.length === 0 && !showDefault ? (
            <CloudEmpty
              kind="list"
              title="No targets or default yet"
              action={!project && open("targets", "Open targets")}
            >
              {project
                ? "This project may not place anything on this Cloud yet."
                : "Add a target or set a default before an environment uses this Cloud."}
            </CloudEmpty>
          ) : (
            <CloudTable
              label="Where services go"
              columns="minmax(0,1fr) minmax(0,1.4fr) 96px"
              head={["Target", "Runs on", "Services"]}
              minWidth={480}
            >
              {targets.map((t) => (
                <CloudTableRow key={t.id}>
                  <NameCell
                    tile={<TargetIcon />}
                    name={t.displayName || t.name}
                    meta={<span className="font-mono">{t.name}</span>}
                  />
                  <Cell>
                    <RunsOn
                      target={t}
                      connections={connections}
                      pools={pools}
                    />
                  </Cell>
                  <Cell className="text-[12.5px] text-muted-foreground">
                    {usage.data ? serviceCount(placed.get(t.id) ?? 0) : "…"}
                  </Cell>
                </CloudTableRow>
              ))}
              {showDefault && fallback && (
                <CloudTableRow>
                  <NameCell
                    tile={<AsteriskIcon />}
                    name="Default"
                    meta="Services with no target"
                  />
                  <Cell>
                    <RunsOn
                      target={{ ...fallback, runtime: "managed" }}
                      connections={connections}
                    />
                  </Cell>
                  <Cell className="text-[12.5px] text-muted-foreground">
                    {usage.data
                      ? serviceCount(placed.get(DEFAULT_KEY) ?? 0)
                      : "…"}
                  </Cell>
                </CloudTableRow>
              )}
            </CloudTable>
          )}
        </section>

        <section
          aria-label="About this Cloud"
          className="grid content-start gap-2.5"
        >
          <CloudSectionHead
            title="About"
            description="Who owns this Cloud and what it runs on."
          />
          <FactList facts={facts} />
        </section>
      </div>

      <div className="grid gap-6 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
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
          className="grid content-start gap-2.5"
        >
          <CloudSectionHead
            title="Latest changes"
            description="The last three changes to this Cloud."
          >
            <Button
              size="sm"
              variant="outline"
              onClick={() => onTab("changes")}
            >
              All changes
            </Button>
          </CloudSectionHead>
          {syncs.length === 0 ? (
            <CloudEmpty kind="list" title="No changes yet">
              Nothing has changed on this Cloud yet.
            </CloudEmpty>
          ) : (
            <CloudTable
              label="Latest changes"
              columns="minmax(0,1fr) 136px"
              head={["Change", "State"]}
              minWidth={300}
            >
              {syncs.slice(0, 3).map((s) => {
                const state = syncState(s)
                return (
                  <CloudTableRow key={s.id}>
                    <NameCell
                      name={
                        changeTitle(s, cloud.id, targets, connections).title
                      }
                      meta={relative(Date.parse(s.createdAt), now)}
                    />
                    <Cell>
                      <StatusPill tone={state.tone}>{state.label}</StatusPill>
                    </Cell>
                  </CloudTableRow>
                )
              })}
            </CloudTable>
          )}
        </section>
      </div>
    </div>
  )
}
