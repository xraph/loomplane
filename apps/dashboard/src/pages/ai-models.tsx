// Models: the registry of open models Loomplane can run on your own
// servers, any Hugging Face model you paste, and the models the
// workspace's projects run now with what their GPUs cost.
import { InfoIcon, LockIcon, SearchIcon } from "lucide-react"
import { useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useAiModelDeploymentsList,
  useAiModelsRegistry,
  useProjectsList,
} from "@/client/forge/loomplane/hooks"
import type {
  EntrySummary,
  ModelsStatus,
  RegistryList,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { HFInspect } from "@/components/ai/models/hf-inspect"
import { ModelCard } from "@/components/ai/models/model-card"
import {
  ModelFilters,
  type LicenseOption,
} from "@/components/ai/models/model-filters"
import { ModelsIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Tabs,
  TabsContent,
  TabsCount,
  TabsList,
  TabsTrigger,
} from "@/components/ui/tabs"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { acceleratorLabel } from "@/lib/gpu"
import {
  costLabel,
  deployedSummary,
  monthLabel,
  registryQuery,
  type ModelsSearch,
} from "@/lib/models"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

const description =
  "Open models Loomplane runs on your own servers and clusters. Each one says whether Loomplane's qualification served it on a GPU or only did the memory math."

const noDeploy = "Ask a workspace admin for permission to deploy models."

export function AIModels({
  search,
  onSearch,
  onDeploy,
}: {
  search: ModelsSearch
  onSearch: (next: ModelsSearch) => void
  /** Opens the deploy dialog for a registry entry or a pasted repo. */
  onDeploy?: (entry: EntrySummary) => void
}) {
  const deployed = useQuery(useAiModelDeploymentsList)
  const tab = search.tab ?? "registry"
  return (
    <>
      <SiteHeader crumbs={[{ label: "Models" }]} />
      <Page className="gap-6">
        <PageHeading title="Models" description={description} />
        <Tabs
          value={tab}
          onValueChange={(v) =>
            onSearch(
              v === "deployed"
                ? { ...search, tab: "deployed" }
                : { ...search, tab: undefined }
            )
          }
          className="gap-5"
        >
          <TabsList variant="line">
            <TabsTrigger value="registry">Registry</TabsTrigger>
            <TabsTrigger value="deployed">
              Deployed
              {deployed.data && (
                <TabsCount>{deployed.data.models.length}</TabsCount>
              )}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="registry">
            <Registry search={search} onSearch={onSearch} onDeploy={onDeploy} />
          </TabsContent>
          <TabsContent value="deployed">
            <Deployed
              onBrowse={() => onSearch({ ...search, tab: undefined })}
            />
          </TabsContent>
        </Tabs>
      </Page>
    </>
  )
}

function Restricted() {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <LockIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>Models are restricted</ZeroStateTitle>
        <ZeroStateDescription>
          Ask a workspace admin for access to AI in this workspace.
        </ZeroStateDescription>
      </ZeroStateHeader>
    </ZeroState>
  )
}

/* ------------------------------------------------------------------------ */
/* Registry                                                                   */

