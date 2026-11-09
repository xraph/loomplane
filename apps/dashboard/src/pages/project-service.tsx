import { BoxIcon, DatabaseIcon, EyeOffIcon, RocketIcon } from "lucide-react"
import { useState } from "react"
import { toast } from "sonner"

import { messageOf, statusOf } from "@/client/errors"
import {
  useEnvironmentsList,
  useManifestsCurrent,
  useManifestsEdit,
  useProjectsGet,
  useProjectsGraph,
} from "@/client/forge/loomplane/hooks"
import type {
  Binding,
  CloudinfraRoute,
  Environment,
  Project,
  ProjectView,
} from "@/client/forge/loomplane/types"
import { useInvalidate, useMutation, useQuery } from "@/client/hooks"
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudConfirm,
  CloudPageHeader,
  CloudTable,
  CloudTableRow,
  FactList,
  NameCell,
  PageMenu,
  RowMenu,
  Segments,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
  statusWord,
} from "@/components/cloud-targets/infrastructure-ui"
import { aliasOf, BindingAlias } from "@/components/binding-alias"
import { AddHostnameDialog } from "@/components/domains/add-hostname-dialog"
import { EdgeCacheDialog } from "@/components/domains/edge-cache-dialog"
import { useRouteLifecycle } from "@/components/domains/use-route-lifecycle"
import { Page } from "@/components/page"
import {
  ExcludeDialog,
  ServiceStatePill,
  useExclusions,
  useServiceRows,
} from "@/components/project-services"
import { ProductIcon } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import { QueryError } from "@/components/query-error"
import { StatusPill, toneOf, type Tone } from "@/components/runs"
import { ServiceAddress } from "@/components/service-address"
import { ServiceScaling } from "@/components/service-scaling"
import { SiteHeader, type Crumb } from "@/components/site-header"
import { Button, buttonVariants } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import {
  connections,
  ecosystemLabel,
  kindLabel,
  resourceLabel,
  shortSHA,
} from "@/lib/architecture"
import { cloudLabel } from "@/lib/clouds"
import { formatDate } from "@/lib/format"
import { useProjectCan } from "@/lib/project-permissions"
import {
  callersOf,
  deployedConfig,
  enabledIn,
  exclusionOf,
  locationOf,
  reasonLabel,
  serviceStatus,
  serviceTabOf,
  sizeLabel,
  type ServiceRow,
  type ServiceTab,
  stateOf,
} from "@/lib/project-services"
import { isReady } from "@/lib/projects"
import { routablePorts } from "@/lib/routable-ports"
import { Link, useNavigate } from "@/lib/router"
import { type Run, runStatusText } from "@/lib/runs"
import { useRuns } from "@/lib/use-runs"
import { useUrlTab } from "@/lib/use-url-tab"

const outlineSm = buttonVariants({ size: "sm", variant: "outline" })

/**
 * ProjectService is one service of a project: where it runs in each
 * environment, what's running now, its deployments, domains and what it
 * uses, and the setting that excludes it.
 */
export function ProjectService({
  projectId,
  name,
  environment,
  tab,
}: {
  projectId: string
  name: string
  environment?: string
  /** The tab the page opens on; Overview when unset. */
  tab?: ServiceTab
}) {
  const project = useQuery(useProjectsGet, { path: { id: projectId } })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const view = useQuery(useProjectsGraph, { path: { id: projectId } })
  const missing = project.status === "error" && statusOf(project.error) === 404
  const crumbs: Crumb[] = [
    { label: "Projects", to: "/projects" },
    project.data
      ? {
          label: project.data.name,
          link: <Link to="/projects/$id" params={{ id: projectId }} />,
        }
      : { label: "Project" },
    { label: name },
  ]

  return (
    <>
      <SiteHeader crumbs={crumbs} />
      <Page>
        {missing ? (
          <CloudEmpty
            kind="notice"
            icon={BoxIcon}
            title="No such project here"
            action={
              <Link to="/projects" className={outlineSm}>
                Back to projects
              </Link>
            }
          >
            It may have been deleted, or it belongs to another workspace.
          </CloudEmpty>
        ) : project.status === "error" ? (
          <QueryError
            error={project.error}
            fallback="The project couldn't be loaded."
            retry={project.refetch}
          />
        ) : environments.status === "error" ? (
          <QueryError
            error={environments.error}
            fallback="The project's environments couldn't be loaded."
            retry={environments.refetch}
          />
        ) : view.status === "error" ? (
          <QueryError
            error={view.error}
            fallback="The project's services couldn't be loaded."
            retry={view.refetch}
          />
        ) : project.data && environments.data && view.data ? (
          <Loaded
            project={project.data}
            environments={environments.data.environments}
            view={view.data}
            name={name}
            environment={environment}
            tab={tab}
          />
        ) : (
          <div className="grid gap-5" aria-busy="true">
            <Skeleton className="h-11 w-72" />
            <Skeleton className="h-8 w-60 rounded-[10px]" />
            <Skeleton className="h-72 rounded-xl" />
          </div>
        )}
      </Page>
    </>
  )
}

