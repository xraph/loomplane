import { FilterBar } from "@forge-go/dashboard-kit/components/filter-bar"
import { NoneCell } from "@forge-go/dashboard-kit/components/none-cell"
import { PageHeader } from "@forge-go/dashboard-kit/components/page-header"
import { QueryBoundary } from "@forge-go/dashboard-kit/components/query-boundary"
import {
  type Column,
  ResourceTable,
} from "@forge-go/dashboard-kit/components/resource-table"
import { Section } from "@forge-go/dashboard-kit/components/section"
import { Timestamp } from "@forge-go/dashboard-kit/components/timestamp"
import {
  PluginLink,
  type PluginPageProps,
  useQuery,
} from "@forge-go/dashboard-plugin"
import { useState } from "react"

import {
  CopyButton,
  HEALTH_HELP,
  HealthBadge,
  healthLabel,
} from "../components"
import {
  type Health,
  HEALTH_ORDER,
  type IntegrationsResponse,
  type SignedInConnection,
  type SignedInPage,
  countOf,
  signInAgainMessage,
} from "../types"

type HealthFilter = "" | "problems" | Health

const PAGE_SIZE = 25

function messagesByOrganization(
  items: SignedInConnection[],
  message: (c: SignedInConnection) => string
): string {
  const groups = new Map<string, string[]>()
  for (const c of items) {
    const org = c.orgName || c.orgId
    groups.set(org, [...(groups.get(org) ?? []), message(c)])
  }
  return [...groups.entries()]
    .map(([org, lines]) => `${org}\n${lines.map((l) => `- ${l}`).join("\n")}`)
    .join("\n\n")
}

function emptyMessage(
  health: HealthFilter,
  search: string,
  filtered: boolean
): string {
  if (health === "problems" && search.trim() === "") {
    return "Nothing needs attention."
  }
  return filtered
    ? "No connections match these filters."
    : "Nobody has connected a cloud by signing in yet."
}