function Registry({
  search,
  onSearch,
  onDeploy,
}: {
  search: ModelsSearch
  onSearch: (next: ModelsSearch) => void
  onDeploy?: (entry: EntrySummary) => void
}) {
  const { can } = useOrganization()
  // The whole registry, for the license list; with no filters set it's the
  // same request as the list below.
  const everything = useQuery(useAiModelsRegistry, { query: {} })
  const list = useQuery(useAiModelsRegistry, { query: registryQuery(search) })
  // While a new filter loads, the last list stays rather than flashing a
  // skeleton.
  const [last, setLast] = useState<RegistryList>()
  if (list.data && list.data !== last) setLast(list.data)
  const data = list.data ?? last
  const editor = can("ai:edit")
  const blocked = editor ? undefined : noDeploy

  if (list.status === "error" && statusOf(list.error) === 403)
    return <Restricted />

  const filtered = Object.keys(registryQuery(search)).length > 0
  return (
    <div className="grid gap-5">
      {editor && <HFInspect onDeploy={onDeploy} />}
      <ModelFilters
        search={search}
        counts={data?.counts}
        licenses={licensesOf(everything.data?.entries ?? [])}
        onChange={(next) => onSearch({ ...next, tab: undefined })}
      />
      {!editor && (
        <p className="flex items-center gap-1.5 text-[12.5px] text-muted-foreground">
          <InfoIcon aria-hidden="true" className="size-3.5 shrink-0" />
          {noDeploy}
        </p>
      )}
      {data?.fitsNotice && (
        <p
          role="status"
          className="flex items-center gap-1.5 text-[12.5px] text-muted-foreground"
        >
          <InfoIcon aria-hidden="true" className="size-3.5 shrink-0" />
          <span>
            {data.fitsNotice} The list shows every model, whether it fits or
            not.
          </span>
        </p>
      )}
      {list.status === "error" ? (
        <QueryError
          error={list.error}
          fallback="The model registry couldn't be loaded."
          retry={list.refetch}
        />
      ) : !data ? (
        <div
          data-testid="models-loading"
          className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3"
        >
          {[0, 1, 2, 3, 4, 5].map((i) => (
            <Skeleton key={i} className="h-[238px] rounded-xl" />
          ))}
        </div>
      ) : data.entries.length === 0 ? (
        <ZeroState>
          <ZeroStateHeader>
            <ZeroStateMedia variant="icon">
              <SearchIcon />
            </ZeroStateMedia>
            <ZeroStateTitle>No models match</ZeroStateTitle>
            <ZeroStateDescription>
              {filtered
                ? "Try another name, or turn some filters off."
                : "The registry has no models to show."}
            </ZeroStateDescription>
          </ZeroStateHeader>
          {filtered && (
            <ZeroStateContent>
              <Button variant="outline" onClick={() => onSearch({})}>
                Clear filters
              </Button>
            </ZeroStateContent>
          )}
        </ZeroState>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {data.entries.map((e) => (
            <ModelCard
              key={e.id}
              entry={e}
              linked
              onDeploy={onDeploy ? () => onDeploy(e) : undefined}
              deployBlocked={blocked}
            />
          ))}
        </div>
      )}
    </div>
  )
}

/** licensesOf lists the registry's licenses, each once, by title. */
function licensesOf(entries: EntrySummary[]): LicenseOption[] {
  const seen = new Map<string, LicenseOption>()
  for (const e of entries) {
    if (!seen.has(e.license.id))
      seen.set(e.license.id, { id: e.license.id, title: e.license.title })
  }
  return [...seen.values()].sort((a, b) => a.title.localeCompare(b.title))
}

/* ------------------------------------------------------------------------ */
/* Deployed                                                                   */

const states: Record<string, { label: string; tone: Tone }> = {
  ready: { label: "Ready", tone: "live" },
  deploying: { label: "Deploying", tone: "flight" },
  planned: { label: "Planned", tone: "flight" },
  waiting_for_server: { label: "Waiting for a server", tone: "waiting" },
  needs_attention: { label: "Needs attention", tone: "failed" },
  failed: { label: "Failed", tone: "failed" },
  absent: { label: "Not deployed", tone: "quiet" },
}

function stateOf(state: string): { label: string; tone: Tone } {
  return states[state] ?? { label: state, tone: "quiet" }
}

function Deployed({ onBrowse }: { onBrowse: () => void }) {
  const list = useQuery(useAiModelDeploymentsList)
  const projects = useQuery(useProjectsList)
  if (list.status === "error") {
    if (statusOf(list.error) === 403) return <Restricted />
    return (
      <QueryError
        error={list.error}
        fallback="The deployed models couldn't be loaded."
        retry={list.refetch}
      />
    )
  }
  if (!list.data)
    return (
      <div data-testid="deployed-loading" className="grid gap-2">
        <Skeleton className="h-5 w-72 max-w-full" />
        <Skeleton className="h-44 w-full rounded-[14px]" />
      </div>
    )
  const models = list.data.models.filter((m): m is ModelsStatus => !!m)
  if (models.length === 0)
    return (
      <ZeroState>
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <ModelsIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>No models deployed yet</ZeroStateTitle>
          <ZeroStateDescription>
            Deploy one from the registry. It runs on a server or cluster you
            connected, and its GPU cost shows here.
          </ZeroStateDescription>
        </ZeroStateHeader>
        <ZeroStateContent>
          <Button variant="outline" onClick={onBrowse}>
            Browse the registry
          </Button>
        </ZeroStateContent>
      </ZeroState>
    )
  const names = new Map(
    (projects.data?.projects ?? []).map((p) => [p.id, p.name])
  )
  const summary = deployedSummary(models)
  return (
    <div className="grid gap-3">
      {summary && (
        <p className="text-[13.5px] text-pretty" data-testid="deployed-summary">
          {summary}
        </p>
      )}
      <div className="overflow-hidden rounded-[14px] bg-card ring-1 ring-border">
        <div
          aria-hidden="true"
          className="hidden grid-cols-[minmax(0,2fr)_minmax(0,1.5fr)_minmax(0,1.3fr)_8.5rem_7rem_7rem] gap-4 border-b bg-subtle px-4 py-2 text-[11.5px] font-medium text-muted-foreground lg:grid"
        >
          <span>Model</span>
          <span>Project and environment</span>
          <span>GPU</span>
          <span>State</span>
          <span className="text-right">Per hour</span>
          <span className="text-right">This month</span>
        </div>
        <ul aria-label="Deployed models">
          {models.map((m, i) => (
            <DeployedRow
              key={`${m.projectId}/${m.environments[0]?.environmentId ?? ""}/${m.resource}/${i}`}
              model={m}
              project={names.get(m.projectId)}
            />
          ))}
        </ul>
      </div>
    </div>
  )
}

