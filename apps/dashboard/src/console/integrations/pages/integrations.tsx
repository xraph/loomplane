import { Button } from "@forge-go/dashboard-kit/components/button"
import { FilterBar } from "@forge-go/dashboard-kit/components/filter-bar"
import { NoneCell } from "@forge-go/dashboard-kit/components/none-cell"
import { PageHeader } from "@forge-go/dashboard-kit/components/page-header"
import { QueryBoundary } from "@forge-go/dashboard-kit/components/query-boundary"
import {
  type Column,
  ResourceTable,
} from "@forge-go/dashboard-kit/components/resource-table"
import { Section } from "@forge-go/dashboard-kit/components/section"
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@forge-go/dashboard-kit/components/sheet"
import { StatGrid } from "@forge-go/dashboard-kit/components/stat-grid"
import { Timestamp } from "@forge-go/dashboard-kit/components/timestamp"
import { PluginLink, useNavigateTo, useQuery } from "@forge-go/dashboard-plugin"
import { useMemo, useState } from "react"

import {
  ConnectionSummary,
  SetupBadge,
  SourceBadge,
  setupHelp,
  setupLabel,
} from "../components"
import { AppSetup } from "../setup-panel"
import {
  type ConsoleIntegration,
  type IntegrationDetail,
  type IntegrationsResponse,
  type SetupState,
  SETUP_ORDER,
  setupState,
} from "../types"

type StatusFilter = "all" | "todo" | SetupState
type KindFilter = "all" | ConsoleIntegration["kind"]

function matches(it: ConsoleIntegration, q: string): boolean {
  if (!q) {
    return true
  }
  return [it.name, it.key, it.provider.name, it.clientId ?? ""]
    .join(" ")
    .toLowerCase()
    .includes(q)
}

function matchesStatus(it: ConsoleIntegration, status: StatusFilter): boolean {
  const state = setupState(it)
  if (status === "all") {
    return true
  }
  if (status === "todo") {
    return state === "refused" || state === "broken"
  }
  return state === status
}

