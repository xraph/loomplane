import {
  BoxIcon,
  ChevronLeftIcon,
  DatabaseIcon,
  EyeOffIcon,
  RocketIcon,
} from "lucide-react"
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
import { AddHostnameDialog } from "@/components/domains/add-hostname-dialog"
import { useRouteLifecycle } from "@/components/domains/use-route-lifecycle"
import { Page, PageHeading } from "@/components/page"
import {
  ExcludeDialog,
  ServiceStateLabel,
  useExclusions,
  useServiceRows,
} from "@/components/project-services"
import { QueryError } from "@/components/query-error"
import { ProductIcon } from "@/components/product-icon"
import type { ProductKey } from "@/components/product-logos"
import { EnvBadge } from "@/components/runs"
import { ServiceAddress } from "@/components/service-address"
import { useShell } from "@/components/shell"
import { SiteHeader } from "@/components/site-header"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty"
import { Skeleton } from "@/components/ui/skeleton"
import { ServiceScaling } from "@/components/service-scaling"
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
import { useOrganization } from "@/lib/organization"
import {
  callersOf,
  deployedConfig,
  enabledIn,
  exclusionOf,
  locationOf,
  reasonLabel,
  sizeLabel,
  type ServiceRow,
  stateOf,
} from "@/lib/project-services"
import { isReady } from "@/lib/projects"
import { Link, useNavigate } from "@/lib/router"
import { type Run, runStatusText } from "@/lib/runs"
import { useRuns } from "@/lib/use-runs"
import { cn } from "@/lib/utils"

/**
 * ProjectService is one service of a project: where it runs in each
 * environment, what's running now, its deployments, domains and what it
 * uses, and the setting that excludes it.
 */