function DeployedRow({
  model: m,
  project,
}: {
  model: ModelsStatus
  project?: string
}) {
  const state = stateOf(m.state)
  const cost = costLabel(m.cost, m.connectionName)
  const environment = m.environments[0]?.environment ?? ""
  const environmentId = m.environments[0]?.environmentId
  // Every row names its project and resource, a model added to the
  // manifest by hand too.
  const where = `${project ?? "Project"} / ${environment}${
    m.deployment ? "" : ", added to the manifest by hand"
  }`
  const title = m.entry.title || m.resource || "Model"
  // Why it failed and what to try, in full on hover.
  const trouble = [m.message, m.hint].filter(Boolean).join(" ")
  return (
    <li className="grid gap-x-4 gap-y-1.5 border-t px-4 py-3 first:border-t-0 lg:grid-cols-[minmax(0,2fr)_minmax(0,1.5fr)_minmax(0,1.3fr)_8.5rem_7rem_7rem] lg:items-center">
      <div className="grid min-w-0 gap-0.5">
        {m.projectId && m.resource ? (
          <Link
            to="/projects/$id/catalog/$resource"
            params={{ id: m.projectId, resource: m.resource }}
            search={{ environment: environmentId }}
            className="truncate text-[13.5px] font-medium hover:underline"
          >
            {title}
          </Link>
        ) : (
          <span className="truncate text-[13.5px] font-medium">{title}</span>
        )}
        <span className="truncate font-mono text-[11.5px] text-muted-foreground">
          {m.entry.id}
        </span>
      </div>
      <span
        title={where}
        className="min-w-0 truncate text-[12.5px] text-muted-foreground"
      >
        {where}
      </span>
      <span className="min-w-0 truncate text-[12.5px]">
        {m.gpu ? acceleratorLabel(m.gpu) : "CPU"}
        {m.connectionName && (
          <span className="text-muted-foreground"> on {m.connectionName}</span>
        )}
      </span>
      <div className="grid min-w-0 gap-1">
        <span title={trouble || undefined} className="w-fit">
          <StatusPill tone={state.tone}>{state.label}</StatusPill>
        </span>
        {state.tone === "failed" && m.message && (
          <span
            title={trouble}
            className="line-clamp-2 text-[11.5px] leading-snug text-destructive"
          >
            {m.message}
          </span>
        )}
      </div>
      {cost.kind === "cpu" ? (
        // Never $0.00: a CPU model's cost is its server's.
        <span className="text-[12.5px] text-muted-foreground lg:col-span-2 lg:text-right">
          {cost.detail}
        </span>
      ) : (
        <div className="flex items-center justify-between gap-3 text-[12.5px] lg:contents">
          <Cost label={cost.hourly} detail={cost.detail} kind={cost.kind} />
          <span className="text-right">
            <span className="text-muted-foreground lg:sr-only">
              This month{" "}
            </span>
            <span
              className={
                cost.kind === "unknown"
                  ? "text-muted-foreground"
                  : "font-mono tabular-nums"
              }
            >
              {monthLabel(m)}
            </span>
          </span>
        </div>
      )}
    </li>
  )
}

function Cost({
  label,
  detail,
  kind,
}: {
  label: string
  detail: string
  kind: "known" | "estimate" | "unknown"
}) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-label={`${label}. ${detail}`}
            className={cn(
              "w-fit rounded text-left outline-none focus-visible:ring-3 focus-visible:ring-ring/50 lg:ml-auto lg:text-right",
              kind === "known" || kind === "estimate"
                ? "font-mono tabular-nums"
                : "text-muted-foreground"
            )}
          />
        }
      >
        {label}
        {kind === "estimate" && (
          <span className="ml-1 font-sans text-[11px] text-muted-foreground">
            estimate
          </span>
        )}
      </TooltipTrigger>
      <TooltipContent>{detail}</TooltipContent>
    </Tooltip>
  )
}
