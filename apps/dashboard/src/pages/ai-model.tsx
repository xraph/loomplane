// One deployed model: how it's doing and what it costs, what a service
// that links to it writes to call it, and moving or removing it. A model
// can run its own copy in several environments of its project; the page
// shows one of them, with links to the others.
import { LockIcon, PlusIcon, SearchIcon } from "lucide-react"
import { useState } from "react"

import { statusOf } from "@/client/errors"
import {
  useAiModelDeploymentsGet,
  useEnvironmentsList,
  useProjectsGet,
} from "@/client/forge/loomplane/hooks"
import type { Environment, ModelsStatus } from "@/client/forge/loomplane/types"
import { useQuery, useRefetchWhile } from "@/client/hooks"
import { DeployModelDialog } from "@/components/ai/models/deploy-model-dialog"
import { ModelExample } from "@/components/ai/models/model-example"
import {
  ModelOverview,
  type ModelNotice,
} from "@/components/ai/models/model-overview"
import { ModelSettings } from "@/components/ai/models/model-settings"
import { ModelsIcon } from "@/components/icons"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { StatusPill } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { modelState } from "@/lib/models"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

export type ModelTab = "overview" | "example" | "settings"

// States that change on their own, so the page asks again now and then.
const moving = new Set([
  "planned",
  "deploying",
  "waiting_for_server",
  "needs_attention",
])

export function AIModel({
  projectId,
  resource,
  environmentId,
  tab,
  onTab,
}: {
  projectId: string
  resource: string
  environmentId: string
  tab: ModelTab
  onTab: (tab: ModelTab) => void
}) {
  const project = useQuery(useProjectsGet, { path: { id: projectId } })
  const environments = useQuery(useEnvironmentsList, {
    path: { id: projectId },
  })
  const environment = environments.data?.environments.find(
    (e) => e.id === environmentId
  )
  // What a change, a cancel or a removal said. It stays when the model is
  // gone, since that's often when it matters most.
  const [notice, setNotice] = useState<ModelNotice>()
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Models", to: "/ai/models" },
          { label: project.data?.name ?? "Project" },
          { label: resource },
        ]}
      />
      <Page className="gap-6">
        {notice && (
          <Notice notice={notice} onClose={() => setNotice(undefined)} />
        )}
        {!environmentId ? (
          <PickEnvironment
            projectId={projectId}
            resource={resource}
            environments={environments.data?.environments ?? []}
          />
        ) : (
          <Model
            projectId={projectId}
            resource={resource}
            environmentId={environmentId}
            environmentName={environment?.name}
            production={environment?.kind === "production"}
            // Who may change it depends on the environment's kind, so the
            // page waits for it, unless it can't be read.
            environmentsKnown={environments.status !== "pending"}
            environments={environments.data?.environments ?? []}
            projectName={project.data?.name}
            tab={tab}
            onTab={onTab}
            notify={setNotice}
          />
        )}
      </Page>
    </>
  )
}