function Loaded({
  project,
  environments,
  view,
  name,
  environment,
  tab: initialTab,
}: {
  project: Project
  environments: Environment[]
  view: ProjectView
  name: string
  environment?: string
  tab?: ServiceTab
}) {
  const can = useProjectCan(project.id)
  const navigate = useNavigate()
  const { rows, routes, routesQuery, bindings } = useServiceRows(
    project,
    environments,
    view
  )
  const [tab, setTab] = useUrlTab<ServiceTab>(initialTab, "overview")
  const { runs } = useRuns()
  const [excluding, setExcluding] = useState(false)
  const exclusions = useExclusions(project)

  const row = rows.find((r) => r.service.name === name)
  const excluded = (view.excluded ?? []).find((x) => x.service.name === name)
  const env =
    environments.find((e) => e.name === environment) ??
    environments.find((e) => e.kind === "production") ??
    environments[0]
  const parent = {
    label: project.name,
    onClick: () =>
      void navigate({
        to: "/projects/$id",
        params: { id: project.id },
        search: { tab: "services" },
      }),
  }

  if (excluded) {
    const rule = exclusionOf(excluded, project.exclusions ?? [])
    return (
      <>
        <CloudPageHeader
          parent={parent}
          glyph={<EyeOffIcon className="size-5 text-muted-foreground" />}
          title={name}
          mono
          status={<StatusPill tone="quiet">Excluded</StatusPill>}
          meta={
            <code className="font-mono text-[12.5px]">
              {locationOf(excluded.service)}
            </code>
          }
        />
        <CloudCallout
          tone="info"
          icon={EyeOffIcon}
          title="Excluded from this project"
          action={
            can("project:update") && (
              <Button
                size="sm"
                variant="outline"
                disabled={exclusions.pending}
                onClick={() =>
                  exclusions
                    .include([excluded.service])
                    .then(() =>
                      toast.success(
                        `${name} is back in the graph and the next plan.`
                      )
                    )
                    .catch((err: unknown) =>
                      toast.error(
                        messageOf(err, "The exclusion wasn't removed.")
                      )
                    )
                }
              >
                Include again
              </Button>
            )
          }
        >
          {reasonLabel(rule?.reason ?? "other")}
          {excluded.rule.kind === "path"
            ? `, by the rule ${excluded.rule.match}`
            : ""}
          . It stays out of the graph, the plans and every deploy, on every
          branch.
        </CloudCallout>
      </>
    )
  }

  if (!row) {
    return (
      <>
        <CloudPageHeader
          parent={parent}
          glyph={<BoxIcon className="size-5 text-muted-foreground" />}
          title={name}
          mono
        />
        <CloudEmpty
          kind="notice"
          icon={BoxIcon}
          title={`No service called ${name}`}
        >
          The project&apos;s last analysis didn&apos;t find it. It may have been
          renamed or removed from the repository.
        </CloudEmpty>
      </>
    )
  }

  const s = row.service
  const ecosystem = [ecosystemLabel(s.ecosystem), s.framework]
    .filter(Boolean)
    .join(", ")
  const glyph = ecosystems[s.ecosystem] ? (
    <ProductIcon product={ecosystems[s.ecosystem]} size="lg" />
  ) : (
    <BoxIcon className="size-5 text-muted-foreground" />
  )
  const meta = (
    <>
      {kindLabel(s.kind)} ·{" "}
      <code className="font-mono text-[12.5px]">{locationOf(s)}</code>
      {ecosystem && ` · ${ecosystem}`}
    </>
  )
  const copyId = () =>
    void navigator.clipboard
      ?.writeText(s.id)
      .then(() => toast.success("Service ID copied."))
      .catch(() => toast.error("The service ID couldn't be copied."))
  const menu = (
    <PageMenu label="More service actions">
      <DropdownMenuItem onClick={copyId}>Copy service ID</DropdownMenuItem>
      {can("project:update") && (
        <>
          <DropdownMenuSeparator />
          <DropdownMenuItem
            variant="destructive"
            onClick={() => setExcluding(true)}
          >
            Exclude from project
          </DropdownMenuItem>
        </>
      )}
    </PageMenu>
  )
  const excludeDialog = excluding && (
    <ExcludeDialog
      project={project}
      rows={[row]}
      all={view.graph?.services ?? []}
      onClose={(done) => {
        setExcluding(false)
        if (done) parent.onClick()
      }}
    />
  )

  // A project with no environment yet has nowhere to show the service in.
  if (!env) {
    return (
      <>
        <CloudPageHeader
          parent={parent}
          glyph={glyph}
          title={s.name}
          mono
          meta={meta}
          actions={menu}
        />
        <CloudEmpty
          kind="notice"
          icon={BoxIcon}
          title="No environments yet"
          action={
            <Button size="sm" variant="outline" onClick={parent.onClick}>
              Open {project.name}
            </Button>
          }
        >
          {s.name} deploys into an environment. Add one to the project first.
        </CloudEmpty>
        {excludeDialog}
      </>
    )
  }

  const binding = row.deployed[env.name]
  const linked = deployedConfig(binding).env
  const touched = runs
    .filter(
      (r) =>
        r.projectId === project.id &&
        r.plan.actions.some(
          (a) => a.subject === "service" && a.name === name && a.op !== "keep"
        )
    )
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
  const here = touched.filter((r) => r.environment === env.name)
  const latest = here[0]
  const pickEnvironment = (next: string) =>
    void navigate({
      to: "/projects/$id/services/$service",
      params: { id: project.id, service: name },
      search: {
        environment: next,
        tab: tab === "overview" ? undefined : tab,
      },
    })

  return (
    <>
      <CloudPageHeader
        parent={parent}
        glyph={glyph}
        title={s.name}
        mono
        status={
          bindings[env.name] !== undefined && (
            <ServiceStatePill binding={binding} />
          )
        }
        meta={meta}
        actions={
          <>
            {can("deployment:create") && isReady(project.status) && (
              <Link
                to="/deployments/new"
                search={{
                  project: project.id,
                  environment: env.name,
                  service: name,
                }}
                className={buttonVariants()}
              >
                <RocketIcon />
                Deploy to {env.name}
              </Link>
            )}
            {menu}
          </>
        }
      />

      <Segments
        label="Environment"
        value={env.name}
        onChange={pickEnvironment}
        items={environments.map((e) => ({ id: e.name, label: e.name }))}
      />

      <Tabs
        value={tab}
        onValueChange={(v) => {
          const next = serviceTabOf(v)
          if (next) setTab(next)
        }}
      >
        <TabsList
          variant="line"
          aria-label="Service views"
          className="mb-5 max-w-full overflow-x-auto"
        >
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="deployments">
            Deployments
            {touched.length > 0 && <TabsCount>{touched.length}</TabsCount>}
          </TabsTrigger>
          <TabsTrigger value="variables">
            Variables
            {linked.length > 0 && <TabsCount>{linked.length}</TabsCount>}
          </TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="grid gap-6">
          {binding ? (
            <RunningNow binding={binding} latest={latest} env={env} />
          ) : (
            <NotHere row={row} env={env} />
          )}
          <RunList
            title={`Deployments to ${env.name}`}
            description={`Plans that changed ${name} in ${env.name}, newest first.`}
            runs={here.slice(0, 5)}
            total={here.length}
            binding={binding}
            empty={`Nothing has deployed ${name} to ${env.name} yet.`}
            more={
              touched.length > 5 ? (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setTab("deployments")}
                >
                  See all {touched.length}
                </Button>
              ) : undefined
            }
          />
          <Domains
            row={row}
            env={env}
            projectId={project.id}
            routes={routes.filter(
              (r) =>
                r.input.service === name && r.input.environmentId === env.id
            )}
            routesQuery={routesQuery}
            mayRoute={can("infrastructure:modify")}
          />
          <Uses
            project={project}
            view={view}
            row={row}
            env={env}
            bindings={bindings[env.name] ?? []}
          />
          <Connections
            project={project}
            row={row}
            env={env}
            bindings={bindings[env.name] ?? []}
          />
        </TabsContent>

        <TabsContent value="deployments" className="grid gap-6">
          <RunList
            title="Every deployment of this service"
            description={`Plans that changed ${name}, in every environment.`}
            runs={touched}
            total={touched.length}
            binding={binding}
            showEnvironment
            empty={`Nothing has deployed ${name} yet.`}
          />
        </TabsContent>

        <TabsContent value="variables" className="grid gap-6">
          <Variables row={row} env={env} />
        </TabsContent>

        <TabsContent value="settings" className="grid max-w-[760px] gap-6">
          <section aria-label="Build and run" className="grid gap-2.5">
            <CloudSectionHead
              title="Build and run"
              description="What the last analysis found. The manifest can override any of it."
            />
            <FactList
              facts={[
                {
                  label: "Source",
                  value: (
                    <code className="font-mono text-[12.5px]">
                      {s.entrypoint}
                    </code>
                  ),
                },
                {
                  label: "Built with",
                  value: [ecosystemLabel(s.ecosystem), s.framework, s.runtime]
                    .filter(Boolean)
                    .join(", "),
                },
                ...(s.image
                  ? [
                      {
                        label: "Dockerfile",
                        value: (
                          <code className="font-mono text-[12.5px]">
                            {s.image}
                          </code>
                        ),
                      },
                    ]
                  : []),
                {
                  label: "Ports",
                  value:
                    (s.ports ?? []).length > 0
                      ? (s.ports ?? []).join(", ")
                      : "None found",
                },
                ...(s.source
                  ? [
                      {
                        label: "Repository",
                        value: (
                          <code className="font-mono text-[12.5px]">
                            {s.source}
                          </code>
                        ),
                      },
                    ]
                  : []),
              ]}
            />
          </section>
          <ServiceEnvironments
            project={project}
            row={row}
            environments={environments}
          />
          <ServiceScaling
            project={project}
            name={name}
            environments={environments}
            deployed={row.deployed}
          />
          {can("project:update") && (
            <section
              aria-label="Exclude from this project"
              className="grid gap-2.5"
            >
              <CloudSectionHead
                title="Exclude from this project"
                description={
                  <>
                    For code that should never deploy, such as tooling or test
                    fixtures. It leaves the graph and future plans on every
                    branch, and analyses keep it out. It&apos;s a project
                    setting, so the repository doesn&apos;t change.
                    {Object.values(row.deployed).some(Boolean) &&
                      ` ${name} is running, so you pick what happens to it.`}
                  </>
                }
              >
                <Button
                  size="sm"
                  variant="outline"
                  className="text-destructive"
                  onClick={() => setExcluding(true)}
                >
                  Exclude {name}
                </Button>
              </CloudSectionHead>
            </section>
          )}
        </TabsContent>
      </Tabs>

      {excludeDialog}
    </>
  )
}