export function ProjectService({
  projectId,
  name,
  environment,
}: {
  projectId: string
  name: string
  environment?: string
}) {
  const project = useQuery(useProjectsGet, { path: { id: projectId } })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const view = useQuery(useProjectsGraph, { path: { id: projectId } })
  const missing = project.status === "error" && statusOf(project.error) === 404

  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Projects", to: "/projects" },
          { label: project.data?.name ?? "Project" },
          { label: name },
        ]}
      />
      <Page>
        {project.status === "error" &&
          (missing ? (
            <Empty preview={false}>
              <EmptyHeader>
                <EmptyTitle>No such project here</EmptyTitle>
                <EmptyDescription>
                  It may have been deleted, or it belongs to another workspace.
                </EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Link to="/projects" className={buttonVariants()}>
                  Back to projects
                </Link>
              </EmptyContent>
            </Empty>
          ) : (
            <QueryError
              error={project.error}
              fallback="The project couldn't be loaded."
              retry={project.refetch}
            />
          ))}
        {view.status === "error" && (
          <QueryError
            error={view.error}
            fallback="The project's services couldn't be loaded."
            retry={view.refetch}
          />
        )}
        {project.data && environments.data && view.data ? (
          <Loaded
            project={project.data}
            environments={environments.data.environments}
            view={view.data}
            name={name}
            environment={environment}
          />
        ) : (
          project.status !== "error" &&
          view.status !== "error" && (
            <div className="grid gap-5" aria-busy="true">
              <Skeleton className="h-10 w-72" />
              <div className="grid gap-3 md:grid-cols-2">
                <Skeleton className="h-20 rounded-xl" />
                <Skeleton className="h-20 rounded-xl" />
              </div>
              <Skeleton className="h-72 rounded-xl" />
            </div>
          )
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
}: {
  project: Project
  environments: Environment[]
  view: ProjectView
  name: string
  environment?: string
}) {
  const { can } = useOrganization()
  const { openDeploy } = useShell()
  const navigate = useNavigate()
  const { rows, routes, bindings } = useServiceRows(project, environments, view)
  const [tab, setTab] = useState("overview")
  const { runs } = useRuns()
  const [excluding, setExcluding] = useState(false)
  const exclusions = useExclusions(project)

  const row = rows.find((r) => r.service.name === name)
  const excluded = (view.excluded ?? []).find((x) => x.service.name === name)
  const env =
    environments.find((e) => e.name === environment) ??
    environments.find((e) => e.kind === "production") ??
    environments[0]
  const back = (
    <Link
      to="/projects/$id"
      params={{ id: project.id }}
      className="-mb-2.5 inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
    >
      <ChevronLeftIcon className="size-3.5" />
      {project.name}
    </Link>
  )

  if (excluded) {
    const rule = exclusionOf(excluded, project.exclusions ?? [])
    return (
      <>
        {back}
        <PageHeading title={name} description={locationOf(excluded.service)} />
        <Alert className="rounded-xl">
          <EyeOffIcon />
          <AlertTitle>Excluded from this project</AlertTitle>
          <AlertDescription>
            {reasonLabel(rule?.reason ?? "other")}
            {excluded.rule.kind === "path"
              ? `, by the rule ${excluded.rule.match}`
              : ""}
            . It stays out of the graph, the plans and every deploy, on every
            branch.
          </AlertDescription>
          {can("project:update") && (
            <div className="col-start-2 mt-2">
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
            </div>
          )}
        </Alert>
      </>
    )
  }

  if (!row || !env) {
    return (
      <>
        {back}
        <Empty preview={false}>
          <EmptyHeader>
            <EmptyTitle>No service called {name}</EmptyTitle>
            <EmptyDescription>
              The project&apos;s last analysis didn&apos;t find it. It may have
              been renamed or removed from the repository.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      </>
    )
  }

  const s = row.service
  const binding = row.deployed[env.name]
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
  const pick = (next: string) =>
    navigate({
      to: "/projects/$id/services/$service",
      params: { id: project.id, service: name },
      search: { environment: next },
    })

  return (
    <>
      {back}
      <PageHeading
        title={
          <span className="flex items-center gap-3">
            <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-card ring-1 ring-border">
              {ecosystems[s.ecosystem] ? (
                <ProductIcon product={ecosystems[s.ecosystem]} size="lg" />
              ) : (
                <BoxIcon className="size-5 text-muted-foreground" />
              )}
            </span>
            <span className="min-w-0 wrap-break-word">{s.name}</span>
          </span>
        }
        actions={
          can("deployment:create") &&
          isReady(project.status) && (
            <Button
              onClick={() =>
                openDeploy({ project: project.id, environment: env.name })
              }
            >
              <RocketIcon />
              {binding ? "Redeploy" : `Deploy to ${env.name}`}
            </Button>
          )
        }
      >
        <div className="mt-1 flex flex-wrap gap-1.5">
          <Badge
            variant="secondary"
            className="bg-muted font-normal text-muted-foreground"
          >
            {kindLabel(s.kind)}
          </Badge>
          <Badge variant="outline" className="font-mono font-normal">
            {locationOf(s)}
          </Badge>
          <Badge
            variant="secondary"
            className="bg-muted font-normal text-muted-foreground"
          >
            {[ecosystemLabel(s.ecosystem), s.framework]
              .filter(Boolean)
              .join(", ")}
          </Badge>
          {(s.ports ?? []).map((p) => (
            <Badge
              key={p}
              variant="secondary"
              className="bg-muted font-mono font-normal text-muted-foreground"
            >
              port {p}
            </Badge>
          ))}
        </div>
      </PageHeading>

      <div
        role="radiogroup"
        aria-label="Environment"
        className="grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-2.5"
      >
        {environments.map((e) => {
          const b = row.deployed[e.name]
          const on = e.id === env.id
          return (
            <button
              key={e.id}
              type="button"
              role="radio"
              aria-checked={on}
              onClick={() => pick(e.name)}
              className={cn(
                "grid gap-1.5 rounded-xl bg-card px-3.5 py-2.5 text-left ring-1 transition-shadow",
                on
                  ? "ring-2 ring-primary"
                  : "ring-border hover:ring-foreground/25"
              )}
            >
              <span className="flex items-center gap-2">
                <span className="text-sm font-semibold">{e.name}</span>
                <EnvBadge env={e.kind} />
              </span>
              <span className="flex items-center gap-2">
                <ServiceStateLabel binding={b} />
                {b && (
                  <span className="text-[12.5px] text-muted-foreground">
                    {[shortSHA(b.commitSha), formatDate(b.updatedAt)]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                )}
              </span>
            </button>
          )
        })}
      </div>

      <Tabs value={tab} onValueChange={(v) => setTab(String(v))}>
        <TabsList variant="line" className="mb-3 max-w-full overflow-x-auto">
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="deployments">
            Deployments
            {touched.length > 0 && <TabsCount>{touched.length}</TabsCount>}
          </TabsTrigger>
          <TabsTrigger value="variables">
            Variables
            {deployedConfig(binding).env.length > 0 && (
              <TabsCount>{deployedConfig(binding).env.length}</TabsCount>
            )}
          </TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="overview">
          <div className="flex flex-wrap items-start gap-4">
            <div className="grid min-w-0 flex-[999_1_520px] gap-4">
              {binding ? (
                <RunningNow binding={binding} latest={latest} />
              ) : (
                <NotHere
                  row={row}
                  env={env}
                  onDeploy={() =>
                    openDeploy({ project: project.id, environment: env.name })
                  }
                  mayDeploy={
                    can("deployment:create") && isReady(project.status)
                  }
                />
              )}
              <RunList
                title={`Deployments to ${env.name}`}
                runs={here.slice(0, 5)}
                binding={binding}
                empty={`Nothing has deployed ${name} to ${env.name} yet.`}
                more={
                  touched.length > 5 ? (
                    <button
                      type="button"
                      onClick={() => setTab("deployments")}
                      className="text-[12.5px] font-medium text-primary hover:underline"
                    >
                      See all {touched.length}
                    </button>
                  ) : undefined
                }
              />
            </div>
            <aside className="grid min-w-0 flex-[1_1_300px] gap-4">
              <Domains
                row={row}
                env={env}
                projectId={project.id}
                routes={routes.filter(
                  (r) =>
                    r.input.service === name && r.input.environmentId === env.id
                )}
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
            </aside>
          </div>
        </TabsContent>

        <TabsContent value="deployments">
          <RunList
            title="Every deployment of this service"
            runs={touched}
            binding={binding}
            showEnvironment
            empty={`Nothing has deployed ${name} yet.`}
          />
        </TabsContent>

        <TabsContent value="variables">
          <Variables row={row} env={env} />
        </TabsContent>

        <TabsContent value="settings" className="grid max-w-[760px] gap-3">
          <section className="rounded-xl px-4 pt-1 pb-1.5 ring-1 ring-border">
            <h2 className="pt-3 text-sm font-semibold">Build and run</h2>
            <p className="mb-2 text-[12.5px] text-muted-foreground">
              What the last analysis found. The manifest can override any of it.
            </p>
            <dl className="grid grid-cols-[150px_minmax(0,1fr)] text-[13px]">
              <Row label="Source">
                <code className="font-mono text-[12.5px]">{s.entrypoint}</code>
              </Row>
              <Row label="Built with">
                {[ecosystemLabel(s.ecosystem), s.framework, s.runtime]
                  .filter(Boolean)
                  .join(", ")}
              </Row>
              {s.image && (
                <Row label="Dockerfile">
                  <code className="font-mono text-[12.5px]">{s.image}</code>
                </Row>
              )}
              <Row label="Ports">
                {(s.ports ?? []).length > 0
                  ? (s.ports ?? []).join(", ")
                  : "None found"}
              </Row>
              {s.source && (
                <Row label="Repository">
                  <code className="font-mono text-[12.5px]">{s.source}</code>
                </Row>
              )}
            </dl>
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
            <section className="flex flex-wrap items-center gap-3 rounded-xl px-4 py-3.5 ring-1 ring-border">
              <div className="grid flex-[1_1_360px] gap-1">
                <h2 className="text-sm font-semibold">
                  Exclude from this project
                </h2>
                <p className="text-[12.5px] leading-relaxed text-muted-foreground">
                  For code that should never deploy, such as tooling or test
                  fixtures. It leaves the graph and future plans on every
                  branch, and analyses keep it out. It&apos;s a project setting,
                  so the repository doesn&apos;t change.
                  {Object.values(row.deployed).some(Boolean) &&
                    ` ${name} is running, so you pick what happens to it.`}
                </p>
              </div>
              <Button variant="outline" onClick={() => setExcluding(true)}>
                Exclude {name}
              </Button>
            </section>
          )}
        </TabsContent>
      </Tabs>

      {excluding && (
        <ExcludeDialog
          project={project}
          rows={[row]}
          all={view.graph?.services ?? []}
          onClose={(done) => {
            setExcluding(false)
            if (done)
              navigate({ to: "/projects/$id", params: { id: project.id } })
          }}
        />
      )}
    </>
  )
}

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <>
      <dt className="border-t py-2.5 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 border-t py-2.5">{children}</dd>
    </>
  )
}

