// The clouds Loomplane deploys to and everything it runs there, across
// every project: one list, whichever cloud or engine owns the lifecycle.
import { BoxIcon, PlusIcon, SearchIcon, ServerIcon } from "lucide-react"
import { type ReactNode, useMemo, useState } from "react"

import { QueryError } from "@/components/query-error"
import { PersistentStorage } from "@/components/persistent-storage"
import {
  useConnectionsList,
  useInfrastructureGet,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { Binding, Connection } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { InfrastructureGpus } from "@/components/gpu-targets"
import { InfrastructureIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { Panel } from "@/components/panel"
import { EnvBadge, segmented, StatusPill, type Tone } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Card } from "@/components/ui/card"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupInput,
} from "@/components/ui/input-group"
import { Kbd } from "@/components/ui/kbd"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { SpendAccounts } from "@/components/usage"
import { cloudLabel } from "@/lib/clouds"
import { relative } from "@/lib/format"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { useNow, useRunning } from "@/lib/use-runs"

const connectionStatus: Record<string, { label: string; tone: Tone }> = {
  ready: { label: "Ready", tone: "live" },
  incomplete: { label: "Missing permissions", tone: "waiting" },
  failed: { label: "Can't connect", tone: "failed" },
}

/** Heading is a section's 15px title, with a line under it or a note aside. */
function Heading({
  title,
  description,
  aside,
  id,
}: {
  title: ReactNode
  description?: ReactNode
  aside?: ReactNode
  id?: string
}) {
  return (
    <div
      id={id}
      className="flex scroll-mt-16 flex-wrap items-baseline justify-between gap-2"
    >
      <div className="grid gap-0.5">
        <h2 className="text-[15px] font-semibold">{title}</h2>
        {description && (
          <p className="text-[12.5px] text-muted-foreground">{description}</p>
        )}
      </div>
      {aside && (
        <span className="text-[12.5px] text-muted-foreground">{aside}</span>
      )}
    </div>
  )
}