const ecosystems: Record<string, ProductKey> = {
  go: "go",
  node: "nodejs",
  nodejs: "nodejs",
}

const products: Record<string, ProductKey> = {
  postgres: "postgresql",
  postgresql: "postgresql",
  mysql: "mysql",
  mariadb: "mariadb",
  redis: "redis",
  digitalocean: "digitalocean",
  vercel: "vercel",
  render: "render",
  gcp: "gcp",
  aws: "aws",
  cloudflare: "cloudflare",
  docker: "docker",
  kubernetes: "kubernetes",
}

function RunningNow({
  binding,
  latest,
  env,
}: {
  binding: Binding
  latest?: Run
  env: Environment
}) {
  const c = deployedConfig(binding)
  const product = products[binding.provider]
  return (
    <section aria-label="Running now" className="grid gap-2.5">
      <CloudSectionHead
        title="Running now"
        description={`What runs in ${env.name}, from its last deploy.`}
      >
        {latest && (
          <Link
            to="/deployments/$id"
            params={{ id: latest.id }}
            className={outlineSm}
          >
            Open deployment
          </Link>
        )}
        <CloudLink binding={binding} className={outlineSm}>
          Open cloud account
        </CloudLink>
      </CloudSectionHead>
      <FactList
        facts={[
          {
            label: "Commit",
            value: binding.commitSha ? (
              <span className="font-mono text-[12.5px]">
                {shortSHA(binding.commitSha)}
              </span>
            ) : (
              "Unknown"
            ),
          },
          {
            label: "Deployed",
            value: `${formatDate(binding.updatedAt)}${latest?.plan.decidedAt ? ", approved plan" : ""}`,
          },
          ...(c.framework || c.image
            ? [
                {
                  label: "Built",
                  value: [c.framework, c.image && `from ${c.image}`]
                    .filter(Boolean)
                    .join(" · "),
                },
              ]
            : []),
          {
            label: "Runs on",
            value: (
              <CloudLink
                binding={binding}
                className="inline-flex items-center gap-1.5 underline-offset-2 hover:underline"
              >
                {product && <ProductIcon product={product} size="sm" />}
                {[cloudLabel(binding.provider), binding.region, sizeLabel(c)]
                  .filter(Boolean)
                  .join(" · ")}
              </CloudLink>
            ),
          },
          ...(c.healthPath
            ? [
                {
                  label: "Health check",
                  value: (
                    <span className="font-mono text-[12.5px]">
                      GET {c.healthPath}
                    </span>
                  ),
                },
              ]
            : []),
          ...(stateOf(binding) === "drifted" && binding.driftMessage
            ? [
                {
                  label: "Drift",
                  value: (
                    <span className="text-weld-ink">
                      {binding.driftMessage}
                    </span>
                  ),
                },
              ]
            : []),
        ]}
      />
    </section>
  )
}