const card = "overflow-hidden rounded-[14px] bg-card ring-1 ring-border"
const cardHead = "flex items-center justify-between gap-2 border-b px-4 py-3"
const outlineButton =
  "inline-flex h-8 items-center rounded-[9px] bg-card px-3 text-[12.5px] font-medium ring-1 ring-border transition-colors hover:bg-muted"

/** A status pill: the colour is never the only signal, the word is there. */
function Pill({
  tone,
  children,
}: {
  tone: "live" | "waiting" | "flight" | "failed" | "idle"
  children: React.ReactNode
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 justify-self-start rounded-full px-2 py-px text-xs font-medium whitespace-nowrap",
        tone === "live" && "bg-overdye-tint text-overdye-ink",
        tone === "waiting" && "bg-weld-tint text-weld-ink",
        tone === "flight" && "bg-flight-tint text-flight-ink",
        tone === "failed" && "bg-failed-tint text-failed",
        tone === "idle" && "bg-muted text-muted-foreground"
      )}
    >
      <span
        className={cn(
          "size-1.5 rounded-full",
          tone === "live" && "bg-overdye",
          tone === "waiting" && "bg-weld",
          tone === "flight" && "bg-flight",
          tone === "failed" && "bg-failed",
          tone === "idle" && "bg-muted-foreground/50"
        )}
      />
      {children}
    </span>
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