function Model({
  projectId,
  resource,
  environmentId,
  environmentName,
  production,
  environmentsKnown,
  environments,
  projectName,
  tab,
  onTab,
  notify,
}: {
  projectId: string
  resource: string
  environmentId: string
  environmentName?: string
  production: boolean
  environmentsKnown: boolean
  environments: Environment[]
  projectName?: string
  tab: ModelTab
  onTab: (tab: ModelTab) => void
  notify: (n: ModelNotice | undefined) => void
}) {
  const status = useQuery(useAiModelDeploymentsGet, {
    path: { projectId, environmentId, resource },
  })
  useRefetchWhile(
    !!status.data && moving.has(status.data.state),
    status.refetch,
    5000
  )
  if (status.status === "error")
    return <ModelError error={status.error} retry={status.refetch} />
  if (!status.data || !environmentsKnown)
    return (
      <div data-testid="model-loading" className="grid gap-4">
        <Skeleton className="h-4 w-40" />
        <Skeleton className="h-7 w-80 max-w-full" />
        <Skeleton className="h-9 w-72 max-w-full" />
        <Skeleton className="h-40 w-full rounded-xl" />
      </div>
    )
  const s = status.data
  const env = s.environments[0]?.environment || environmentName || ""
  const state = modelState(s.state, !!s.internalUrl)
  const props = {
    status: s,
    projectId,
    environmentId,
    production,
    notify,
    onTab,
  }
  return (
    <>
      <PageHeading
        eyebrow={
          <p className="text-[12.5px] font-medium text-muted-foreground">
            {projectName ?? "Project"} / {env}
          </p>
        }
        title={
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <span className="min-w-0 break-all">{resource}</span>
            <StatusPill tone={state.tone}>{state.label}</StatusPill>
          </span>
        }
        description={
          s.entry.id ? (
            <>
              {s.entry.title || s.entry.id}
              {s.entry.title && (
                <span className="ml-1.5 font-mono text-[12px] break-all">
                  {s.entry.id}
                </span>
              )}
            </>
          ) : undefined
        }
        actions={
          <DeployElsewhere
            status={s}
            projectId={projectId}
            environments={environments}
          />
        }
      >
        <EnvironmentSwitcher
          status={s}
          projectId={projectId}
          resource={resource}
          environmentId={environmentId}
          environments={environments}
        />
      </PageHeading>
      <Tabs
        value={tab}
        onValueChange={(v) => onTab(v as ModelTab)}
        className="min-w-0 gap-5"
      >
        <div className="max-w-full overflow-x-auto border-b">
          <TabsList variant="line" aria-label="Model">
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="example">Example</TabsTrigger>
            <TabsTrigger value="settings">Settings</TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="overview" className="min-w-0">
          <ModelOverview {...props} />
        </TabsContent>
        <TabsContent value="example" className="min-w-0">
          <ModelExample {...props} />
        </TabsContent>
        <TabsContent value="settings" className="min-w-0">
          <ModelSettings {...props} />
        </TabsContent>
      </Tabs>
    </>
  )
}

/**
 * EnvironmentSwitcher links each environment the model runs its own copy
 * in, in the project's order, when there's more than one.
 */
function EnvironmentSwitcher({
  status,
  projectId,
  resource,
  environmentId,
  environments,
}: {
  status: ModelsStatus
  projectId: string
  resource: string
  environmentId: string
  environments: Environment[]
}) {
  if (status.environments.length < 2) return null
  const order = (id: string) => {
    const i = environments.findIndex((e) => e.id === id)
    return i < 0 ? environments.length : i
  }
  const copies = [...status.environments].sort(
    (a, b) => order(a.environmentId) - order(b.environmentId)
  )
  return (
    <nav
      aria-label="Environments"
      className="mt-2 flex max-w-full flex-wrap items-center gap-1.5"
    >
      {copies.map((e) => {
        const current = e.environmentId === environmentId
        return (
          <Link
            key={e.environmentId}
            to="/projects/$id/ai/models/$resource"
            params={{ id: projectId, resource }}
            search={{ environment: e.environmentId }}
            aria-current={current ? "page" : undefined}
            className={cn(
              "inline-flex h-7 max-w-full items-center rounded-full px-3 text-[12.5px] font-medium ring-1 transition-colors",
              current
                ? "bg-foreground text-background ring-foreground"
                : "text-muted-foreground ring-border hover:bg-muted hover:text-foreground"
            )}
          >
            <span className="truncate">{e.environment}</span>
          </Link>
        )
      })}
    </nav>
  )
}

/**
 * DeployElsewhere adds another of the project's environments to the model,
 * while there's one it doesn't run in that the caller may deploy models to.
 */