export function ConnectionsPage({ params }: PluginPageProps) {
  // A plugin can't read a query string, so a link from an integration's page
  // names the cloud as a path segment: /connections/cloud/:cloud.
  const [cloud, setCloud] = useState(params.cloud ?? "")
  const [health, setHealth] = useState<HealthFilter>(
    params.cloud ? "" : "problems"
  )
  const [search, setSearch] = useState("")
  const [page, setPage] = useState(1)

  const integrations = useQuery<IntegrationsResponse>("oauth.integrations")
  const list = useQuery<SignedInPage>("connections.list", {
    health: health || undefined,
    cloud: cloud || undefined,
    search: search.trim() || undefined,
    limit: PAGE_SIZE,
    offset: (page - 1) * PAGE_SIZE,
  })
  const counts = list.data?.counts
  const clouds = (integrations.data?.integrations ?? []).filter(
    (it) => it.kind === "cloud"
  )
  const names = new Map(clouds.map((it) => [it.key, it.name] as const))
  const appUrl = integrations.data?.appUrl ?? ""
  const message = (c: SignedInConnection) =>
    signInAgainMessage(c, names.get(c.cloud) ?? c.cloud, appUrl)
  const needsAction = (c: SignedInConnection) => c.health !== "healthy"

  const healthOptions: { label: string; value: HealthFilter }[] = [
    { label: "Needs attention", value: "problems" },
    { label: "All sign-ins", value: "" },
    ...HEALTH_ORDER.map((h) => ({ label: healthLabel(h), value: h })),
  ]

  const columns: Column<SignedInConnection>[] = [
    {
      id: "connection",
      header: "Connection",
      cell: (c) => (
        <span className="flex min-w-0 flex-col">
          <span className="font-medium">{c.name}</span>
          {c.account && (
            <span className="text-xs text-muted-foreground">{c.account}</span>
          )}
        </span>
      ),
    },
    {
      id: "cloud",
      header: "Cloud",
      cell: (c) => (
        <PluginLink
          className="underline-offset-4 hover:underline"
          to={`/integrations/${encodeURIComponent(c.cloud)}`}
        >
          {names.get(c.cloud) ?? c.cloud}
        </PluginLink>
      ),
    },
    {
      id: "organization",
      header: "Organization",
      cell: (c) => <span title={c.orgId}>{c.orgName || c.orgId}</span>,
    },
    {
      id: "health",
      header: "Health",
      cell: (c) => (
        <span title={HEALTH_HELP[c.health]}>
          <HealthBadge health={c.health} />
        </span>
      ),
    },
    {
      id: "why",
      header: "Why",
      cell: (c) =>
        c.health === "healthy" ? (
          <NoneCell label="problem" />
        ) : (
          <span className="line-clamp-3 max-w-[18rem] text-sm wrap-break-word">
            {c.message || HEALTH_HELP[c.health]}
          </span>
        ),
    },
    {
      id: "signedInBy",
      header: "Signed in by",
      cell: (c) =>
        c.signedInByEmail || c.signedInBy ? (
          <span className="text-sm">{c.signedInByEmail || c.signedInBy}</span>
        ) : (
          <NoneCell label="person" />
        ),
    },
    {
      id: "checked",
      header: "Last checked",
      cell: (c) => <Timestamp label="check" value={c.checkedAt} />,
    },
  ]

  const filtered = search.trim() !== "" || cloud !== "" || health !== ""
  const actionable = (list.data?.items ?? []).filter(needsAction)

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <PageHeader
        actions={
          actionable.length > 0 && (
            <CopyButton
              label="Copy messages for every connection shown, by organization"
              text={messagesByOrganization(actionable, message)}
            >
              Copy all messages
            </CopyButton>
          )
        }
        description="Every cloud connection made by signing in, in every organization, worst first. Only someone in the organization can sign one in again, so each problem has a message you can copy and send them."
        title="Sign-in health"
      />

      <Section
        actions={
          <FilterBar
            filters={[
              {
                id: "health",
                label: "Health",
                value: health,
                onChange: (v) => {
                  setHealth(v as HealthFilter)
                  setPage(1)
                },
                options: healthOptions.map((o) => ({
                  label: counts
                    ? `${o.label} (${countOf(counts, o.value)})`
                    : o.label,
                  value: o.value,
                })),
              },
              {
                id: "cloud",
                label: "Cloud",
                value: cloud,
                onChange: (v) => {
                  setCloud(v)
                  setPage(1)
                },
                options: [
                  { label: "All clouds", value: "" },
                  ...clouds.map((it) => ({ label: it.name, value: it.key })),
                ],
              },
            ]}
            search={{
              value: search,
              onChange: (v) => {
                setSearch(v)
                setPage(1)
              },
              placeholder: "Organization, connection, person or ID",
              label: "Search connections",
            }}
          />
        }
        aria-label="Signed-in connections"
      >
        <QueryBoundary
          keepPreviousData
          query={list}
          skeletonRows={6}
          title="Signed-in connections"
        >
          {(data) => (
            <ResourceTable<SignedInConnection>
              caption={`${data.total} ${data.total === 1 ? "connection" : "connections"}`}
              columns={columns}
              emptyMessage={emptyMessage(health, search, filtered)}
              onPageChange={setPage}
              pagination={{ page, pageSize: data.limit, total: data.total }}
              rowActions={(c) =>
                needsAction(c) ? (
                  <CopyButton
                    label={`Copy a message about ${c.name} for its organization`}
                    text={message(c)}
                  >
                    Copy message
                  </CopyButton>
                ) : null
              }
              rowKey={(c) => c.id}
              rows={data.items}
            />
          )}
        </QueryBoundary>
      </Section>

      <Section title="What the health means">
        <dl className="grid gap-x-4 gap-y-2 p-4 text-sm sm:grid-cols-[auto_1fr]">
          {HEALTH_ORDER.map((h) => (
            <div className="contents" key={h}>
              <dt>
                <HealthBadge health={h} />
              </dt>
              <dd className="text-muted-foreground">{HEALTH_HELP[h]}</dd>
            </div>
          ))}
        </dl>
      </Section>
    </section>
  )
}
