// The clouds Loomplane deploys to and everything it runs there, across
// every project: one list, whichever cloud or engine owns the lifecycle.
import {
  BoxIcon,
  CpuIcon,
  DatabaseIcon,
  EllipsisIcon,
  PlusIcon,
  SearchIcon,
  ServerIcon,
  WalletIcon,
} from "lucide-react"
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
import { Page, PageHeading, SectionHeading } from "@/components/page"
import { EnvBadge, segmented } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
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
import { cn } from "@/lib/utils"

const connectionStatus: Record<string, { label: string; tone: string }> = {
  ready: { label: "Ready", tone: "bg-overdye" },
  incomplete: { label: "Missing permissions", tone: "bg-weld" },
  failed: { label: "Can't connect", tone: "bg-destructive" },
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
        <PageHeading
          title="Infrastructure"
          description="Connected servers, Kubernetes clusters and cloud accounts, with the services running on each."
          actions={
            <Link
              to="/integrations"
              className={cn(buttonVariants(), "rounded-full")}
            >
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

        <section className="grid gap-4">
          <SectionHeading
            id="clouds"
            title="Deployment infrastructure"
            description="Connected accounts, their health and what runs on each"
          />
          {!connections.data &&
          connections.status === "error" ? null : !connections.data ? (
            <div className="grid gap-2 md:grid-cols-2">
              {[0, 1].map((i) => (
                <Skeleton key={i} className="h-16 rounded-xl" />
              ))}
            </div>
          ) : connections.data.connections.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <ServerIcon />
                </EmptyMedia>
                <EmptyTitle>No infrastructure connected yet</EmptyTitle>
                <EmptyDescription>
                  Connect a server, Kubernetes cluster or managed cloud account,
                  then choose it for each service in your project's deployment
                  targets.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Link
                  to="/integrations"
                  className={cn(buttonVariants(), "rounded-full")}
                >
                  Connect a cloud
                </Link>
              </EmptyContent>
            </Empty>
          ) : (
            <div className="grid gap-2 md:grid-cols-2">
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

        <PersistentStorage environmentNames={envNames} />

        <Card id="spend" className="scroll-mt-16">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <WalletIcon className="size-4 text-muted-foreground" />
              Spend this month
            </CardTitle>
            <CardDescription>
              What each connected account has cost this month, read from the
              cloud&apos;s own billing.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <SpendAccounts />
          </CardContent>
        </Card>

        {infra.data && (
          <section className="grid gap-3">
            <SectionHeading
              title="Engine"
              description="What keeps track of everything above"
            />
            <div className="grid gap-4 md:grid-cols-2">
              <Fact
                icon={<CpuIcon />}
                title="Lifecycle engine"
                value={infra.data.engine}
                note="Starts, stops and replaces what Loomplane deploys, and remembers what it did."
              />
              <Fact
                icon={<DatabaseIcon />}
                title="Engine state"
                value={infra.data.store}
                note="Where the engine records every instance and release."
              />
            </div>
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

function CloudCard({
  connection: c,
  bindings,
}: {
  connection: Connection
  bindings: Binding[]
}) {
  const status = connectionStatus[c.status] ?? {
    label: c.status,
    tone: "bg-muted-foreground/50",
  }
  const services = bindings.filter(
    (binding) => binding.subject === "service"
  ).length
  const resources = bindings.filter(
    (binding) => binding.subject === "resource"
  ).length
  const drifted = bindings.filter((binding) => binding.drift).length
  return (
    <Card size="sm" className="flex-row items-center gap-3 px-3 py-2">
      <CloudGlyph cloud={c.cloud} size="md" />
      <div className="grid min-w-0 flex-1 gap-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Link
            to="/clouds/accounts/$id"
            params={{ id: c.id }}
            className="truncate text-sm font-semibold text-foreground hover:underline"
          >
            {c.name}
          </Link>
          <span className="flex items-center gap-1 text-xs text-muted-foreground">
            <span className={cn("size-1.5 rounded-full", status.tone)} />
            {status.label}
          </span>
        </div>
        <p className="truncate text-xs text-muted-foreground">
          {cloudLabel(c.cloud)} · {c.account} · {services} services ·{" "}
          {resources} resources
        </p>
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
      </div>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`${c.name} actions`}
            />
          }
        >
          <EllipsisIcon />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            render={<Link to="/clouds/accounts/$id" params={{ id: c.id }} />}
          >
            Check, replace or remove it
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </Card>
  )
}

const subjects = [
  { value: "all", label: "Everything" },
  { value: "service", label: "Services" },
  { value: "resource", label: "Resources" },
] as const

function driftLabel(b: Binding): { label: string; tone: string } {
  if (b.drift === "missing") {
    return { label: "Missing", tone: "bg-destructive" }
  }
  if (b.drift) return { label: "Changed outside", tone: "bg-weld" }
  return b.observedAt
    ? { label: "In sync", tone: "bg-overdye" }
    : { label: "Not checked yet", tone: "bg-warp" }
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
    <section id="resources" className="grid scroll-mt-16 gap-3">
      <SectionHeading
        title="Running"
        description="Every service and resource Loomplane deployed, across every project and environment."
      />
      <div className="flex flex-wrap items-center gap-2">
        <InputGroup className="w-full sm:w-64">
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
      <Card className="py-0">
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
                      <span
                        className="inline-flex items-center gap-1.5 text-sm"
                        title={b.driftMessage || undefined}
                      >
                        <span className={cn("size-1.5 rounded-full", d.tone)} />
                        {d.label}
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
  icon,
  title,
  value,
  note,
}: {
  icon: ReactNode
  title: string
  value: string
  note: string
}) {
  return (
    <Card>
      <CardHeader>
        <CardDescription className="flex items-center gap-2 [&_svg]:size-4">
          {icon}
          {title}
        </CardDescription>
        <CardTitle className="text-2xl font-semibold tracking-tight">
          {value}
        </CardTitle>
      </CardHeader>
      <CardContent className="text-sm text-muted-foreground">
        {note}
      </CardContent>
    </Card>
  )
}