function DeployElsewhere({
  status,
  projectId,
  environments,
}: {
  status: ModelsStatus
  projectId: string
  environments: Environment[]
}) {
  const { can } = useOrganization()
  const [open, setOpen] = useState(false)
  const runsIn = status.environments.map((e) => e.environmentId)
  // One the model doesn't run in yet that the caller may deploy to:
  // production takes ai:manage, the others ai:edit.
  const left = environments.some(
    (e) =>
      !runsIn.includes(e.id) &&
      can(e.kind === "production" ? "ai:manage" : "ai:edit")
  )
  if (!status.entry.id || !left) return null
  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        <PlusIcon aria-hidden="true" />
        Deploy to another environment
      </Button>
      {open && (
        <DeployModelDialog
          entryId={status.entry.id}
          projectId={projectId}
          resource={status.resource}
          runsIn={runsIn}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

function Notice({
  notice,
  onClose,
}: {
  notice: ModelNotice
  onClose: () => void
}) {
  return (
    <div
      role="status"
      className={cn(
        "grid gap-1.5 rounded-xl p-4 ring-1",
        notice.tone === "weld"
          ? "bg-weld-tint text-weld-ink ring-weld/30"
          : "bg-card ring-border"
      )}
    >
      {notice.title && (
        <p className="text-[13.5px] font-semibold">{notice.title}</p>
      )}
      <p className="text-[13.5px] text-pretty">{notice.text}</p>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        {notice.planId && (
          <Link
            to="/deployments/$id"
            params={{ id: notice.planId }}
            className="text-[13px] font-medium text-primary hover:underline"
          >
            Open the plan
          </Link>
        )}
        {notice.infrastructure && (
          <Link
            to="/infrastructure"
            className="text-[13px] font-medium text-primary hover:underline"
          >
            Open Infrastructure
          </Link>
        )}
        <button
          type="button"
          onClick={onClose}
          className="text-[13px] text-muted-foreground hover:text-foreground"
        >
          Dismiss
        </button>
      </div>
    </div>
  )
}

function PickEnvironment({
  projectId,
  resource,
  environments,
}: {
  projectId: string
  resource: string
  environments: { id: string; name: string }[]
}) {
  return (
    <ZeroState preview={false}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <ModelsIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>Which environment?</ZeroStateTitle>
        <ZeroStateDescription>
          Each environment runs its own copy of {resource}. Pick the one to
          open.
        </ZeroStateDescription>
      </ZeroStateHeader>
      <ZeroStateContent>
        <div className="flex flex-wrap justify-center gap-2">
          {environments.map((e) => (
            <Button
              key={e.id}
              variant="outline"
              nativeButton={false}
              render={
                <Link
                  to="/projects/$id/ai/models/$resource"
                  params={{ id: projectId, resource }}
                  search={{ environment: e.id }}
                />
              }
            >
              {e.name}
            </Button>
          ))}
        </div>
      </ZeroStateContent>
    </ZeroState>
  )
}

function ModelError({
  error,
  retry,
}: {
  error: unknown
  retry: () => Promise<unknown>
}) {
  const status = statusOf(error)
  if (status === 403)
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
  if (status === 404)
    return (
      <ZeroState preview={false}>
        <ZeroStateHeader>
          <ZeroStateMedia variant="icon">
            <SearchIcon />
          </ZeroStateMedia>
          <ZeroStateTitle>No such model here</ZeroStateTitle>
          <ZeroStateDescription>
            This environment doesn&apos;t run a model by that name. It may have
            been removed, or it runs in another environment.
          </ZeroStateDescription>
        </ZeroStateHeader>
        <ZeroStateContent>
          <Button
            variant="outline"
            nativeButton={false}
            render={<Link to="/ai/models" search={{ tab: "deployed" }} />}
          >
            See deployed models
          </Button>
        </ZeroStateContent>
      </ZeroState>
    )
  return (
    <QueryError
      error={error}
      fallback="The model couldn't be loaded."
      retry={retry}
    />
  )
}