function RunningNow({ binding, latest }: { binding: Binding; latest?: Run }) {
  const state = stateOf(binding)
  const c = deployedConfig(binding)
  const product = products[binding.provider]
  return (
    <section aria-label="Running now" className={card}>
      <div className={cardHead}>
        <h2 className="text-sm font-semibold">Running now</h2>
        {state === "live" ? (
          <Pill tone="live">
            {c.healthPath ? "Live, health checked" : "Live"}
          </Pill>
        ) : (
          <Pill tone="waiting">
            {binding.drift === "missing"
              ? "Deleted outside Loomplane"
              : "Changed outside Loomplane"}
          </Pill>
        )}
      </div>
      <dl className="grid grid-cols-[130px_minmax(0,1fr)] px-4 pb-1 text-[13px] [&>dd:nth-child(2)]:border-t-0 [&>dt:first-child]:border-t-0">
        <Row label="Commit">
          <span className="font-mono text-[12.5px]">
            {binding.commitSha ? shortSHA(binding.commitSha) : "Unknown"}
          </span>
        </Row>
        <Row label="Deployed">
          {formatDate(binding.updatedAt)}
          {latest?.plan.decidedAt && ", approved plan"}
        </Row>
        {(c.framework || c.image) && (
          <Row label="Built">
            {[c.framework, c.image && `from ${c.image}`]
              .filter(Boolean)
              .join(" · ")}
          </Row>
        )}
        <Row label="Runs on">
          <CloudLink
            binding={binding}
            className="flex items-center gap-1.5 underline-offset-4 hover:underline"
          >
            {product && <ProductIcon product={product} size="sm" />}
            {[cloudLabel(binding.provider), binding.region, sizeLabel(c)]
              .filter(Boolean)
              .join(" · ")}
          </CloudLink>
        </Row>
        {c.healthPath && (
          <Row label="Health check">
            <span className="font-mono text-[12.5px]">GET {c.healthPath}</span>
          </Row>
        )}
        {state === "drifted" && binding.driftMessage && (
          <Row label="Drift">
            <span className="text-weld-ink">{binding.driftMessage}</span>
          </Row>
        )}
      </dl>
      <div className="flex flex-wrap gap-2 border-t bg-muted/30 px-4 py-2.5">
        {latest && (
          <Link
            to="/deployments/$id"
            params={{ id: latest.id }}
            className={outlineButton}
          >
            Open deployment
          </Link>
        )}
        <CloudLink binding={binding} className={outlineButton}>
          Open cloud account
        </CloudLink>
      </div>
    </section>
  )
}

