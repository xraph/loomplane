// The service catalog: every database and cache Loomplane can run, each
// opening its own page to deploy it and recover it, above the service
// projects already deployed. Kinds registered with the API after the five
// databases follow them, each opening a page that adds it to a project.
import { ChevronRightIcon, SearchIcon } from "lucide-react"
import { useRef, useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useCatalogKindsList,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type { KindInfo } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import {
  catalogKind,
  groupIcons,
  inCatalog,
  kindIcon,
  targetLabels,
  type CatalogKindModule,
} from "@/lib/catalog-kinds"
import { databaseRecipes, type DatabaseKind } from "@/lib/catalog-recipes"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

type Entry = {
  kind: DatabaseKind
  product?: ProductKey
  group: "database" | "cache" | "data"
  runsOn: string
  blurb: string
  features: string[]
}

const databaseFeatures = ["Retained volume", "Daily backups", "Test restores"]
const cacheFeatures = ["AOF every second", "RDB snapshots", "Up to 128 MiB"]

// What each engine is for, in the catalog's words; the recipe holds the
// title, version and storage profile.
export const catalog: Entry[] = [
  {
    kind: "postgres",
    product: "postgresql",
    group: "database",
    runsOn: "Docker, Kubernetes, RDS or Cloud SQL",
    blurb:
      "The relational database most apps reach for, with a restricted application role and backups you can test-restore.",
    features: databaseFeatures,
  },
  {
    kind: "mysql",
    product: "mysql",
    group: "database",
    runsOn: "Docker",
    blurb:
      "Relational database with a restricted application role and native backups you can test-restore.",
    features: databaseFeatures,
  },
  {
    kind: "mariadb",
    product: "mariadb",
    group: "database",
    runsOn: "Docker",
    blurb:
      "MySQL-compatible relational database, with the same role, volume and backup setup.",
    features: databaseFeatures,
  },
  {
    kind: "redis",
    product: "redis",
    group: "cache",
    runsOn: "Docker",
    blurb:
      "In-memory cache with AOF persistence every second and native RDB snapshots. No failover in this profile.",
    features: cacheFeatures,
  },
  {
    kind: "valkey",
    group: "cache",
    runsOn: "Docker",
    blurb:
      "Open source, Redis-compatible cache with the same persistence and snapshot setup.",
    features: cacheFeatures,
  },
  {
    kind: "nats",
    group: "data",
    runsOn: "Docker",
    blurb:
      "Persistent messaging on one private server, with native snapshots of messages and durable consumers.",
    features: [
      "Retained file store",
      "Messages and consumers",
      "Test restores",
    ],
  },
]

type Group = CatalogKindModule["group"]

const groups: { value: "all" | Group; label: string }[] = [
  { value: "all", label: "All" },
  { value: "database", label: "Databases" },
  { value: "cache", label: "Caches" },
  { value: "gateway", label: "Gateways" },
  { value: "data", label: "Data" },
]

const groupLabels: Record<Group, string> = {
  database: "Database",
  cache: "Cache",
  gateway: "Gateway",
  data: "Data",
}

/** A kind the API registered beyond the five databases. */
type Registered = {
  kind: string
  title: string
  version?: string
  module?: CatalogKindModule
  group?: Group
  runsOn: string
  blurb: string
  features: string[]
}

// Kinds added to an existing project. The five databases keep their own
// cards above, and neither a kind provisioned only as another's
// dependency nor one whose module is hidden shows. Read when rendering:
// kind modules import this page.
function registeredKinds(kinds: KindInfo[]): Registered[] {
  const builtIn = new Set<string>(catalog.map((e) => e.kind))
  return kinds
    .filter(
      (k) =>
        !builtIn.has(k.name) && k.placement === "app-project" && inCatalog(k)
    )
    .map((k) => {
      const module = catalogKind(k.name)
      return {
        kind: k.name,
        title: module?.title ?? k.title,
        version: k.versions[0],
        module,
        group: module?.group,
        runsOn: k.targets.map((t) => targetLabels[t] ?? t).join(" or "),
        blurb: module?.blurb ?? k.description ?? "",
        features: module?.features ?? [],
      }
    })
}

export function Services() {
  const projects = useQuery(useProjectsList)
  const kinds = useQuery(useCatalogKindsList)
  const [group, setGroup] = useState<"all" | Group>("all")
  const [query, setQuery] = useState("")
  const search = useRef<HTMLInputElement>(null)
  const q = query.trim().toLowerCase()
  const searched = catalog.filter(
    (e) =>
      !q ||
      `${databaseRecipes[e.kind].title} ${e.blurb}`.toLowerCase().includes(q)
  )
  const shown = searched.filter((e) => group === "all" || e.group === group)
  const registered = registeredKinds(kinds.data?.kinds ?? [])
  const registeredSearched = registered.filter(
    (e) => !q || `${e.title} ${e.blurb}`.toLowerCase().includes(q)
  )
  const registeredShown = registeredSearched.filter(
    (e) => group === "all" || e.group === group
  )
  // A group shows once something is in it.
  const offered = groups.filter(
    (g) =>
      g.value === "all" ||
      g.value === "database" ||
      g.value === "cache" ||
      catalog.some((e) => e.group === g.value) ||
      registered.some((e) => e.group === g.value)
  )
  const kindsDenied =
    kinds.status === "error" && [401, 403].includes(statusOf(kinds.error))
  // Catalog deploys make their own project with the database icon.
  const yours =
    projects.data?.projects.filter((p) => p.icon === "database") ?? []

  return (
    <>
      <SiteHeader crumbs={[{ label: "Services" }]} />
      <Page>
        <PageHeading
          title="Services"
          description="Choose a service, then review where it runs and how its data is protected."
        />

        <section aria-label="Your services" className="grid gap-2">
          <div className="flex items-baseline justify-between">
            <h2 className="text-[15px] font-semibold">Yours</h2>
            <span className="text-[12.5px] text-muted-foreground">
              {yours.length ? `${yours.length} deployed` : "None yet"}
            </span>
          </div>
          {projects.status === "error" ? (
            <QueryError
              error={projects.error}
              fallback="Projects couldn't be loaded."
              retry={projects.refetch}
            />
          ) : !projects.data ? (
            <Skeleton className="h-16 rounded-xl" />
          ) : yours.length === 0 ? (
            <Empty className="py-8">
              <EmptyHeader>
                <EmptyTitle>No service projects yet</EmptyTitle>
                <EmptyDescription>
                  Deploy a database or cache below, or add a service to an
                  existing project.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => search.current?.focus()}
                >
                  Browse catalog
                </Button>
              </EmptyContent>
            </Empty>
          ) : (
            <div className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
              {yours.map((p) => {
                const s = projectStyle(p)
                return (
                  <Link
                    key={p.id}
                    to="/projects/$id"
                    params={{ id: p.id }}
                    className="group flex items-center gap-3 px-4 py-3 text-foreground transition-colors hover:bg-subtle"
                  >
                    <ProjectIcon icon={s.icon} color={s.color} size="md" />
                    <span className="grid min-w-0 flex-1 gap-0.5">
                      <span className="truncate text-sm font-semibold">
                        {p.name}
                      </span>
                      <span className="truncate text-[12.5px] text-muted-foreground">
                        {p.description || "A service project"}
                      </span>
                    </span>
                    <ChevronRightIcon className="size-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5" />
                  </Link>
                )
              })}
            </div>
          )}
        </section>

        <section aria-label="Catalog" className="grid gap-3">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 className="text-[15px] font-semibold">Catalog</h2>
            <div className="flex flex-wrap items-center gap-2">
              <label className="relative flex items-center">
                <SearchIcon className="pointer-events-none absolute left-3 size-4 text-muted-foreground" />
                <span className="sr-only">Search the catalog</span>
                <input
                  data-page-search
                  ref={search}
                  type="search"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Search the catalog"
                  className="h-[34px] w-56 rounded-[9px] border border-transparent bg-muted pr-3 pl-9 text-[13px] outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/40"
                />
              </label>
              <div
                role="radiogroup"
                aria-label="Kind"
                className="flex gap-0.5 rounded-[10px] bg-muted p-[3px]"
              >
                {offered.map((g) => {
                  const on = g.value === group
                  const n = [...searched, ...registeredSearched].filter(
                    (e) => g.value === "all" || e.group === g.value
                  ).length
                  return (
                    <button
                      key={g.value}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      onClick={() => setGroup(g.value)}
                      className={cn(
                        "flex h-7 items-center gap-1.5 rounded-lg px-2.5 text-[12.5px] font-medium",
                        on
                          ? "bg-background text-foreground shadow-xs"
                          : "text-muted-foreground hover:text-foreground"
                      )}
                    >
                      {g.label}
                      <span className="font-mono text-[11px] text-muted-foreground">
                        {n}
                      </span>
                    </button>
                  )
                })}
              </div>
            </div>
          </div>

          {shown.length === 0 && registeredShown.length === 0 ? (
            <Empty className="py-8">
              <EmptyHeader>
                <EmptyTitle>Nothing in the catalog matches</EmptyTitle>
                <EmptyDescription>
                  Try another name, or pick All.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => {
                    setQuery("")
                    setGroup("all")
                    search.current?.focus()
                  }}
                >
                  Clear filters
                </Button>
              </EmptyContent>
            </Empty>
          ) : (
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {shown.map((e) => {
                const r = databaseRecipes[e.kind]
                return (
                  <article
                    key={e.kind}
                    className="flex flex-col overflow-hidden rounded-xl bg-card ring-1 ring-border transition-shadow hover:shadow-[0_6px_18px_oklch(0.2_0.01_50/0.08)]"
                  >
                    <div className="flex flex-1 flex-col gap-2.5 p-4">
                      <div className="flex items-center gap-3">
                        <ProductIcon
                          product={e.product}
                          fallback={groupIcons[e.group]}
                          label={r.title}
                          size="lg"
                        />
                        <div className="grid min-w-0 flex-1 gap-0.5">
                          <span className="text-[15px] font-semibold tracking-tight">
                            {r.title}{" "}
                            <span className="font-mono text-xs font-normal text-muted-foreground">
                              {r.version}
                            </span>
                          </span>
                          <span className="text-xs text-muted-foreground">
                            {groupLabels[e.group]} · {e.runsOn}
                          </span>
                        </div>
                      </div>
                      <p className="text-[13px] leading-relaxed text-muted-foreground">
                        {e.blurb}
                      </p>
                      <div className="flex flex-wrap gap-1.5">
                        {e.features.map((f) => (
                          <span
                            key={f}
                            className="rounded-md bg-muted px-2 py-0.5 text-[11.5px] text-foreground/80"
                          >
                            {f}
                          </span>
                        ))}
                      </div>
                    </div>
                    <div className="flex items-center justify-between gap-2 border-t bg-subtle px-4 py-2.5">
                      <span className="text-xs text-muted-foreground">
                        Single instance
                      </span>
                      <Button
                        size="sm"
                        nativeButton={false}
                        render={
                          <Link
                            to="/services/$kind"
                            params={{ kind: e.kind }}
                          />
                        }
                      >
                        Deploy {r.title}
                      </Button>
                    </div>
                  </article>
                )
              })}
              {registeredShown.map((e) => (
                <RegisteredCard key={e.kind} entry={e} />
              ))}
              {kinds.status === "pending" && !q && group === "all" && (
                <Skeleton
                  role="status"
                  aria-label="Loading more of the catalog"
                  className="min-h-48 rounded-xl"
                />
              )}
            </div>
          )}
          {kinds.status === "error" &&
            (kindsDenied ? (
              <p className="text-[12.5px] text-muted-foreground">
                Your role can&apos;t read the rest of the catalog, so only the
                databases and caches show.
              </p>
            ) : (
              <QueryError
                error={kinds.error}
                fallback="The rest of the catalog couldn't be loaded."
                retry={kinds.refetch}
              />
            ))}
          <p className="text-xs leading-relaxed text-muted-foreground">
            Each service lists its storage and backup support. Keep supported
            backups off the host.
          </p>
        </section>
      </Page>
    </>
  )
}