function NotHere({ row, env }: { row: ServiceRow; env: Environment }) {
  const elsewhere = Object.entries(row.deployed).find(([, b]) => b)
  return (
    <section aria-label="Running now" className="grid gap-2.5">
      <CloudSectionHead
        title="Running now"
        description={`What runs in ${env.name}, from its last deploy.`}
      />
      <CloudEmpty
        kind="compact"
        icon={BoxIcon}
        title={`${row.service.name} isn't in ${env.name} yet`}
      >
        {row.service.deployable
          ? `A deploy to ${env.name} plans it with everything else the manifest asks for.`
          : `${kindLabel(row.service.kind)}s don't deploy on their own.`}
        {elsewhere?.[1]?.commitSha &&
          ` ${elsewhere[0]} runs ${shortSHA(elsewhere[1].commitSha)}, if you want the same commit here.`}
      </CloudEmpty>
    </section>
  )
}

/**
 * runState names how a run left the service: the newest that succeeded
 * with what's running now is Live, older successes were Replaced.
 */
function runState(
  r: Run,
  binding: Binding | undefined,
  liveID?: string
): { tone: Tone; label: string } {
  if (r.status === "succeeded")
    return r.id === liveID
      ? { tone: "live", label: "Live" }
      : { tone: "quiet", label: binding ? "Replaced" : "Deployed" }
  return { tone: toneOf(r.status), label: runStatusText[r.status] }
}

