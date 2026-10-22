// The service catalog: every database and cache Loomplane can run, each
// opening its own page to deploy it and recover it, above the service
// projects already deployed.
import { ChevronRightIcon, SearchIcon, ZapIcon } from "lucide-react"
import { useState } from "react"

import { useProjectsList } from "@/client/forge/loomplane/hooks"
import { useQuery } from "@/client/hooks"
import { Page, PageHeading } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import { QueryError } from "@/components/query-error"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { databaseRecipes, type DatabaseKind } from "@/lib/catalog-recipes"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

type Entry = {
  kind: DatabaseKind
  product?: ProductKey
  group: "database" | "cache"
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
]

const groups = [
  { value: "all", label: "All" },
  { value: "database", label: "Databases" },
  { value: "cache", label: "Caches" },
] as const

export function Services() {
  const projects = useQuery(useProjectsList)
  const [group, setGroup] = useState<(typeof groups)[number]["value"]>("all")
  const [query, setQuery] = useState("")
  const q = query.trim().toLowerCase()
  const searched = catalog.filter(
    (e) =>
      !q ||
      `${databaseRecipes[e.kind].title} ${e.blurb}`.toLowerCase().includes(q)
  )
  const shown = searched.filter((e) => group === "all" || e.group === group)
  // Catalog deploys make their own project with the database icon.
  const yours =
    projects.data?.projects.filter((p) => p.icon === "database") ?? []

  return (
    <>
      <SiteHeader crumbs={[{ label: "Services" }]} />
      <Page>
        <PageHeading
          title="Services"
          description="Databases and caches Loomplane runs for you, with persistent storage and native backups. Each one's backups are on its project, under Backups."
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
            <Empty className="py-6">
              <EmptyHeader>
                <EmptyTitle>No service projects yet</EmptyTitle>
                <EmptyDescription>
                  Deploy a database or a cache below to create a project with
                  persistent storage.
                </EmptyDescription>
              </EmptyHeader>
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
                {groups.map((g) => {
                  const on = g.value === group
                  const n = searched.filter(
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

          {shown.length === 0 ? (
            <Empty className="py-6">
              <EmptyHeader>
                <EmptyTitle>Nothing in the catalog matches</EmptyTitle>
                <EmptyDescription>
                  Try another name, or pick All.
                </EmptyDescription>
              </EmptyHeader>
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
                          fallback={ZapIcon}
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
                            {e.group === "database" ? "Database" : "Cache"} ·{" "}
                            {e.runsOn}
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
            </div>
          )}
          <p className="text-xs leading-relaxed text-muted-foreground">
            Every service gets a private endpoint and a volume that outlives the
            container. Point-in-time recovery isn&apos;t available yet, so keep
            backups somewhere other than the host.
          </p>
        </section>
      </Page>
    </>
  )
}
