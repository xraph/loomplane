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
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
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
import { cloudLabel } from "@/lib/clouds"
import { formatDate, relative } from "@/lib/format"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { useNow, usePeople, useRunning } from "@/lib/use-runs"
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
          description="The clouds Loomplane deploys to, and everything it runs there for you. Ctrl Plane owns the lifecycle; you see one list."
          actions={
            <Link
              to="/integrations"
              className={cn(buttonVariants(), "rounded-full")}
            >
              <PlusIcon />
              Connect a cloud
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
            title="Clouds"
            description="Connected accounts, their health and what runs on each"
          />
          {!connections.data &&
          connections.status === "error" ? null : !connections.data ? (
            <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              {[0, 1].map((i) => (
                <Skeleton key={i} className="h-56 rounded-xl" />
              ))}
            </div>
          ) : connections.data.connections.length === 0 ? (
            <Empty className="border border-dashed">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <ServerIcon />
                </EmptyMedia>
                <EmptyTitle>No clouds connected yet</EmptyTitle>
                <EmptyDescription>
                  Connect Google Cloud, AWS, Vercel or Render, and plans can
                  place services and resources there.
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
            <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
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

        <Card id="spend" className="scroll-mt-16">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <WalletIcon className="size-4 text-muted-foreground" />
              Spend this month
            </CardTitle>
            <CardDescription>
              Spend arrives with billing from your cloud connections: what each
              cloud charges, against a budget you set.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div
              aria-hidden="true"
              className="canvas-dots h-48 w-full rounded-lg opacity-60"
            />
          </CardContent>
        </Card>

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
  const person = usePeople()
  const status = connectionStatus[c.status] ?? {
    label: c.status,
    tone: "bg-muted-foreground/50",
  }
  const services = bindings.filter((b) => b.subject === "service").length
  const resources = bindings.filter((b) => b.subject === "resource").length
  const regions = [...new Set(bindings.map((b) => b.region).filter(Boolean))]
  const drifted = bindings.filter((b) => !!b.drift).length
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <CloudGlyph cloud={c.cloud} className="size-6 text-[10px]" />
          <span className="truncate">{c.name}</span>
        </CardTitle>
        <CardDescription className="flex min-w-0 items-center gap-1.5">
          <span className={cn("size-1.5 shrink-0 rounded-full", status.tone)} />
          <span className="shrink-0">{status.label}</span>
          <span className="truncate font-mono text-xs">{c.account}</span>
        </CardDescription>
        <CardAction>
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
              <DropdownMenuItem render={<Link to="/integrations" />}>
                Check, replace or remove it
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </CardAction>
      </CardHeader>
      <CardContent className="grid gap-4">
        {c.status !== "ready" && (
          <p className="text-weld-ink text-xs">
            {c.message ||
              (c.missing.length
                ? `Missing ${c.missing.length} ${c.missing.length === 1 ? "permission" : "permissions"}`
                : "Needs attention")}
          </p>
        )}
        <div className="grid grid-cols-2 gap-3 text-sm">
          <div className="grid gap-0.5">
            <span className="text-xs text-muted-foreground">Services</span>
            <span className="tabular-nums">{services}</span>
          </div>
          <div className="grid gap-0.5">
            <span className="text-xs text-muted-foreground">Resources</span>
            <span className="tabular-nums">{resources}</span>
          </div>
        </div>
        <div className="grid gap-1.5">
          <div className="flex items-baseline justify-between text-sm">
            <span className="text-muted-foreground">Spend</span>
            <span className="text-xs text-muted-foreground">
              Not reported yet
            </span>
          </div>
          {drifted > 0 && (
            <p className="text-weld-ink text-xs">
              {drifted} {drifted === 1 ? "thing has" : "things have"} drifted
              from what Loomplane deployed
            </p>
          )}
        </div>
        <div className="flex flex-wrap gap-1">
          {regions.length ? (
            regions.map((r) => (
              <Badge key={r} variant="outline" className="font-normal">
                {r}
              </Badge>
            ))
          ) : (
            <span className="text-xs text-muted-foreground">
              Nothing deployed with it yet
            </span>
          )}
        </div>
      </CardContent>
      <CardFooter className="mt-auto text-xs text-muted-foreground">
        {cloudLabel(c.cloud)}, connected by {person(c.createdBy).name} on{" "}
        {formatDate(c.createdAt)}
      </CardFooter>
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
          <Empty className="border-t">
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