function RunList({
  title,
  description,
  runs,
  total,
  binding,
  empty,
  more,
  showEnvironment,
}: {
  title: string
  description: string
  runs: Run[]
  total: number
  binding?: Binding
  empty: string
  more?: React.ReactNode
  showEnvironment?: boolean
}) {
  const liveID = binding
    ? runs.find(
        (r) =>
          r.status === "succeeded" &&
          r.environment === runs[0]?.environment &&
          r.plan.sources.some((s) => s.commitSha === binding.commitSha)
      )?.id
    : undefined
  return (
    <section aria-label={title} className="grid gap-2.5">
      <CloudSectionHead title={title} count={total} description={description} />
      {runs.length === 0 ? (
        <CloudEmpty kind="compact" icon={RocketIcon} title="No deployments yet">
          {empty}
        </CloudEmpty>
      ) : (
        <CloudTable
          label={title}
          columns="minmax(0,1fr) 160px 130px 70px"
          head={["Deployment", "Status", "Started", ""]}
          minWidth={560}
          footer={
            <>
              <span>
                {total > runs.length
                  ? `Showing ${runs.length} of ${total}`
                  : `Showing ${runs.length}`}
              </span>
              {more}
            </>
          }
        >
          {runs.map((r) => {
            const state = runState(r, binding, liveID)
            const shas = r.plan.sources
              .map((s) => shortSHA(s.commitSha))
              .filter(Boolean)
              .join(", ")
            return (
              <CloudTableRow key={r.id}>
                <NameCell
                  mono
                  name={
                    <Link
                      to="/deployments/$id"
                      params={{ id: r.id }}
                      className="underline-offset-2 hover:underline"
                    >
                      {shas || "No commit"}
                    </Link>
                  }
                  meta={
                    showEnvironment
                      ? r.environment
                      : r.status === "succeeded"
                        ? undefined
                        : r.phase
                  }
                />
                <Cell>
                  <StatusPill tone={state.tone}>{state.label}</StatusPill>
                </Cell>
                <Cell className="text-[12.5px] text-muted-foreground">
                  {formatDate(r.createdAt)}
                </Cell>
                <ActionsCell>
                  <Link
                    to="/deployments/$id"
                    params={{ id: r.id }}
                    className={outlineSm}
                  >
                    Open
                  </Link>
                </ActionsCell>
              </CloudTableRow>
            )
          })}
        </CloudTable>
      )}
    </section>
  )
}

/** routeTone draws a domain route's status. */
function routeTone(status: string): Tone {
  if (/active|ready|live|ok|serving/i.test(status)) return "live"
  if (/fail|error|blocked/i.test(status)) return "failed"
  return "waiting"
}

function Domains({
  projectId,
  row,
  env,
  routes,
  routesQuery,
  mayRoute,
}: {
  projectId: string
  row: ServiceRow
  env: Environment
  routes: CloudinfraRoute[]
  routesQuery: ReturnType<typeof useServiceRows>["routesQuery"]
  mayRoute: boolean
}) {
  const binding = row.deployed[env.name]
  const routable = routablePorts(row.service.ports ?? [], binding)
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<CloudinfraRoute>()
  const [caching, setCaching] = useState<CloudinfraRoute>()
  const name = row.service.name
  const count = routes.length + (binding?.url ? 1 : 0)
  return (
    <section aria-label="Domains" className="grid gap-2.5">
      <CloudSectionHead
        title="Domains"
        count={routesQuery.data ? count : undefined}
        description={`Hostnames that reach ${name} in ${env.name}.`}
      >
        <Link to="/domains" className={outlineSm}>
          Manage domains
        </Link>
        {mayRoute && (
          <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
            Add a domain
          </Button>
        )}
      </CloudSectionHead>
      {routesQuery.status === "error" ? (
        <QueryError
          error={routesQuery.error}
          fallback="The service's domains couldn't be loaded."
          retry={routesQuery.refetch}
        />
      ) : !routesQuery.data ? (
        <Skeleton className="h-24 rounded-xl" />
      ) : count === 0 ? (
        <CloudEmpty kind="compact" title="No domains yet">
          {binding
            ? `None routed to it in ${env.name}.`
            : `None in ${env.name}. It gets a generated address when it deploys.`}
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Domains"
          columns="minmax(0,1.4fr) minmax(0,1fr) 120px 170px"
          head={["Hostname", "Source", "Status", ""]}
          minWidth={620}
        >
          {routes.map((r) => (
            <CloudTableRow key={r.id}>
              <NameCell mono name={r.input.hostname} />
              <Cell className="text-[12.5px] text-muted-foreground">
                Custom
              </Cell>
              <Cell>
                <StatusPill tone={routeTone(r.status)}>
                  {r.status ? statusWord(r.status) : "Routed"}
                </StatusPill>
              </Cell>
              <ActionsCell>
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={`CDN and cache for ${r.input.hostname}`}
                  onClick={() => setCaching(r)}
                >
                  CDN and cache
                </Button>
                {mayRoute && (
                  <RowMenu label={`More actions for ${r.input.hostname}`}>
                    <DropdownMenuItem
                      variant="destructive"
                      onClick={() => setRemoving(r)}
                    >
                      Remove
                    </DropdownMenuItem>
                  </RowMenu>
                )}
              </ActionsCell>
            </CloudTableRow>
          ))}
          {binding?.url && (
            <CloudTableRow>
              <NameCell
                mono
                name={
                  <ServiceAddress
                    address={binding.url}
                    className="font-mono font-medium"
                  />
                }
              />
              <Cell className="text-[12.5px] text-muted-foreground">
                Generated by {cloudLabel(binding.provider)}
              </Cell>
              <Cell>
                <ServiceStatePill binding={binding} short />
              </Cell>
              <ActionsCell />
            </CloudTableRow>
          )}
        </CloudTable>
      )}
      <AddHostnameDialog
        projectId={projectId}
        environment={env}
        service={name}
        ports={routable.ports}
        blocked={routable.reason}
        deployed={!!binding}
        open={adding}
        onOpenChange={setAdding}
      />
      {caching && (
        <EdgeCacheDialog
          projectId={projectId}
          routeId={caching.id}
          hostname={caching.input.hostname}
          writable={mayRoute}
          onClose={() => setCaching(undefined)}
        />
      )}
      {removing && (
        <RemoveHostname
          projectId={projectId}
          route={removing}
          service={name}
          onClose={() => setRemoving(undefined)}
        />
      )}
    </section>
  )
}