function NotHere({
  row,
  env,
  mayDeploy,
  onDeploy,
}: {
  row: ServiceRow
  env: Environment
  mayDeploy: boolean
  onDeploy: () => void
}) {
  const elsewhere = Object.entries(row.deployed).find(([, b]) => b)
  return (
    <section className="grid gap-3 rounded-xl border border-dashed px-5 py-5">
      <h2 className="text-[15px] font-semibold">
        {row.service.name} isn&apos;t in {env.name} yet
      </h2>
      <p className="max-w-[560px] text-[13px] leading-relaxed text-muted-foreground">
        {row.service.deployable
          ? `A deploy to ${env.name} plans it with everything else the manifest asks for.`
          : `${kindLabel(row.service.kind)}s don't deploy on their own.`}
        {elsewhere?.[1]?.commitSha &&
          ` ${elsewhere[0]} runs ${shortSHA(elsewhere[1].commitSha)}, if you want the same commit here.`}
      </p>
      {mayDeploy && row.service.deployable && (
        <div>
          <Button onClick={onDeploy}>Plan a deploy to {env.name}</Button>
        </div>
      )}
    </section>
  )
}

/**
 * runTone names how a run left the service: the newest that succeeded
 * with what's running now is Live, older successes were Replaced.
 */
function runPill(r: Run, binding: Binding | undefined, liveID?: string) {
  if (r.status === "succeeded")
    return r.id === liveID ? (
      <Pill tone="live">Live</Pill>
    ) : (
      <Pill tone="idle">{binding ? "Replaced" : "Succeeded"}</Pill>
    )
  if (r.status === "failed") return <Pill tone="failed">Failed</Pill>
  if (r.status === "running") return <Pill tone="flight">Deploying</Pill>
  if (r.status === "waiting") return <Pill tone="waiting">Needs approval</Pill>
  return <Pill tone="idle">{runStatusText[r.status]}</Pill>
}

