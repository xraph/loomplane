// The service catalog: everything you can add, on a page of its own so it
// can grow apart from what is already deployed. Loomplane runs the
// databases, caches and registered kinds itself, each opening its own page
// to deploy or add it. A connected cloud provider runs the rest: one click
// opens a reviewed creation plan for an offering a checked account has.
import { SearchIcon } from "lucide-react"
import { type ReactNode, useRef, useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useCatalogKindsList,
  useConnectionsList,
  useProvidersList,
} from "@/client/forge/loomplane/hooks"
import type { KindInfo } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { Page, PageHeading } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import {
  ResourceReviewDialog,
  type ResourceOffer,
} from "@/components/provider-resources/review-dialog"
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
import { useOrganization } from "@/lib/organization"
import { managedOffers, type ManagedOffer } from "@/lib/provider-resources"
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
    kind: "falkordb",
    group: "data",
    runsOn: "Docker or Kubernetes",
    blurb:
      "Private graph data with native RDB backups on Docker. Review the deployment plan before it runs.",
    features: ["Retained volume", "Graph snapshots", "Read-only recovery"],
  },
  {
    kind: "opensearch",
    group: "data",
    runsOn: "Docker or Kubernetes",
    blurb:
      "Search with verified TLS. Docker snapshots preserve user indices, mappings and aliases, including hidden user indices.",
    features: ["Verified TLS", "Native snapshots", "Read-only recovery"],
  },
  {
    kind: "elasticsearch",
    group: "data",
    runsOn: "Docker or Kubernetes",
    blurb:
      "Authenticated search with native user-index snapshots on Docker. Global settings and security state stay outside backups.",
    features: ["Private endpoint", "Native snapshots", "Read-only recovery"],
  },
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
    kind: "rabbitmq",
    group: "data",
    runsOn: "Docker",
    blurb:
      "Durable queues with message backups that disconnect clients and briefly stop the broker. Test recovery in an isolated copy.",
    features: ["Retained volume", "Message backups", "Test restores"],
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
// Managed is not a kind of service but who runs it: the provider.
type Filter = "all" | Group | "managed"

const groups: { value: Filter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "database", label: "Databases" },
  { value: "cache", label: "Caches" },
  { value: "gateway", label: "Gateways" },
  { value: "data", label: "Data" },
  { value: "managed", label: "Managed" },
]