/** RemoveHostname confirms, then removes one custom hostname's route. */
function RemoveHostname({
  projectId,
  route,
  service,
  onClose,
}: {
  projectId: string
  route: CloudinfraRoute
  service: string
  onClose(): void
}) {
  const lifecycle = useRouteLifecycle(projectId)
  const host = route.input.hostname
  const remove = async () => {
    try {
      await lifecycle.remove(route)
      toast.success(`Loomplane is removing ${host} from ${service}.`)
      onClose()
    } catch {
      // useRouteLifecycle words the refusal, shown below.
    }
  }
  return (
    <CloudConfirm
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
      title={`Remove ${host}?`}
      confirmLabel="Remove"
      pending={lifecycle.pending}
      onConfirm={() => void remove()}
      error={
        lifecycle.error && (
          <p role="alert" className="text-[12.5px] text-destructive">
            {lifecycle.error}
          </p>
        )
      }
    >
      Loomplane removes its DNS record and stops sending its traffic to{" "}
      {service}. You can add it again later.
    </CloudConfirm>
  )
}

function Uses({
  project,
  view,
  row,
  env,
  bindings,
}: {
  project: Project
  view: ProjectView
  row: ServiceRow
  env: Environment
  bindings: Binding[]
}) {
  const can = useProjectCan(project.id)
  const binding = row.deployed[env.name]
  const links = deployedConfig(binding).env.filter(
    (l) => l.subject === "resource"
  )
  const needs = view.graph ? connections(view.graph, row.service.id).needs : []
  const linked = new Set(links.map((l) => l.from))
  const product = binding ? products[binding.provider] : undefined
  const resourceRows = links.map((l) => {
    const rb = bindings.find(
      (b) => b.subject === "resource" && b.name === l.from
    )
    const rc = deployedConfig(rb)
    return { name: l.from, key: l.key, kind: rc.kind, version: rc.version, rb }
  })
  // What the code needs that nothing deployed provides yet.
  const unmet = needs.filter(
    (n) =>
      !resourceRows.some((r) => r.kind === n.resource.kind) &&
      !linked.has(n.resource.kind)
  )
  const count = resourceRows.length + unmet.length + (binding ? 1 : 0)
  const icon = (kind?: string) =>
    kind && products[kind] ? (
      <ProductIcon product={products[kind]} size="sm" />
    ) : (
      <DatabaseIcon />
    )
  return (
    <section aria-label="What it uses" className="grid gap-2.5">
      <CloudSectionHead
        title="Uses"
        count={count}
        description={`The databases and caches ${row.service.name} reads in ${env.name}, and the app it runs as.`}
      />
      {count === 0 ? (
        <CloudEmpty kind="compact" icon={DatabaseIcon} title="No resources yet">
          Nothing found: its code needs no resources.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="What it uses"
          columns="minmax(0,1fr) minmax(0,1.2fr) 70px"
          head={["Resource", "How", ""]}
          minWidth={520}
        >
          {resourceRows.map((r) => (
            <CloudTableRow key={r.name}>
              <NameCell
                tile={icon(r.kind)}
                name={
                  <span className="inline-flex items-center gap-1">
                    <ResourceLink
                      project={project}
                      env={env}
                      name={r.rb ? r.name : undefined}
                    >
                      {r.rb ? aliasOf(r.rb) : r.name}
                    </ResourceLink>
                    {r.rb && can("deployment:create") && (
                      <BindingAlias
                        projectId={project.id}
                        environment={env.name}
                        binding={r.rb}
                      />
                    )}
                  </span>
                }
                meta={[
                  r.rb?.alias && r.name,
                  r.kind &&
                    `${resourceLabel(r.kind)}${r.version ? ` ${r.version}` : ""}`,
                ]
                  .filter(Boolean)
                  .join(" · ")}
              />
              <Cell className="text-[12.5px] text-muted-foreground">
                As <code className="font-mono text-foreground">{r.key}</code>
              </Cell>
              <ActionsCell>
                {r.rb && (
                  <Link
                    to="/projects/$id/catalog/$resource"
                    params={{ id: project.id, resource: r.name }}
                    search={{ environment: env.id }}
                    className={outlineSm}
                  >
                    Open
                  </Link>
                )}
              </ActionsCell>
            </CloudTableRow>
          ))}
          {unmet.map((n) => (
            <CloudTableRow key={n.resource.id}>
              <NameCell
                tile={icon(n.resource.kind)}
                name={resourceLabel(n.resource.kind)}
              />
              <Cell className="text-[12.5px] text-muted-foreground">
                {n.resource.needsConfirmation
                  ? "Found in its code, not confirmed"
                  : `Found in its code, not in ${env.name} yet`}
                {n.via ? `, through ${n.via}` : ""}
              </Cell>
              <ActionsCell />
            </CloudTableRow>
          ))}
          {binding && (
            <CloudTableRow>
              <NameCell
                tile={
                  product ? (
                    <ProductIcon product={product} size="sm" />
                  ) : (
                    <BoxIcon />
                  )
                }
                name={
                  <span className="inline-flex items-center gap-1">
                    {cloudLabel(binding.provider)} app{" "}
                    <span className="font-mono font-medium">
                      {aliasOf(binding)}
                    </span>
                    {can("deployment:create") && (
                      <BindingAlias
                        projectId={project.id}
                        environment={env.name}
                        binding={binding}
                      />
                    )}
                  </span>
                }
                meta={[
                  binding.externalId || binding.physicalId,
                  sizeLabel(deployedConfig(binding)),
                ]
                  .filter(Boolean)
                  .join(" · ")}
              />
              <Cell className="text-[12.5px] text-muted-foreground">
                Runs {row.service.name}
              </Cell>
              <ActionsCell>
                <CloudLink binding={binding} className={outlineSm}>
                  Open
                </CloudLink>
              </ActionsCell>
            </CloudTableRow>
          )}
        </CloudTable>
      )}
    </section>
  )
}