function RunList({
  title,
  runs,
  binding,
  empty,
  more,
  showEnvironment,
}: {
  title: string
  runs: Run[]
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
    <section aria-label={title} className={card}>
      <div className={cardHead}>
        <h2 className="text-sm font-semibold">{title}</h2>
        {more}
      </div>
      {runs.length === 0 ? (
        <p className="px-4 py-3.5 text-[13px] text-muted-foreground">{empty}</p>
      ) : (
        <ul>
          {runs.map((r) => (
            <li key={r.id} className="border-b last:border-b-0">
              <Link
                to="/deployments/$id"
                params={{ id: r.id }}
                className="grid grid-cols-[120px_80px_minmax(0,1fr)_auto] items-center gap-3 px-4 py-2.5 text-[13px] transition-colors hover:bg-muted/30"
              >
                {runPill(r, binding, liveID)}
                <span className="font-mono text-[12.5px]">
                  {r.plan.sources
                    .map((s) => shortSHA(s.commitSha))
                    .filter(Boolean)
                    .join(", ")}
                </span>
                <span className="truncate text-muted-foreground">
                  {showEnvironment ? (
                    <EnvBadge env={r.environment} />
                  ) : r.status === "succeeded" ? (
                    ""
                  ) : (
                    r.phase
                  )}
                </span>
                <span className="text-right text-muted-foreground">
                  {formatDate(r.createdAt)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function Dot({ tone }: { tone: "live" | "waiting" | "idle" | "failed" }) {
  return (
    <span
      className={cn(
        "size-[7px] shrink-0 rounded-full",
        tone === "live" && "bg-overdye",
        tone === "waiting" && "bg-weld",
        tone === "failed" && "bg-failed",
        tone === "idle" && "bg-warp"
      )}
    />
  )
}

function Domains({
  projectId,
  row,
  env,
  routes,
  mayRoute,
}: {
  projectId: string
  row: ServiceRow
  env: Environment
  routes: CloudinfraRoute[]
  mayRoute: boolean
}) {
  const binding = row.deployed[env.name]
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<CloudinfraRoute>()
  const ok = (status: string) => /active|ready|live|ok|serving/i.test(status)
  const name = row.service.name
  return (
    <section aria-label="Domains" className={card}>
      <div className={cardHead}>
        <h2 className="text-sm font-semibold">Domains</h2>
        <span className="flex items-center gap-3">
          <Link
            to="/domains"
            className="text-[12.5px] text-muted-foreground hover:text-foreground hover:underline"
          >
            Manage domains
          </Link>
          {mayRoute && (
            <button
              type="button"
              onClick={() => setAdding(true)}
              className="text-[12.5px] font-medium text-primary hover:underline"
            >
              Add a domain
            </button>
          )}
        </span>
      </div>
      {routes.length === 0 && !binding?.url ? (
        <p className="px-4 py-3 text-[12.5px] text-muted-foreground">
          {binding
            ? `None routed to it in ${env.name}.`
            : `None in ${env.name}. It gets a generated address when it deploys.`}
        </p>
      ) : (
        <ul>
          {routes.map((r) => (
            <li
              key={r.id}
              className="grid gap-0.5 border-b px-4 py-2.5 last:border-b-0"
            >
              <span className="flex min-w-0 items-center gap-2">
                <Dot tone={ok(r.status) ? "live" : "waiting"} />
                <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] font-medium">
                  {r.input.hostname}
                </span>
                {mayRoute && (
                  <button
                    type="button"
                    aria-label={`Remove ${r.input.hostname}`}
                    onClick={() => setRemoving(r)}
                    className="shrink-0 text-xs text-muted-foreground hover:text-destructive hover:underline"
                  >
                    Remove
                  </button>
                )}
              </span>
              <span className="pl-[15px] text-xs text-muted-foreground">
                Custom · {r.status ? r.status.replaceAll("_", " ") : "routed"}
              </span>
            </li>
          ))}
          {binding?.url && (
            <li className="grid gap-0.5 px-4 py-2.5">
              <span className="flex min-w-0 items-center gap-2">
                <Dot tone={stateOf(binding) === "live" ? "live" : "waiting"} />
                <ServiceAddress
                  address={binding.url}
                  className="truncate font-mono text-[12.5px] font-medium"
                />
              </span>
              <span className="pl-[15px] text-xs text-muted-foreground">
                Generated by {cloudLabel(binding.provider)}
              </span>
            </li>
          )}
        </ul>
      )}
      <AddHostnameDialog
        projectId={projectId}
        environment={env}
        service={name}
        ports={row.service.ports ?? []}
        deployed={!!binding}
        open={adding}
        onOpenChange={setAdding}
      />
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
      // not in the mock
      toast.success(`Loomplane is removing ${host} from ${service}.`)
      onClose()
    } catch {
      // useRouteLifecycle words the refusal, shown below.
    }
  }
  return (
    <AlertDialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {host}?</AlertDialogTitle>
          {/* not in the mock */}
          <AlertDialogDescription>
            Loomplane removes its DNS record and stops sending its traffic to{" "}
            {service}. You can add it again later.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {lifecycle.error && (
          <p role="alert" className="text-[12.5px] text-destructive">
            {lifecycle.error}
          </p>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={lifecycle.pending}>
            Cancel
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={lifecycle.pending}
            onClick={() => void remove()}
          >
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
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
    (n) => !resourceRows.some((r) => r.kind === n.resource.kind)
  )
  const empty = resourceRows.length === 0 && unmet.length === 0 && !binding
  return (
    <section aria-label="What it uses" className={card}>
      <div className={cardHead}>
        <h2 className="text-sm font-semibold">Uses</h2>
      </div>
      {empty ? (
        <p className="px-4 py-3 text-[12.5px] text-muted-foreground">
          Nothing found: its code needs no resources.
        </p>
      ) : (
        <ul>
          {resourceRows.map((r) => (
            <li key={r.name} className="border-b last:border-b-0">
              <ResourceLink
                project={project}
                env={env}
                name={r.rb ? r.name : undefined}
                className="flex items-center gap-2.5 px-4 py-2.5"
              >
                {r.kind && products[r.kind] ? (
                  <ProductIcon product={products[r.kind]} />
                ) : (
                  <DatabaseIcon className="size-4 text-muted-foreground" />
                )}
                <span className="grid min-w-0">
                  <span className="text-[13px] font-semibold">{r.name}</span>
                  <span className="truncate text-xs text-muted-foreground">
                    {[
                      r.kind &&
                        `${resourceLabel(r.kind)}${r.version ? ` ${r.version}` : ""}`,
                    ]
                      .filter(Boolean)
                      .join("")}
                    {r.kind ? " · as " : "As "}
                    <span className="font-mono">{r.key}</span>
                  </span>
                </span>
              </ResourceLink>
            </li>
          ))}
          {unmet
            .filter((n) => !linked.has(n.resource.kind))
            .map((n) => (
              <li key={n.resource.id} className="border-b last:border-b-0">
                <div className="flex items-center gap-2.5 px-4 py-2.5">
                  {products[n.resource.kind] ? (
                    <ProductIcon product={products[n.resource.kind]} />
                  ) : (
                    <DatabaseIcon className="size-4 text-muted-foreground" />
                  )}
                  <span className="grid min-w-0">
                    <span className="text-[13px] font-semibold">
                      {resourceLabel(n.resource.kind)}
                    </span>
                    <span className="truncate text-xs text-muted-foreground">
                      {n.resource.needsConfirmation
                        ? "Found in its code, not confirmed"
                        : `Found in its code, not in ${env.name} yet`}
                      {n.via ? `, through ${n.via}` : ""}
                    </span>
                  </span>
                </div>
              </li>
            ))}
          {binding && (
            <li>
              <CloudLink
                binding={binding}
                className="flex items-center gap-2.5 px-4 py-2.5 transition-colors hover:bg-muted/30"
              >
                {product ? (
                  <ProductIcon product={product} />
                ) : (
                  <BoxIcon className="size-4 text-muted-foreground" />
                )}
                <span className="grid min-w-0">
                  <span className="text-[13px] font-semibold">
                    {cloudLabel(binding.provider)} app{" "}
                    <span className="font-mono font-medium">
                      {binding.name}
                    </span>
                  </span>
                  <span className="truncate text-xs text-muted-foreground">
                    {[
                      binding.externalId || binding.physicalId,
                      sizeLabel(deployedConfig(binding)),
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </span>
              </CloudLink>
            </li>
          )}
        </ul>
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
 * content plainly when nothing of it runs there to open.
 */
function ResourceLink({
  project,
  env,
  name,
  className,
  children,
}: {
  project: Project
  env: Environment
  name?: string
  className?: string
  children: React.ReactNode
}) {
  if (!name) return <div className={className}>{children}</div>
  return (
    <Link
      to="/projects/$id/catalog/$resource"
      params={{ id: project.id, resource: name }}
      search={{ environment: env.id }}
      className={cn(className, "transition-colors hover:bg-muted/30")}
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
  const depends = deployedConfig(binding).env
  const callers = callersOf(name, bindings)
  const status = (b?: Binding) =>
    !b ? "idle" : stateOf(b) === "live" ? "live" : "waiting"
  const find = (subject: string, from: string) =>
    bindings.find((b) => b.subject === subject && b.name === from)
  return (
    <section aria-label="Connections" className={card}>
      <div className="grid gap-0.5 border-b px-4 py-3">
        <h2 className="text-sm font-semibold">Connections</h2>
        <span className="text-xs text-muted-foreground">
          From what&apos;s deployed in {env.name}.
        </span>
      </div>
      <div className="grid gap-2 px-4 py-3 text-[13px]">
        <span className="text-[11.5px] font-semibold tracking-wide text-muted-foreground uppercase">
          Depends on
        </span>
        {depends.length === 0 ? (
          <span className="text-[12.5px] text-muted-foreground">
            {binding ? "Nothing." : `Not deployed in ${env.name}.`}
          </span>
        ) : (
          depends.map((l) => (
            <span key={l.key} className="flex items-center gap-2">
              <Dot tone={status(find(l.subject, l.from))} />
              {l.subject === "service" ? (
                <Link
                  to="/projects/$id/services/$service"
                  params={{ id: project.id, service: l.from }}
                  search={{ environment: env.name }}
                  className="font-medium hover:underline"
                >
                  {l.from}
                </Link>
              ) : (
                <ResourceLink
                  project={project}
                  env={env}
                  name={find(l.subject, l.from) ? l.from : undefined}
                  className="font-medium hover:underline"
                >
                  {l.from}
                </ResourceLink>
              )}
              <span className="font-mono text-[11.5px] text-muted-foreground">
                {l.key}
              </span>
            </span>
          ))
        )}
      </div>
      <div className="grid gap-2 border-t px-4 py-3 text-[13px]">
        <span className="text-[11.5px] font-semibold tracking-wide text-muted-foreground uppercase">
          Called by
        </span>
        {callers.length === 0 ? (
          <span className="text-[12.5px] text-muted-foreground">
            Nothing deployed in {env.name} links to it.
          </span>
        ) : (
          callers.map((c) => (
            <span key={c.binding.id} className="flex items-center gap-2">
              <Dot tone={status(c.binding)} />
              <Link
                to="/projects/$id/services/$service"
                params={{ id: project.id, service: c.binding.name }}
                search={{ environment: env.name }}
                className="font-medium hover:underline"
              >
                {c.binding.name}
              </Link>
              <span className="font-mono text-[11.5px] text-muted-foreground">
                {c.key}
              </span>
            </span>
          ))
        )}
      </div>
    </section>
  )
}

function Variables({ row, env }: { row: ServiceRow; env: Environment }) {
  const binding = row.deployed[env.name]
  const links = deployedConfig(binding).env
  return (
    <div className="grid gap-2.5">
      <span className="text-[12.5px] text-muted-foreground">
        The variables Loomplane links into {row.service.name} in {env.name}.
        Values you set yourself are on the project&apos;s Variables tab.
      </span>
      <div className={card}>
        <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-3 border-b bg-muted/40 px-4 py-2 text-[11.5px] font-medium tracking-wide text-muted-foreground uppercase">
          <span>Key</span>
          <span>From</span>
        </div>
        {links.length === 0 ? (
          <p className="px-4 py-3.5 text-[13px] text-muted-foreground">
            {binding
              ? "None: it links to nothing else."
              : `Not deployed in ${env.name}, so nothing is linked yet.`}
          </p>
        ) : (
          links.map((l) => (
            <div
              key={l.key}
              className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-3 border-b px-4 py-2.5 text-[13px] last:border-b-0"
            >
              <span className="font-mono text-[12.5px] font-medium">
                {l.key}
              </span>
              <span className="text-[12.5px] text-muted-foreground">
                {l.from}
                {l.subject === "resource" ? `, in ${env.name}` : " (service)"}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
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
  const { can } = useOrganization()
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
  return (
    <section className="grid gap-2.5 rounded-xl px-4 py-3.5 ring-1 ring-border">
      <div className="grid gap-1">
        <h2 className="text-sm font-semibold">Environments</h2>
        <p className="text-[12.5px] text-muted-foreground">
          Where {name} deploys. Leaving one out removes it there at the next
          deploy.
        </p>
      </div>
      {current.status === "error" ? (
        <QueryError
          error={current.error}
          fallback="The manifest couldn't be loaded."
          retry={current.refetch}
        />
      ) : !current.data ? (
        <Skeleton className="h-14 w-full rounded-lg" />
      ) : !inManifest ? (
        <p className="text-[12.5px] text-muted-foreground">
          The manifest doesn&apos;t deploy {name} yet. Deploying it adds it, in
          every environment.
        </p>
      ) : (
        environments.map((e) => {
          const on = enabledIn(services[name], e.name)
          const b = row.deployed[e.name]
          return (
            <label
              key={e.id}
              className="flex cursor-pointer items-center gap-2.5 text-[13px]"
            >
              <Checkbox
                checked={on}
                disabled={!can("project:update") || pending !== ""}
                onCheckedChange={(v) => flip(e.name, !!v)}
                aria-label={`Deploy ${name} to ${e.name}`}
              />
              <span className="font-medium">{e.name}</span>
              <span className="text-muted-foreground">
                {pending === e.name
                  ? "Saving"
                  : !on
                    ? b
                      ? "Left out, removed at the next deploy"
                      : "Left out"
                    : b
                      ? stateOf(b) === "live"
                        ? "Live"
                        : "Changed outside Loomplane"
                      : "Not deployed yet"}
              </span>
            </label>
          )
        })
      )}
    </section>
  )
}