const groupLabels: Record<Group, string> = {
  database: "Database",
  cache: "Cache",
  gateway: "Gateway",
  data: "Data",
  // Model kinds are hidden from the catalog; a model's own page names it.
  ai: "AI model",
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

export type CatalogFilter = Filter
export const catalogFilters = groups.map((g) => g.value)

export function ServicesCatalog({
  initialGroup = "all",
}: { initialGroup?: CatalogFilter } = {}) {
  const { can } = useOrganization()
  const kinds = useQuery(useCatalogKindsList)
  const providers = useQuery(useProvidersList)
  const connections = useQuery(useConnectionsList)
  const [group, setGroup] = useState<Filter>(initialGroup)
  const [creating, setCreating] = useState<ResourceOffer>()
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
  const offers = managedOffers(
    providers.data?.providers ?? [],
    connections.data?.connections ?? []
  )
  const offersSearched = offers.filter(
    (o) =>
      !q ||
      `${o.creates} ${o.kind} ${o.providerTitle}`.toLowerCase().includes(q)
  )
  const offersLoaded = !!providers.data && !!connections.data
  const canCreate = can("project:update") && can("deployment:create")
  // A group shows once something is in it. Managed always shows, so a
  // workspace without a checked account learns where they come from.
  const offered = groups.filter(
    (g) =>
      g.value === "all" ||
      g.value === "database" ||
      g.value === "cache" ||
      g.value === "managed" ||
      catalog.some((e) => e.group === g.value) ||
      registered.some((e) => e.group === g.value)
  )
  const kindsDenied =
    kinds.status === "error" && [401, 403].includes(statusOf(kinds.error))
  // Who runs it splits the catalog: Loomplane's own kinds first, then
  // what your providers create. A kind filter shows only Loomplane's.
  const ours = group === "managed" ? [] : [...shown, ...registeredShown]
  const theirs = group === "all" || group === "managed" ? offersSearched : []
  const oursLoading = group !== "managed" && kinds.status === "pending" && !q
  const theirsLoading =
    (group === "all" || group === "managed") && !offersLoaded && !q
  const noAccounts = offersLoaded && offers.length === 0

  return (
    <>
      <SiteHeader
        crumbs={[{ label: "Services", to: "/services" }, { label: "Catalog" }]}
      />
      <Page>
        <PageHeading
          title="Catalog"
          description="Everything you can add. Loomplane runs some services on your own targets; your connected cloud providers create the rest in one click and run them in your account. Either way, you review the plan before anything changes."
        />

        <section aria-label="Catalog" className="grid gap-5">
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
              className="flex flex-wrap gap-0.5 rounded-[10px] bg-muted p-[3px]"
            >
              {offered.map((g) => {
                const on = g.value === group
                const n =
                  g.value === "managed"
                    ? offersSearched.length
                    : [...searched, ...registeredSearched].filter(
                        (e) => g.value === "all" || e.group === g.value
                      ).length + (g.value === "all" ? offersSearched.length : 0)
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

          {group === "managed" && noAccounts ? (
            <Empty className="py-8">
              <EmptyHeader>
                <EmptyTitle>
                  No account can create managed resources yet
                </EmptyTitle>
                <EmptyDescription>
                  Connect and check a cloud account, such as Cloudflare or
                  Vercel. Its provider then creates and runs these resources for
                  you.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button
                  variant="outline"
                  size="sm"
                  nativeButton={false}
                  render={<Link to="/integrations" />}
                >
                  Open Integrations
                </Button>
              </EmptyContent>
            </Empty>
          ) : ours.length === 0 &&
            theirs.length === 0 &&
            !oursLoading &&
            !theirsLoading ? (
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
            <>
              {(ours.length > 0 || oursLoading) && (
                <CatalogGroup
                  title="Run by Loomplane"
                  note="On your own targets, with the storage and backups each lists."
                >
                  {group !== "managed" && (
                    <>
                      {shown.map((e) => (
                        <EngineCard key={e.kind} entry={e} />
                      ))}
                      {registeredShown.map((e) => (
                        <RegisteredCard key={e.kind} entry={e} />
                      ))}
                    </>
                  )}
                  {oursLoading && (
                    <Skeleton
                      role="status"
                      aria-label="Loading more of the catalog"
                      className="min-h-48 rounded-xl"
                    />
                  )}
                </CatalogGroup>
              )}
              {(theirs.length > 0 || theirsLoading) && (
                <CatalogGroup
                  title="One click from your providers"
                  note="Created and run by the provider, in your account."
                >
                  {theirs.map((o) => (
                    <ManagedCard
                      key={`${o.provider}:${o.kind}`}
                      offer={o}
                      canCreate={canCreate}
                      onCreate={() =>
                        setCreating({ kind: o.kind, provider: o.provider })
                      }
                    />
                  ))}
                  {theirsLoading && (
                    <Skeleton
                      role="status"
                      aria-label="Loading provider offerings"
                      className="min-h-48 rounded-xl"
                    />
                  )}
                </CatalogGroup>
              )}
              {group === "all" && noAccounts && !q && (
                <p className="flex flex-wrap items-center justify-between gap-2 rounded-xl px-4 py-3 text-[12.5px] text-muted-foreground ring-1 ring-border">
                  Connect and check a cloud account, such as Cloudflare or
                  Vercel, and what its provider can create shows here.
                  <Button
                    variant="outline"
                    size="sm"
                    nativeButton={false}
                    render={<Link to="/integrations" />}
                  >
                    Open Integrations
                  </Button>
                </p>
              )}
            </>
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
          {(providers.status === "error" || connections.status === "error") && (
            <QueryError
              error={providers.error ?? connections.error}
              fallback="Managed resources couldn't be loaded."
              retry={() =>
                Promise.all([providers.refetch(), connections.refetch()])
              }
            />
          )}
          <p className="text-xs leading-relaxed text-muted-foreground">
            Each service lists its storage and backup support. Keep supported
            backups off the host. Managed resources live in your cloud account,
            so their provider handles storage and backups.
          </p>
        </section>
      </Page>
      {creating && (
        <ResourceReviewDialog
          open
          onOpenChange={(open) => {
            if (!open) setCreating(undefined)
          }}
          offer={creating}
          providers={providers.data?.providers ?? []}
          connections={connections.data?.connections ?? []}
        />
      )}
    </>
  )
}

/** CatalogGroup heads one half of the catalog and lays its cards out. */
function CatalogGroup({
  title,
  note,
  children,
}: {
  title: string
  note: string
  children: ReactNode
}) {
  return (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <h2 className="text-[13px] font-medium text-muted-foreground">
          {title}
        </h2>
        <span className="text-[12.5px] text-muted-foreground">{note}</span>
      </div>
      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">{children}</div>
    </div>
  )
}

/** EngineCard is one of the built-in engines, opening its deploy page. */
function EngineCard({ entry: e }: { entry: Entry }) {
  const r = databaseRecipes[e.kind]
  return (
    <article className="flex flex-col overflow-hidden rounded-xl bg-card ring-1 ring-border transition-shadow hover:shadow-[0_6px_18px_oklch(0.2_0.01_50/0.08)]">
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
        <span className="text-xs text-muted-foreground">Single instance</span>
        <Button
          size="sm"
          nativeButton={false}
          render={<Link to="/services/$kind" params={{ kind: e.kind }} />}
        >
          Deploy {r.title}
        </Button>
      </div>
    </article>
  )
}

/**
 * ManagedCard is an offering a connected provider can create: the provider
 * runs it in your account and Loomplane binds it to your deployments.
 */
function ManagedCard({
  offer: o,
  canCreate,
  onCreate,
}: {
  offer: ManagedOffer
  canCreate: boolean
  onCreate: () => void
}) {
  return (
    <article className="flex flex-col overflow-hidden rounded-xl bg-card ring-1 ring-border transition-shadow hover:shadow-[0_6px_18px_oklch(0.2_0.01_50/0.08)]">
      <div className="flex flex-1 flex-col gap-2.5 p-4">
        <div className="flex items-center gap-3">
          <CloudGlyph cloud={o.cloud} size="lg" />
          <div className="grid min-w-0 flex-1 gap-0.5">
            <span className="text-[15px] font-semibold tracking-tight">
              {o.creates}
            </span>
            <span className="text-xs text-muted-foreground">
              Managed by {o.providerTitle}
            </span>
          </div>
        </div>
        <p className="text-[13px] leading-relaxed text-muted-foreground">
          {o.providerTitle} creates and runs it in your account. Loomplane adds
          it to a project and connects it to your services.
        </p>
        <div className="flex flex-wrap gap-1.5">
          {["Runs in your account", "Reviewed creation plan"].map((f) => (
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
          {o.accounts} {o.accounts === 1 ? "account" : "accounts"} can create
        </span>
        <Button size="sm" disabled={!canCreate} onClick={onCreate}>
          Create with {o.providerTitle}
        </Button>
      </div>
    </article>
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