/** RegisteredCard is a registered kind's card, opening its add page. */
function RegisteredCard({ entry: e }: { entry: Registered }) {
  return (
    <article className="flex flex-col overflow-hidden rounded-xl bg-card ring-1 ring-border transition-shadow hover:shadow-[0_6px_18px_oklch(0.2_0.01_50/0.08)]">
      <div className="flex flex-1 flex-col gap-2.5 p-4">
        <div className="flex items-center gap-3">
          <ProductIcon
            product={e.module?.logo}
            fallback={kindIcon(e.module)}
            label={e.title}
            size="lg"
          />
          <div className="grid min-w-0 flex-1 gap-0.5">
            <span className="text-[15px] font-semibold tracking-tight">
              {e.title}{" "}
              {e.version && (
                <span className="font-mono text-xs font-normal text-muted-foreground">
                  {e.version}
                </span>
              )}
            </span>
            <span className="text-xs text-muted-foreground">
              {e.group ? groupLabels[e.group] : "Service"} · {e.runsOn}
            </span>
          </div>
        </div>
        {e.blurb && (
          <p className="text-[13px] leading-relaxed text-muted-foreground">
            {e.blurb}
          </p>
        )}
        {e.features.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {e.features.map((f) => (
              <span
                key={f}
                className="rounded-md bg-muted px-2 py-0.5 text-[11.5px] text-foreground/80"
              >
                {f}
              </span>
            ))}
          </div>
        )}
      </div>
      <div className="flex items-center justify-between gap-2 border-t bg-subtle px-4 py-2.5">
        <span className="text-xs text-muted-foreground">
          Added to a project
        </span>
        <Button
          size="sm"
          nativeButton={false}
          render={<Link to="/services/$kind" params={{ kind: e.kind }} />}
        >
          Add {e.title}
        </Button>
      </div>
    </article>
  )
}
