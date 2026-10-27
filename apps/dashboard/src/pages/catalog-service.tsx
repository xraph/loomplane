// A catalog resource's own page in one environment: what it is made of,
// where its parts come from and what recovery keeps of each, then the
// panels its kind's module adds.
import {
  BoxIcon,
  ChevronLeftIcon,
  DatabaseIcon,
  LockIcon,
  PlayIcon,
  PlugIcon,
} from "lucide-react"
import type { ComponentType, ReactNode } from "react"

import { statusOf } from "@/client/errors"
import {
  useBindingsList,
  useCatalogKindsList,
  useEnvironmentsList,
  useProjectsGet,
} from "@/client/forge/loomplane/hooks"
import type { Binding, Environment } from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { Page } from "@/components/page"
import { ProductIcon } from "@/components/product-icon"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { buttonVariants } from "@/components/ui/button"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import {
  catalogKind,
  componentName,
  dependencyChoices,
  kindIcon,
  parseResourceConfig,
  serviceBindings,
  type CatalogKindModule,
  type CatalogPanel,
  type CatalogPanelProps,
  type ResourceConfigView,
} from "@/lib/catalog-kinds"
import { cloudLabel } from "@/lib/clouds"
import { formatDate } from "@/lib/format"
import { Link } from "@/lib/router"
import {
  bindingConfig,
  isRecord,
  protectionOf,
  type DependencyChoice,
} from "@/lib/service-recovery"

/**
 * CatalogService is the service page of one resource in one environment:
 * Overview, then the panels of the resource's kind. environmentId is an
 * environment's ID; without one the page opens on production.
 */
export function CatalogService({
  projectId,
  resource,
  environmentId,
}: {
  projectId: string
  resource: string
  environmentId: string
}) {
  const project = useQuery(useProjectsGet, { path: { id: projectId } })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const list = environments.data?.environments ?? []
  const environment = environmentId
    ? list.find((e) => e.id === environmentId)
    : (list.find((e) => e.name === "production") ?? list[0])
  const projectName = project.data?.name ?? "Project"
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Projects", to: "/projects" },
          { label: projectName },
          { label: resource },
        ]}
      />
      <Page>
        <Link
          to="/projects/$id"
          params={{ id: projectId }}
          className="-mb-2.5 inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" />
          {projectName}
        </Link>
        {environments.status === "error" ? (
          <Failed
            error={environments.error}
            retry={environments.refetch}
            fallback="The project's environments couldn't be loaded."
          />
        ) : !environments.data ? (
          <Loading resource={resource} />
        ) : list.length === 0 ? (
          <Missing
            projectId={projectId}
            title="This project has no environments yet"
            description="Nothing can run until it has one. The project's page shows its environments once they're set up."
          />
        ) : !environment ? (
          <Missing
            projectId={projectId}
            title="This environment isn't in the project"
            description="It may have been deleted. The project lists the environments it has and what runs in each."
          />
        ) : (
          <InEnvironment
            projectId={projectId}
            resource={resource}
            environment={environment}
          />
        )}
      </Page>
    </>
  )
}