/**
 * CloudLink opens the connected cloud account a binding deployed through,
 * or the Clouds page when it predates bindings recording their connection.
 */
function CloudLink({
  binding,
  className,
  children,
}: {
  binding: Binding
  className?: string
  children: React.ReactNode
}) {
  return binding.connectionId ? (
    <Link
      to="/clouds/accounts/$id"
      params={{ id: binding.connectionId }}
      className={className}
    >
      {children}
    </Link>
  ) : (
    <Link to="/clouds" className={className}>
      {children}
    </Link>
  )
}

/**
 * ResourceLink opens a resource's page in the environment, or renders its
 * name plainly when nothing of it runs there to open.
 */
function ResourceLink({
  project,
  env,
  name,
  children,
}: {
  project: Project
  env: Environment
  name?: string
  children: React.ReactNode
}) {
  if (!name) return <>{children}</>
  return (
    <Link
      to="/projects/$id/catalog/$resource"
      params={{ id: project.id, resource: name }}
      search={{ environment: env.id }}
      className="underline-offset-2 hover:underline"
    >
      {children}
    </Link>
  )
}

function Connections({
  project,
  row,
  env,
  bindings,
}: {
  project: Project
  row: ServiceRow
  env: Environment
  bindings: Binding[]
}) {
  const name = row.service.name
  const binding = row.deployed[env.name]
  const find = (subject: string, from: string) =>
    bindings.find((b) => b.subject === subject && b.name === from)
  const items = [
    ...deployedConfig(binding).env.map((l) => ({
      id: `to:${l.key}`,
      direction: "Depends on",
      subject: l.subject,
      name: l.from,
      key: l.key,
      binding: find(l.subject, l.from),
    })),
    ...callersOf(name, bindings).map((c) => ({
      id: `from:${c.binding.id}`,
      direction: "Called by",
      subject: "service",
      name: c.binding.name,
      key: c.key,
      binding: c.binding as Binding | undefined,
    })),
  ]
  return (
    <section aria-label="Connections" className="grid gap-2.5">
      <CloudSectionHead
        title="Connections"
        count={items.length}
        description={`What ${name} links to and what links to it, from what's deployed in ${env.name}.`}
      />
      {items.length === 0 ? (
        <CloudEmpty kind="compact" title="No connections yet">
          {binding
            ? `Nothing deployed in ${env.name} links to or from it.`
            : `${name} isn't deployed in ${env.name}.`}
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Connections"
          columns="minmax(0,1fr) 110px minmax(0,1fr) 150px"
          head={["Name", "Direction", "Variable", "State"]}
          minWidth={600}
        >
          {items.map((c) => (
            <CloudTableRow key={c.id}>
              <NameCell
                name={
                  c.subject === "service" ? (
                    <Link
                      to="/projects/$id/services/$service"
                      params={{ id: project.id, service: c.name }}
                      search={{ environment: env.name }}
                      className="underline-offset-2 hover:underline"
                    >
                      {c.name}
                    </Link>
                  ) : (
                    <ResourceLink
                      project={project}
                      env={env}
                      name={c.binding ? c.name : undefined}
                    >
                      {c.name}
                    </ResourceLink>
                  )
                }
                meta={c.subject === "service" ? "Service" : "Resource"}
              />
              <Cell className="text-[12.5px] text-muted-foreground">
                {c.direction}
              </Cell>
              <Cell className="truncate font-mono text-[12px]">{c.key}</Cell>
              <Cell>
                <ServiceStatePill binding={c.binding} short />
              </Cell>
            </CloudTableRow>
          ))}
        </CloudTable>
      )}
    </section>
  )
}