export function Infrastructure() {
  const infra = useQuery(useInfrastructureGet)
  const connections = useQuery(useConnectionsList)
  const projects = useQuery(useProjectsList)
  const list = useMemo(() => projects.data?.projects ?? [], [projects.data])
  const { bindings: all, envNames, loaded, error, refetch } = useRunning()

  return (
    <>
      <SiteHeader crumbs={[{ label: "Infrastructure" }]} />
      <Page>
        <p className="text-xs text-muted-foreground">
          Manage Kubernetes cluster setup in{" "}
          <Link to="/clouds" className="underline">
            Clouds
          </Link>
          . Granted project targets appear with their runtime resources below.
        </p>
        <PageHeading
          title="Infrastructure"
          description="Connected servers, Kubernetes clusters and cloud accounts, with the services running on each."
          actions={
            <Link to="/integrations" className={buttonVariants()}>
              <PlusIcon />
              Connect infrastructure
            </Link>
          }
        />

        {(infra.status === "error" || connections.status === "error") && (
          <QueryError
            error={infra.error ?? connections.error}
            fallback="Infrastructure couldn't be loaded."
            retry={() =>
              Promise.allSettled([infra.refetch(), connections.refetch()])
            }
          />
        )}

        <section className="grid gap-2.5">
          <Heading
            id="clouds"
            title="Deployment infrastructure"
            aside={
              connections.data && accountsNote(connections.data.connections)
            }
          />
          {!connections.data &&
          connections.status === "error" ? null : !connections.data ? (
            <div className="grid gap-2.5 sm:grid-cols-[repeat(auto-fill,minmax(260px,1fr))]">
              {[0, 1].map((i) => (
                <Skeleton key={i} className="h-[86px] rounded-[14px]" />
              ))}
            </div>
          ) : connections.data.connections.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <InfrastructureIcon />
                </EmptyMedia>
                <EmptyTitle>No infrastructure connected yet</EmptyTitle>
                <EmptyDescription>
                  Connect a server, Kubernetes cluster or managed cloud account,
                  then choose it for each service in your project's deployment
                  targets.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Link to="/integrations" className={buttonVariants()}>
                  Connect a cloud
                </Link>
              </EmptyContent>
            </Empty>
          ) : (
            <div className="grid gap-2.5 sm:grid-cols-[repeat(auto-fill,minmax(260px,1fr))]">
              {connections.data.connections.map((c) => (
                <CloudCard
                  key={c.id}
                  connection={c}
                  bindings={all.filter((b) => b.connectionId === c.id)}
                />
              ))}
            </div>
          )}
        </section>

        <InfrastructureGpus />

        {error !== undefined && (
          <QueryError
            error={error}
            fallback="Some running services couldn't be loaded."
            retry={refetch}
          />
        )}
        {(error === undefined || all.length > 0) && (
          <Running
            bindings={all}
            loaded={loaded}
            envNames={envNames}
            projects={list}
          />
        )}

        <div className="grid items-start gap-3 lg:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
          <PersistentStorage environmentNames={envNames} />
          <Panel
            id="spend"
            aria-label="Spend this month"
            className="scroll-mt-16 gap-3"
          >
            <div className="grid gap-1.5">
              <h2 className="text-[14px] font-semibold">Spend this month</h2>
              <p className="text-[12.5px] leading-normal text-muted-foreground">
                What each connected account has cost this month, read from the
                cloud&apos;s own billing.
              </p>
            </div>
            <SpendAccounts />
          </Panel>
        </div>

        {infra.data && (
          <section className="grid gap-3">
            <Panel aria-label="Engine" className="gap-0 p-0">
              <div className="grid gap-0.5 border-b px-4 py-3">
                <h2 className="text-[15px] font-semibold">Engine</h2>
                <p className="text-[12.5px] text-muted-foreground">
                  What keeps track of everything above
                </p>
              </div>
              <div className="grid max-md:divide-y md:grid-cols-2 md:divide-x">
                <Fact
                  title="Lifecycle engine"
                  value={infra.data.engine}
                  note="Starts, stops and replaces what Loomplane deploys, and remembers what it did."
                />
                <Fact
                  title="Engine state"
                  value={infra.data.store}
                  note="Where the engine records every instance and release."
                />
              </div>
            </Panel>
            {infra.data.providers.length > 0 && (
              <Card className="py-0">
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead>Provider</TableHead>
                      <TableHead>Region</TableHead>
                      <TableHead>Can run</TableHead>
                      <TableHead className="hidden md:table-cell">
                        Version
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {infra.data.providers.map((p) => (
                      <TableRow key={p.name}>
                        <TableCell className="font-medium">{p.name}</TableCell>
                        <TableCell>{p.region || "Any"}</TableCell>
                        <TableCell>
                          <div className="flex flex-wrap gap-1">
                            {p.capabilities.map((c) => (
                              <Badge
                                key={c}
                                variant="outline"
                                className="font-normal"
                              >
                                {c}
                              </Badge>
                            ))}
                          </div>
                        </TableCell>
                        <TableCell className="hidden font-mono text-xs text-muted-foreground md:table-cell">
                          {p.version}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </Card>
            )}
          </section>
        )}
      </Page>
    </>
  )
}

/** accountsNote counts the connected accounts and says if any need a look. */
function accountsNote(list: Connection[]): string | undefined {
  if (list.length === 0) return undefined
  const ready = list.filter((c) => c.status === "ready").length
  return `${list.length} connected · ${
    ready === list.length
      ? "all ready"
      : `${list.length - ready} ${list.length - ready === 1 ? "needs" : "need"} a look`
  }`
}

function CloudCard({
  connection: c,
  bindings,
}: {
  connection: Connection
  bindings: Binding[]
}) {
  const status = connectionStatus[c.status] ?? {
    label: c.status,
    tone: "quiet" as const,
  }
  const services = bindings.filter(
    (binding) => binding.subject === "service"
  ).length
  const resources = bindings.filter(
    (binding) => binding.subject === "resource"
  ).length
  const drifted = bindings.filter((binding) => binding.drift).length
  return (
    <Link
      to="/clouds/accounts/$id"
      params={{ id: c.id }}
      className="grid min-w-0 content-start gap-2 rounded-[14px] bg-card px-3.5 py-3 text-foreground ring-1 ring-border transition-colors hover:bg-subtle"
    >
      <div className="flex min-w-0 items-center gap-2.5">
        <CloudGlyph cloud={c.cloud} size="md" />
        <span className="grid min-w-0 flex-1 gap-px">
          <span className="truncate text-[13.5px] font-semibold">{c.name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {[cloudLabel(c.cloud), c.account].filter(Boolean).join(" · ")}
          </span>
        </span>
        <StatusPill tone={status.tone}>{status.label}</StatusPill>
      </div>
      <span className="flex flex-wrap gap-x-3.5 text-xs text-muted-foreground">
        <span>
          <span className="font-mono text-foreground tabular-nums">
            {services}
          </span>{" "}
          {services === 1 ? "service" : "services"}
        </span>
        <span>
          <span className="font-mono text-foreground tabular-nums">
            {resources}
          </span>{" "}
          {resources === 1 ? "resource" : "resources"}
        </span>
      </span>
      {c.status !== "ready" && (
        <p className="text-xs text-weld-ink">
          {c.message || "Check the connection under Integrations."}
        </p>
      )}
      {drifted > 0 && (
        <p className="text-xs text-weld-ink">
          {drifted} deployments need review
        </p>
      )}
    </Link>
  )
}

const subjects = [
  { value: "all", label: "Everything" },
  { value: "service", label: "Services" },
  { value: "resource", label: "Resources" },
] as const

function driftLabel(b: Binding): { label: string; tone: Tone } {
  if (b.drift === "missing") {
    return { label: "Missing", tone: "failed" }
  }
  if (b.drift) return { label: "Changed outside", tone: "waiting" }
  return b.observedAt
    ? { label: "In sync", tone: "live" }
    : { label: "Not checked yet", tone: "quiet" }
}

function Running({
  bindings,
  loaded,
  envNames,
  projects,
}: {
  bindings: Binding[]
  loaded: boolean
  envNames: Map<string, string>
  projects: { id: string; name: string; icon: string; color: string }[]
}) {
  const now = useNow(30_000)
  const [query, setQuery] = useState("")
  const [cloud, setCloud] = useState("all")
  const [subject, setSubject] = useState<string>("all")
  const byProject = new Map(projects.map((p) => [p.id, p]))
  const providers = [...new Set(bindings.map((b) => b.provider))].sort()
  const rows = bindings.filter(
    (b) =>
      (cloud === "all" || b.provider === cloud) &&
      (subject === "all" || b.subject === subject) &&
      (!query ||
        `${b.name} ${b.provider} ${b.engine} ${b.region} ${byProject.get(b.projectId)?.name ?? ""}`
          .toLowerCase()
          .includes(query.toLowerCase()))
  )
  return (
    <section id="resources" className="grid scroll-mt-16 gap-2.5">
      <Heading
        title="Running"
        description="Every service and resource Loomplane deployed, across every project and environment."
      />
      <Card className="gap-0 py-0">
        <div className="flex flex-wrap items-center gap-2 border-b p-2.5">
          <InputGroup className="min-w-0 flex-[1_1_240px]">
            <InputGroupAddon>
              <SearchIcon />
            </InputGroupAddon>
            <InputGroupInput
              data-page-search
              placeholder="Search what's running"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              aria-label="Search what's running"
            />
            <InputGroupAddon align="inline-end">
              <Kbd>/</Kbd>
            </InputGroupAddon>
          </InputGroup>
          <ToggleGroup
            variant="outline"
            size="sm"
            spacing={0}
            value={[subject]}
            onValueChange={(v) => v[0] && setSubject(v[0])}
            aria-label="Kind"
          >
            {subjects.map((s) => (
              <ToggleGroupItem
                key={s.value}
                value={s.value}
                className={segmented}
              >
                {s.label}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <Select
            items={[
              { label: "Every provider", value: "all" },
              ...providers.map((p) => ({ label: p, value: p })),
            ]}
            value={cloud}
            onValueChange={(v) => setCloud(String(v))}
          >
            <SelectTrigger size="sm" className="w-44" aria-label="Provider">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">Every provider</SelectItem>
              {providers.map((p) => (
                <SelectItem key={p} value={p}>
                  {p}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        {!loaded && bindings.length === 0 ? (
          <div className="p-4">
            <Skeleton className="h-24 w-full" />
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead>Name</TableHead>
                <TableHead>Project</TableHead>
                <TableHead className="hidden md:table-cell">Runs on</TableHead>
                <TableHead>State</TableHead>
                <TableHead className="hidden text-right lg:table-cell">
                  Updated
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((b) => {
                const p = byProject.get(b.projectId)
                const s = p ? projectStyle(p) : null
                const d = driftLabel(b)
                return (
                  <TableRow key={b.id}>
                    <TableCell>
                      <div className="flex items-center gap-2.5">
                        <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-muted [&_svg]:size-3.5 [&_svg]:text-muted-foreground">
                          {b.subject === "service" ? (
                            <ServerIcon />
                          ) : (
                            <BoxIcon />
                          )}
                        </span>
                        <div className="grid min-w-0">
                          <span className="truncate font-mono text-xs font-medium">
                            {b.name}
                          </span>
                          {b.url ? (
                            <a
                              href={b.url}
                              target="_blank"
                              rel="noreferrer"
                              className="truncate text-xs text-muted-foreground underline-offset-4 hover:underline"
                            >
                              {b.url.replace(/^https?:\/\//, "")}
                            </a>
                          ) : (
                            <span className="text-xs text-muted-foreground">
                              {b.engine}
                            </span>
                          )}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      {p && s ? (
                        <Link
                          to="/projects/$id"
                          params={{ id: p.id }}
                          className="flex items-center gap-2 underline-offset-4 hover:underline"
                        >
                          <ProjectIcon
                            icon={s.icon}
                            color={s.color}
                            size="xs"
                          />
                          <span className="truncate">{p.name}</span>
                          <EnvBadge env={envNames.get(b.environmentId) ?? ""} />
                        </Link>
                      ) : (
                        <span className="text-muted-foreground">Unknown</span>
                      )}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <span className="grid leading-tight">
                        <span>{b.provider}</span>
                        <span className="text-xs text-muted-foreground">
                          {b.region || "Global"}
                        </span>
                      </span>
                    </TableCell>
                    <TableCell>
                      <span title={b.driftMessage || undefined}>
                        <StatusPill tone={d.tone}>{d.label}</StatusPill>
                      </span>
                    </TableCell>
                    <TableCell className="hidden text-right text-xs text-muted-foreground lg:table-cell">
                      {relative(Date.parse(b.updatedAt), now)}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        )}
        {loaded && rows.length === 0 && (
          <Empty variant="inline" className="border-t">
            <EmptyHeader>
              <EmptyTitle>
                {bindings.length === 0
                  ? "Nothing deployed yet"
                  : "Nothing matches"}
              </EmptyTitle>
              <EmptyDescription>
                {bindings.length === 0
                  ? "Approve a plan and its services and resources appear here."
                  : "Clear the filters to see everything running."}
              </EmptyDescription>
            </EmptyHeader>
            <EmptyContent>
              {bindings.length === 0 ? (
                <Link
                  to="/deployments"
                  className={buttonVariants({ variant: "outline", size: "sm" })}
                >
                  Open deployments
                </Link>
              ) : (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setQuery("")
                    setCloud("all")
                    setSubject("all")
                  }}
                >
                  Clear filters
                </Button>
              )}
            </EmptyContent>
          </Empty>
        )}
      </Card>
    </section>
  )
}

function Fact({
  title,
  value,
  note,
}: {
  title: string
  value: string
  note: string
}) {
  return (
    <div className="grid content-start gap-1 px-4 py-3">
      <span className="text-xs text-muted-foreground">{title}</span>
      <span className="font-mono text-[14px] font-medium">{value}</span>
      <span className="text-[12.5px] text-muted-foreground">{note}</span>
    </div>
  )
}