function InEnvironment({
  projectId,
  resource,
  environment,
}: {
  projectId: string
  resource: string
  environment: Environment
}) {
  const bindings = useQuery(useBindingsList, {
    path: { id: projectId },
    query: { environment: environment.name },
  })
  // The API's description of the kind fills in for a kind without a
  // dashboard module. The page works without it.
  const kinds = useQuery(useCatalogKindsList)
  if (bindings.status === "error")
    return (
      <Failed
        error={bindings.error}
        retry={bindings.refetch}
        fallback="What runs in this environment couldn't be loaded."
      />
    )
  if (!bindings.data) return <Loading resource={resource} />
  const all = bindings.data.bindings
  const { binding, components } = serviceBindings(all, resource)
  if (!binding)
    return (
      <Missing
        projectId={projectId}
        title={`Nothing called ${resource} runs in ${environment.name}`}
        description="It may not be deployed there yet, or it was removed. The project lists what runs in each environment."
      />
    )
  const config: ResourceConfigView = parseResourceConfig(binding) ?? {
    kind: binding.engine,
    provider: binding.provider,
  }
  const kind = config.composition?.kind ?? config.kind
  const module = catalogKind(kind)
  const info = kinds.data?.kinds.find((k) => k.name === kind)
  const props: CatalogPanelProps = {
    projectId,
    environmentId: environment.id,
    resource,
    kind,
    binding,
    config,
    components,
  }
  const panels = (module?.panels ?? [])
    .filter((panel) => !panel.when || panel.when(props))
    .map((panel, index) => ({ panel, index }))
    .sort(
      (a, b) => (a.panel.order ?? 0) - (b.panel.order ?? 0) || a.index - b.index
    )
    .map(({ panel }) => panel)
  const preferredPanel = panels.find(
    (panel) => panel.id === module?.defaultPanel
  )
  const defaultTab = preferredPanel ? `panel:${preferredPanel.id}` : "overview"
  const title = module?.title ?? info?.title ?? kind
  return (
    <>
      <Header
        resource={resource}
        environment={environment.name}
        module={module}
        title={title}
        description={module?.blurb ?? info?.description}
        version={versionOf(binding, config)}
        props={props}
      />
      <Tabs defaultValue={defaultTab}>
        <TabsList
          variant="line"
          aria-label={`${title} sections`}
          className="mb-3 max-w-full overflow-x-auto"
        >
          <TabsTrigger value="overview">Overview</TabsTrigger>
          {panels.map((panel) => (
            <PanelTab key={`${kind}:${panel.id}`} panel={panel} props={props} />
          ))}
        </TabsList>
        <TabsContent value="overview">
          <Overview
            props={props}
            bindings={all}
            module={module}
            title={title}
            environment={environment.name}
          />
        </TabsContent>
        {panels.map((panel) => (
          <TabsContent
            key={panel.id}
            value={`panel:${panel.id}`}
            className="grid gap-4"
          >
            <panel.component {...props} />
          </TabsContent>
        ))}
      </Tabs>
    </>
  )
}

// Each tab is its own component so a panel's useCount, which may use
// hooks, always runs from the same place: a tab that comes or goes
// mounts or unmounts with its hooks, and no other tab's order shifts.
function PanelTab({
  panel,
  props,
}: {
  panel: CatalogPanel
  props: CatalogPanelProps
}) {
  const useCount = panel.useCount ?? noCount
  const count = useCount(props)
  return (
    <TabsTrigger value={`panel:${panel.id}`}>
      {panel.title}
      {count !== undefined && (
        <>
          {" "}
          <TabsCount>{count}</TabsCount>
        </>
      )}
    </TabsTrigger>
  )
}

function noCount(): number | undefined {
  return undefined
}

function Header({
  resource,
  environment,
  module,
  title,
  description,
  version,
  props,
}: {
  resource: string
  environment: string
  module?: CatalogKindModule
  title: string
  description?: string
  version?: string
  props: CatalogPanelProps
}) {
  const state = stateOf(props.binding)
  const Actions = module?.actions
  // A module's subtitle says which instance this is, environment included,
  // so it stands in for the catalog's description of the kind.
  const subtitle = module?.subtitle?.(props, environment)
  return (
    <header className="flex flex-wrap items-center gap-x-3.5 gap-y-3">
      <ProductIcon
        product={module?.logo}
        fallback={kindIcon(module)}
        label={title}
        size="xl"
      />
      <div className="grid min-w-0 flex-[1_1_300px] gap-1">
        <h1 className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-[22px] leading-tight font-semibold tracking-[-0.025em] md:text-2xl">
          <span className="min-w-0 break-all">{resource}</span>
          <span className="font-mono text-[13px] font-normal tracking-normal text-muted-foreground">
            {title}
            {version && <span className="ml-1">{version}</span>}
          </span>
        </h1>
        {(subtitle ?? description) && (
          <p className="max-w-[72ch] text-[13px] text-pretty text-muted-foreground">
            {subtitle ?? description}
          </p>
        )}
      </div>
      {!subtitle && (
        <span className="inline-flex h-5 items-center rounded-full bg-muted px-2 text-xs font-medium text-muted-foreground capitalize">
          {environment}
        </span>
      )}
      <StatusPill tone={state.drift ? "waiting" : "live"}>
        {state.drift ? state.label : (module?.deployedLabel ?? state.label)}
      </StatusPill>
      {Actions && (
        <div className="flex flex-wrap items-center gap-2">
          <Actions {...props} />
        </div>
      )}
    </header>
  )
}