function Variables({ row, env }: { row: ServiceRow; env: Environment }) {
  const binding = row.deployed[env.name]
  const links = deployedConfig(binding).env
  return (
    <section aria-label="Linked variables" className="grid gap-2.5">
      <CloudSectionHead
        title="Linked variables"
        count={links.length}
        description={`The variables Loomplane links into ${row.service.name} in ${env.name}. Values you set yourself are on the project's Variables tab.`}
      />
      {links.length === 0 ? (
        <CloudEmpty kind="list" title="No linked variables yet">
          {binding
            ? "It links to nothing else."
            : `Not deployed in ${env.name}, so nothing is linked yet.`}
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Linked variables"
          columns="minmax(0,1fr) minmax(0,1fr)"
          head={["Key", "From"]}
          minWidth={480}
        >
          {links.map((l) => (
            <CloudTableRow key={l.key}>
              <Cell className="font-mono text-[12.5px] font-medium">
                {l.key}
              </Cell>
              <Cell className="text-[12.5px] text-muted-foreground">
                {l.from}
                {l.subject === "resource" ? `, in ${env.name}` : " (service)"}
              </Cell>
            </CloudTableRow>
          ))}
        </CloudTable>
      )}
    </section>
  )
}

/**
 * ServiceEnvironments turns the service on or off in each environment,
 * through the manifest. Leaving one out removes it there at the next
 * deploy, which still goes through a plan someone approves.
 */
function ServiceEnvironments({
  project,
  row,
  environments,
}: {
  project: Project
  row: ServiceRow
  environments: Environment[]
}) {
  const can = useProjectCan(project.id)
  const name = row.service.name
  const current = useQuery(useManifestsCurrent, { path: { id: project.id } })
  const save = useMutation(useManifestsEdit)
  const invalidate = useInvalidate()
  const [pending, setPending] = useState("")
  const services = current.data?.manifest?.services as
    Record<string, unknown> | undefined
  const inManifest = !!services && name in services
  const flip = async (environment: string, enabled: boolean) => {
    setPending(environment)
    try {
      await save.mutateAsync({
        path: { id: project.id },
        body: {
          placement: { section: "services", name, environment, enabled },
        },
      })
      invalidate(useManifestsCurrent)
      toast.success(
        enabled
          ? `${name} deploys to ${environment} again.`
          : `${name} is left out of ${environment}. The next deploy there removes it.`
      )
    } catch (err) {
      toast.error(messageOf(err, "The environment wasn't changed."))
    } finally {
      setPending("")
    }
  }
  // Where the service stands in one environment, with the manifest's say.
  const stateIn = (on: boolean, b?: Binding): { tone: Tone; label: string } =>
    !on
      ? b
        ? { tone: "waiting", label: "Left out, removed at the next deploy" }
        : { tone: "quiet", label: "Left out" }
      : b
        ? serviceStatus(b)
        : { tone: "quiet", label: "Not deployed yet" }
  return (
    <section aria-label="Environments" className="grid gap-2.5">
      <CloudSectionHead
        title="Environments"
        count={environments.length}
        description={`Where ${name} deploys. Leaving one out removes it there at the next deploy.`}
      />
      {current.status === "error" ? (
        <QueryError
          error={current.error}
          fallback="The manifest couldn't be loaded."
          retry={current.refetch}
        />
      ) : !current.data ? (
        <Skeleton className="h-24 rounded-xl" />
      ) : !inManifest ? (
        <CloudCallout
          tone="info"
          title={`The manifest doesn't deploy ${name} yet`}
        >
          Deploying it adds it, in every environment.
        </CloudCallout>
      ) : environments.length === 0 ? (
        <CloudEmpty kind="compact" title="No environments yet">
          Add an environment to the project to deploy {name}.
        </CloudEmpty>
      ) : (
        <CloudTable
          label="Environments"
          columns="64px minmax(0,1fr) minmax(0,1.4fr)"
          head={["Deploys", "Environment", "State"]}
          minWidth={460}
        >
          {environments.map((e) => {
            const on = enabledIn(services[name], e.name)
            const state =
              pending === e.name
                ? { tone: "flight" as Tone, label: "Saving" }
                : stateIn(on, row.deployed[e.name])
            return (
              <CloudTableRow key={e.id}>
                <Cell className="flex">
                  <Checkbox
                    checked={on}
                    disabled={!can("project:update") || pending !== ""}
                    onCheckedChange={(v) => flip(e.name, !!v)}
                    aria-label={`Deploy ${name} to ${e.name}`}
                  />
                </Cell>
                <Cell className="font-medium">{e.name}</Cell>
                <Cell>
                  <StatusPill tone={state.tone}>{state.label}</StatusPill>
                </Cell>
              </CloudTableRow>
            )
          })}
        </CloudTable>
      )}
    </section>
  )
}