export function IntegrationsPage() {
  const list = useQuery<IntegrationsResponse>("oauth.integrations")
  const [search, setSearch] = useState("")
  const [status, setStatus] = useState<StatusFilter>("all")
  const [kind, setKind] = useState<KindFilter>("all")
  const [open, setOpen] = useState<string | null>(null)

  // Worst first; the backend's own order (clouds, then repository hosts)
  // breaks ties.
  const sorted = useMemo(() => {
    const all = list.data?.integrations ?? []
    const rank = (it: ConsoleIntegration) => SETUP_ORDER.indexOf(setupState(it))
    return all
      .map((it, i) => ({ it, i }))
      .sort((a, b) => rank(a.it) - rank(b.it) || a.i - b.i)
      .map(({ it }) => it)
  }, [list.data])

  const q = search.trim().toLowerCase()
  const rows = sorted.filter(
    (it) =>
      matches(it, q) &&
      matchesStatus(it, status) &&
      (kind === "all" || it.kind === kind)
  )
  const filtered = q !== "" || status !== "all" || kind !== "all"

  const columns: Column<ConsoleIntegration>[] = [
    {
      id: "integration",
      header: "Integration",
      cell: (it) => (
        <span className="flex min-w-0 flex-col items-start">
          <button
            className="font-medium underline-offset-4 hover:underline"
            onClick={() => setOpen(it.key)}
            type="button"
          >
            {it.name}
          </button>
          <span className="text-xs text-muted-foreground">
            {it.kind === "cloud" ? "Cloud sign-in" : "Repository host"}
            {it.provider.name !== it.name ? ` · ${it.provider.name}` : ""}
          </span>
        </span>
      ),
    },
    {
      id: "status",
      header: "Status",
      cell: (it) => (
        <span className="flex flex-col items-start gap-1" title={setupHelp(it)}>
          <SetupBadge integration={it} />
          {it.problem && (
            <span className="line-clamp-2 max-w-[16rem] text-xs text-muted-foreground">
              {it.problem}
            </span>
          )}
        </span>
      ),
    },
    {
      id: "app",
      header: "App",
      cell: (it) =>
        it.clientId ? (
          <span className="flex min-w-0 flex-col items-start gap-1">
            <span className="max-w-[14rem] truncate font-mono text-xs">
              {it.clientId}
            </span>
            <SourceBadge source={it.source} />
          </span>
        ) : (
          <NoneCell label="app" />
        ),
    },
    {
      id: "connections",
      header: "Signed-in connections",
      cell: (it) => <ConnectionSummary integration={it} />,
    },
    {
      id: "updated",
      header: "Updated",
      cell: (it) => <Timestamp label="change here" value={it.updatedAt} />,
    },
  ]

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <PageHeader
        description="The OAuth apps people sign in to clouds and repository hosts with. An app saved here is live at once, with no restart; one set in configuration wins over it."
        title="OAuth integrations"
      />

      <QueryBoundary
        keepPreviousData
        query={list}
        skeletonRows={6}
        title="OAuth integrations"
      >
        {(d) => {
          const clouds = d.integrations.filter((it) => it.kind === "cloud")
          const t = d.totals
          const lapsed = t.signInNeeded + t.appChanged
          return (
            <>
              <StatGrid
                items={[
                  {
                    label: "Clouds with sign-in",
                    value: `${clouds.filter((it) => it.source !== "none").length} of ${clouds.length}`,
                    hint: "People can connect them by signing in",
                    tone: "success",
                    href: "ready",
                  },
                  {
                    label: "Apps refused",
                    value: d.integrations.filter((it) => it.problem).length,
                    hint: "The cloud refused the saved app",
                    tone: d.integrations.some((it) => it.problem)
                      ? "danger"
                      : "default",
                    href: "refused",
                  },
                  {
                    label: "Need signing in again",
                    value: d.healthAvailable ? lapsed : "–",
                    hint: "Grant refused, or its app replaced",
                    tone: lapsed > 0 ? "danger" : "default",
                    href: "@connections",
                  },
                  {
                    label: "Failing",
                    value: d.healthAvailable ? t.failed : "–",
                    hint: "Their last check failed",
                    tone: t.failed > 0 ? "warning" : "default",
                    href: "@connections",
                  },
                ]}
                renderLink={(href, cell) =>
                  href === "@connections" ? (
                    <PluginLink to="/connections">{cell}</PluginLink>
                  ) : (
                    <button
                      className="text-left"
                      onClick={() => setStatus(href as StatusFilter)}
                      type="button"
                    >
                      {cell}
                    </button>
                  )
                }
              />

              <Section
                actions={
                  <FilterBar
                    filters={[
                      {
                        id: "status",
                        label: "Status",
                        value: status,
                        onChange: (v) => setStatus(v as StatusFilter),
                        options: [
                          { label: "All", value: "all" },
                          { label: "Needs attention", value: "todo" },
                          ...SETUP_ORDER.map((s) => ({
                            label: setupLabel(s),
                            value: s,
                          })),
                        ],
                      },
                      {
                        id: "kind",
                        label: "Kind",
                        value: kind,
                        onChange: (v) => setKind(v as KindFilter),
                        options: [
                          { label: "Any", value: "all" },
                          { label: "Cloud sign-in", value: "cloud" },
                          { label: "Repository hosts", value: "repository" },
                        ],
                      },
                    ]}
                    search={{
                      value: search,
                      onChange: setSearch,
                      placeholder: "Search integrations or client IDs",
                      label: "Search integrations",
                    }}
                  />
                }
                aria-label="OAuth integrations"
              >
                <ResourceTable<ConsoleIntegration>
                  caption={`${rows.length} ${rows.length === 1 ? "integration" : "integrations"}`}
                  columns={columns}
                  emptyMessage={
                    filtered
                      ? "No integrations match these filters."
                      : "Nothing here signs in through OAuth."
                  }
                  rowActions={(it) => (
                    <Button
                      onClick={() => setOpen(it.key)}
                      size="sm"
                      variant={
                        it.editable && it.source === "none"
                          ? "outline"
                          : "ghost"
                      }
                    >
                      {!it.editable
                        ? "View"
                        : it.source === "none"
                          ? "Set up"
                          : "Edit"}
                    </Button>
                  )}
                  rowKey={(it) => it.key}
                  rows={rows}
                />
              </Section>
            </>
          )
        }}
      </QueryBoundary>

      <QuickSetupSheet integrationKey={open} onClose={() => setOpen(null)} />
    </section>
  )
}

/** One integration's setup, beside the list, so the list stays put. */
function QuickSetupSheet({
  integrationKey,
  onClose,
}: {
  integrationKey: string | null
  onClose: () => void
}) {
  const detail = useQuery<IntegrationDetail>(
    "oauth.integration",
    { key: integrationKey ?? "" },
    { enabled: integrationKey !== null }
  )
  const navigate = useNavigateTo()
  const it = detail.data?.key === integrationKey ? detail.data : undefined

  return (
    <Sheet onOpenChange={(o) => !o && onClose()} open={integrationKey !== null}>
      <SheetContent className="w-full overflow-y-auto data-[side=right]:sm:max-w-2xl">
        <SheetHeader className="border-b pr-12">
          <SheetTitle>{it ? it.name : "Loading…"}</SheetTitle>
          <SheetDescription>
            {it ? `Registered with ${it.provider.name}.` : " "}
          </SheetDescription>
          {it && (
            <span className="flex items-center gap-2 text-sm">
              <SetupBadge integration={it} />
              <button
                className="underline underline-offset-4"
                onClick={() => {
                  onClose()
                  navigate(`/integrations/${encodeURIComponent(it.key)}`)
                }}
                type="button"
              >
                Open the full page
              </button>
            </span>
          )}
        </SheetHeader>
        <div className="px-4 pb-6">
          <QueryBoundary
            keepPreviousData
            query={detail}
            skeletonRows={8}
            title="OAuth app"
          >
            {(d) =>
              d.key === integrationKey ? (
                <AppSetup integration={d} key={d.key} />
              ) : null
            }
          </QueryBoundary>
        </div>
      </SheetContent>
    </Sheet>
  )
}