/** Overview is every service page's first tab. */
function Overview({
  props,
  bindings,
  module,
  title,
  environment,
}: {
  props: CatalogPanelProps
  bindings: Binding[]
  module?: CatalogKindModule
  title: string
  environment: string
}) {
  const { binding, config, components } = props
  const parts = config.composition
    ? components.flatMap((b) => {
        const part = parseResourceConfig(b)?.component
        return part ? [{ binding: b, part }] : []
      })
    : []
  const choices = dependencyChoices(config)
  return (
    <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_300px]">
      <div className="grid min-w-0 gap-4">
        <Section
          title="Components"
          description={
            config.composition
              ? "What runs for it, and what recovery keeps of each."
              : "What recovery keeps of it."
          }
        >
          {config.composition ? (
            parts.length > 0 ? (
              parts.map(({ binding: b, part }) => (
                <Row
                  key={b.id}
                  icon={part.component.role === "job" ? PlayIcon : BoxIcon}
                  name={part.component.name}
                  detail={part.component.role === "job" ? "Job" : "Process"}
                  keeps={protectionOf(b)?.label}
                  state={stateOf(b)}
                />
              ))
            ) : (
              <Note>
                No component of it has been deployed in {environment} yet.
              </Note>
            )
          ) : (
            <Row
              icon={kindIcon(module)}
              name={binding.name}
              detail={title}
              keeps={protectionOf(binding)?.label}
              state={stateOf(binding)}
            />
          )}
        </Section>
        {config.composition && (
          <Section
            title="Dependencies"
            description="Where each dependency comes from."
          >
            {choices.length > 0 ? (
              choices.map(([name, choice]) => {
                const target =
                  choice.mode === "new" || choice.mode === "resource"
                    ? bindings.find(
                        (b) =>
                          b.subject === "resource" &&
                          b.name ===
                            (choice.resource ??
                              componentName(props.resource, name))
                      )
                    : undefined
                return (
                  <Row
                    key={name}
                    icon={choice.mode === "external" ? PlugIcon : DatabaseIcon}
                    name={name}
                    detail={sourceOf(choice)}
                    keeps={
                      protectionOf(target, choice)?.label ??
                      (choice.mode === "off" ? undefined : "Not deployed yet")
                    }
                    state={target ? stateOf(target) : undefined}
                  />
                )
              })
            ) : (
              <Note>It has no dependencies.</Note>
            )}
          </Section>
        )}
      </div>
      <Details binding={binding} config={config} environment={environment} />
    </div>
  )
}

function sourceOf(choice: DependencyChoice): string {
  switch (choice.mode) {
    case "new":
      return choice.resource
        ? `Provisioned with it as ${choice.resource}`
        : "Provisioned with it"
    case "resource":
      return `Connected to ${choice.resource ?? "another resource"}`
    case "external":
      return "External endpoint"
    case "off":
      return "Turned off"
  }
  return "Unknown source"
}

function Section({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children: ReactNode
}) {
  return (
    <section aria-label={title} className="grid gap-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <h2 className="text-[15px] font-semibold">{title}</h2>
        <span className="text-[12.5px] text-muted-foreground">
          {description}
        </span>
      </div>
      <ul className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
        {children}
      </ul>
    </section>
  )
}

function Row({
  icon: Icon,
  name,
  detail,
  keeps,
  state,
}: {
  icon: ComponentType<{ className?: string }>
  name: string
  detail: string
  keeps?: string
  state?: State
}) {
  // On a phone what it keeps drops under the name, beside the icon.
  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,17rem)_auto]">
      <span className="flex min-w-0 items-center gap-2.5">
        <span
          aria-hidden="true"
          className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted"
        >
          <Icon className="size-4" />
        </span>
        <span className="grid min-w-0 gap-0.5">
          <span className="truncate text-[13px] font-medium">{name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {detail}
          </span>
        </span>
      </span>
      {keeps ? (
        <span className="order-last col-span-2 min-w-0 pl-[38px] text-[12.5px] text-muted-foreground sm:order-none sm:col-span-1 sm:pl-0 sm:text-right">
          {keeps}
        </span>
      ) : (
        <span className="hidden sm:block" />
      )}
      <span className="justify-self-end">
        {state && (
          <StatusPill tone={state.drift ? "waiting" : "live"}>
            {state.label}
          </StatusPill>
        )}
      </span>
    </li>
  )
}

function Note({ children }: { children: ReactNode }) {
  return (
    <li className="px-4 py-3 text-[13px] text-muted-foreground">{children}</li>
  )
}

function Details({
  binding,
  config,
  environment,
}: {
  binding: Binding
  config: ResourceConfigView
  environment: string
}) {
  const rows: [string, ReactNode][] = [
    ["Environment", <span className="capitalize">{environment}</span>],
    ["Runs on", runsOn(binding.provider || config.provider)],
    ...(binding.region
      ? [["Region", binding.region] as [string, ReactNode]]
      : []),
    [
      "Updated",
      <time dateTime={binding.updatedAt}>{formatDate(binding.updatedAt)}</time>,
    ],
  ]
  return (
    <aside
      aria-label="Details"
      className="grid gap-3 rounded-xl bg-subtle p-4 ring-1 ring-border"
    >
      <h2 className="text-[13px] font-semibold">Details</h2>
      <dl className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-[12.5px]">
        {rows.map(([label, value]) => (
          <div key={label} className="contents">
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="min-w-0 break-words">{value}</dd>
          </div>
        ))}
      </dl>
    </aside>
  )
}

type State = { label: string; drift: boolean }

function stateOf(binding: Binding): State {
  if (binding.drift === "missing")
    return { label: "Deleted outside Loomplane", drift: true }
  if (binding.drift) return { label: "Changed outside Loomplane", drift: true }
  return { label: "Deployed", drift: false }
}

const providerLabels: Record<string, string> = {
  server: "Docker on a server",
  kubernetes: "Kubernetes",
  rds: "Amazon RDS",
  "cloud-sql": "Cloud SQL",
}

function runsOn(provider: string): string {
  return providerLabels[provider] ?? cloudLabel(provider)
}

// The release a composed resource runs, or the engine version a database
// was deployed with.
function versionOf(
  binding: Binding,
  config: ResourceConfigView
): string | undefined {
  if (config.composition?.version) return config.composition.version
  const raw = bindingConfig(binding)
  const catalog = raw?.catalog
  if (
    isRecord(catalog) &&
    typeof catalog.product_version === "string" &&
    catalog.product_version
  )
    return catalog.product_version
  return typeof raw?.version === "string" && raw.version
    ? raw.version
    : undefined
}

function Loading({ resource }: { resource: string }) {
  return (
    <ZeroState role="status" aria-busy="true">
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <BoxIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>Loading {resource}</ZeroStateTitle>
        <ZeroStateDescription>
          Reading what runs in this environment.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

function Missing({
  projectId,
  title,
  description,
}: {
  projectId: string
  title: string
  description: string
}) {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <BoxIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{description}</ZeroStateDescription>
      </ZeroStateHeader>
      <ZeroStateContent>
        <Link
          to="/projects/$id"
          params={{ id: projectId }}
          className={buttonVariants({ variant: "outline" })}
        >
          Open project
        </Link>
      </ZeroStateContent>
    </ZeroState>
  )
}

// A missing permission isn't a failure to retry, so it reads differently.
function Failed({
  error,
  retry,
  fallback,
}: {
  error: unknown
  retry: () => Promise<unknown>
  fallback: string
}) {
  const status = statusOf(error)
  if (status === 401 || status === 403)
    return (
      <ZeroState preview={false}>
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <LockIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>You can&apos;t see this service</ZeroStateTitle>
          <ZeroStateDescription>
            Your role doesn&apos;t include reading this project&apos;s
            deployments. An organization owner can give you access.
          </ZeroStateDescription>
        </ZeroStateHeader>
      </ZeroState>
    )
  return <QueryError error={error} fallback={fallback} retry={retry} />
}
