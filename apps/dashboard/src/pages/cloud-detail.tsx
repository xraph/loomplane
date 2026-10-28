import { ChevronLeftIcon } from "lucide-react"
import { useState } from "react"

import { CloudsIcon, ProjectsIcon } from "@/components/icons"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Pool,
  Profile,
  Project,
  ProjectGrant,
  Server,
  Sync,
} from "@/client/forge/loomplane/types"
import { useQuery } from "@/client/hooks"
import { messageOf, statusOf } from "@/client/errors"
import { Capacity } from "@/components/cloud-detail/capacity"
import { Changes } from "@/components/cloud-detail/changes"
import {
  movingSync,
  useScopedSyncs,
  useScopedTargets,
  useScopedUsage,
  type CloudScope,
} from "@/components/cloud-detail/data"
import { Overview, type CloudTab } from "@/components/cloud-detail/overview"
import { Projects } from "@/components/cloud-detail/projects"
import { Settings } from "@/components/cloud-detail/settings"
import { Targets } from "@/components/cloud-detail/targets"
import {
  CloudEmpty,
  CloudError,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { Servers } from "@/components/cloud-targets/servers"
import { Page } from "@/components/page"
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
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"

export function CloudDetail({ id, project }: { id: string; project?: string }) {
  const { session, can } = useOrganization()
  return can("organization_cloud:manage") ? (
    <FromOrganization key={`${session.orgId}:${id}`} id={id} />
  ) : project ? (
    <FromProject
      key={`${session.orgId}:${id}:${project}`}
      id={id}
      project={project}
    />
  ) : (
    <Frame loading={false} missing={false} restricted />
  )
}
function FromOrganization({ id }: { id: string }) {
  const cloud = useQuery(api.useOrganizationCloudsGet, {
    path: { cloudId: id },
  })
  // A rename returns the Cloud at its new revision; show it until the read
  // catches up.
  const [saved, setSaved] = useState<Profile | null>(null)
  const profile =
    saved && cloud.data && saved.revision >= cloud.data.revision
      ? saved
      : cloud.data
  return (
    <Frame
      loading={!cloud.data && cloud.status !== "error"}
      missing={cloud.status === "error" && statusOf(cloud.error) === 404}
      error={
        cloud.status === "error" && statusOf(cloud.error) !== 404
          ? cloud.error
          : undefined
      }
      name={profile?.name}
    >
      {profile && (
        <OrganizationCloud
          cloud={profile}
          onSaved={(next) => {
            setSaved(next)
            void cloud.refetch()
          }}
        />
      )}
    </Frame>
  )
}
function FromProject({ id, project }: { id: string; project: string }) {
  const clouds = useQuery(api.useCloudsList, { path: { id: project } })
  const cloud = clouds.data?.clouds.find((c): c is Profile => c?.id === id)
  return (
    <Frame
      loading={!clouds.data && clouds.status !== "error"}
      missing={!!clouds.data && !cloud}
      error={clouds.status === "error" ? clouds.error : undefined}
      name={cloud?.name}
    >
      {cloud && <ProjectCloud cloud={cloud} project={project} />}
    </Frame>
  )
}

function Frame({
  name,
  loading,
  missing,
  error,
  restricted,
  children,
}: {
  name?: string
  loading: boolean
  missing: boolean
  error?: unknown
  restricted?: boolean
  children?: React.ReactNode
}) {
  return (
    <>
      <SiteHeader
        crumbs={[
          { label: "Clouds", to: "/clouds" },
          { label: name ?? "Cloud" },
        ]}
      />
      <Page>
        <Link
          to="/clouds"
          className="-mb-2 inline-flex items-center gap-1.5 self-start text-[13px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeftIcon className="size-3.5" />
          All Clouds
        </Link>
        {restricted ? (
          <CloudEmpty
            kind="notice"
            icon={ProjectsIcon}
            title="Choose a project with Cloud access"
          >
            Open this Cloud from the Clouds list, through a project that may use
            it.
          </CloudEmpty>
        ) : error !== undefined ? (
          <CloudError
            message={messageOf(error, "The Cloud couldn't be loaded.")}
          />
        ) : missing ? (
          <CloudEmpty
            kind="notice"
            icon={CloudsIcon}
            title="No Cloud here by that ID"
            action={
              <Button nativeButton={false} render={<Link to="/clouds" />}>
                All Clouds
              </Button>
            }
          >
            It may have been deleted, or your project no longer has access.
          </CloudEmpty>
        ) : loading || !children ? (
          <div aria-busy="true" className="grid gap-3">
            <Skeleton className="h-12 w-64" />
            <Skeleton className="h-64" />
          </div>
        ) : (
          children
        )}
      </Page>
    </>
  )
}

/** The organization's view: every project's share, servers and settings. */
function OrganizationCloud({
  cloud,
  onSaved,
}: {
  cloud: Profile
  onSaved: (cloud: Profile) => void
}) {
  const grants = useQuery(api.useOrganizationCloudsListGrants, {
    path: { cloudId: cloud.id },
  })
  const pools = useQuery(api.useCloudPoolsList, { path: { cloudId: cloud.id } })
  const servers = useQuery(api.useCloudServersList, {
    path: { cloudId: cloud.id },
  })
  return (
    <CloudPage
      scope={{ cloud, project: "" }}
      manage
      grants={(grants.data?.grants ?? []).filter(
        (g): g is ProjectGrant => !!g && !g.revoked
      )}
      grantsQuery={grants}
      pools={(pools.data?.pools ?? []).filter((p): p is Pool => !!p)}
      servers={(servers.data?.servers ?? []).filter((s): s is Server => !!s)}
      onSaved={onSaved}
      onChanged={() => {
        void grants.refetch()
        void servers.refetch()
      }}
    />
  )
}

/** A project's view: only what the Cloud's grant lets this project use. */
function ProjectCloud({ cloud, project }: { cloud: Profile; project: string }) {
  return (
    <CloudPage
      scope={{ cloud, project }}
      manage={false}
      onSaved={() => undefined}
      onChanged={() => undefined}
    />
  )
}

/** headState is the Cloud's one-line state, from its latest changes. */
function headState(
  syncs: Sync[],
  environments?: number
): { label: string; tone: Tone } {
  if (syncs.some((s) => s.status === "failed"))
    return { label: "A change failed", tone: "failed" }
  const waiting = syncs.filter((s) => s.status === "routing_pending").length
  if (waiting)
    return {
      label: `${waiting} ${waiting === 1 ? "change" : "changes"} waiting`,
      tone: "waiting",
    }
  const moving = syncs.filter(movingSync).length
  if (moving)
    return {
      label: `${moving} ${moving === 1 ? "change" : "changes"} in flight`,
      tone: "flight",
    }
  // Nothing to keep in sync until an environment uses the Cloud.
  if (environments === 0) return { label: "Not in use", tone: "quiet" }
  return { label: "In sync", tone: "live" }
}

const plural = (n: number, one: string, many = `${one}s`) =>
  `${n} ${n === 1 ? one : many}`

function CloudPage({
  scope,
  manage,
  grants,
  grantsQuery,
  pools,
  servers,
  onSaved,
  onChanged,
}: {
  scope: CloudScope
  manage: boolean
  grants?: ProjectGrant[]
  grantsQuery?: {
    status: string
    error?: unknown
    refetch: () => Promise<unknown>
  }
  pools?: Pool[]
  servers?: Server[]
  onSaved: (cloud: Profile) => void
  onChanged: () => void
}) {
  const { cloud, project } = scope
  const [tab, setTab] = useState<CloudTab>("overview")
  const targets = useScopedTargets(scope)
  const usage = useScopedUsage(scope)
  const syncs = useScopedSyncs(scope)
  const connections = useQuery(api.useConnectionsList)
  const projects = useQuery(api.useProjectsList)
  useInfrastructureRefresh(syncs.list.some(movingSync), async () =>
    Promise.allSettled([syncs.refetch(), usage.refetch()])
  )
  const projectList = (projects.data?.projects ?? []).filter(
    (p): p is Project => !!p
  )
  const accounts = connections.data?.connections ?? []
  const refresh = () => {
    void targets.refetch()
    void usage.refetch()
    void syncs.refetch()
    onChanged()
  }
  const viewing = projectList.find((p) => p.id === project)
  const creator = projectList.find((p) => p.id === cloud.projectId)
  const environments = usage.data?.environments.filter((e) => e.assigned).length
  const state = headState(syncs.list, environments)
  const owner = !cloud.projectId ? "Workspace" : (creator?.name ?? "A project")

  const summary = project
    ? [
        cloud.projectId === project
          ? `${viewing?.name ?? "This project"}'s Cloud`
          : `Shared with ${viewing?.name ?? "this project"} by your workspace`,
        targets.data &&
          plural(
            targets.list.length,
            "target you may use",
            "targets you may use"
          ),
      ]
    : [
        cloud.projectId ? `Created in ${owner}` : "Workspace Cloud",
        targets.data && plural(targets.list.length, "target"),
        grants && plural(grants.length, "project"),
        environments !== undefined && plural(environments, "environment"),
      ]

  const failed =
    targets.status === "error" ||
    connections.status === "error" ||
    projects.status === "error"
  const tabs: { id: CloudTab; label: string; count?: number; show: boolean }[] =
    [
      { id: "overview", label: "Overview", show: true },
      {
        id: "targets",
        label: "Targets",
        count: targets.data ? targets.list.length : undefined,
        show: true,
      },
      {
        id: "projects",
        label: "Projects",
        count: grants?.length,
        show: !project,
      },
      {
        id: "servers",
        label: "Servers",
        count: servers?.length,
        show: !project,
      },
      { id: "capacity", label: "Capacity", show: !project },
      {
        id: "changes",
        label: "Changes",
        count: syncs.list.filter(movingSync).length || undefined,
        show: true,
      },
      { id: "settings", label: "Settings", show: manage },
    ]
  return (
    <>
      <header className="flex items-center gap-3.5">
        <span className="flex size-11 shrink-0 items-center justify-center rounded-xl bg-muted">
          <CloudsIcon className="size-[22px]" />
        </span>
        <div className="grid min-w-0 gap-1">
          <div className="flex flex-wrap items-center gap-2.5">
            <h1 className="text-2xl leading-tight font-semibold tracking-[-0.025em] break-words">
              {cloud.name}
            </h1>
            {syncs.data && (
              <StatusPill tone={state.tone}>{state.label}</StatusPill>
            )}
          </div>
          <p className="text-[13px] text-muted-foreground">
            {summary.filter(Boolean).join(" · ")}
          </p>
        </div>
      </header>
      {failed ? (
        <QueryError
          error={targets.error ?? connections.error ?? projects.error}
          fallback="This Cloud couldn't be loaded."
          retry={() =>
            Promise.allSettled([
              targets.refetch(),
              connections.refetch(),
              projects.refetch(),
            ])
          }
        />
      ) : !targets.data || !connections.data || !projects.data ? (
        <Skeleton className="h-64 rounded-xl" />
      ) : (
        <Tabs value={tab} onValueChange={(v) => setTab(v as CloudTab)}>
          <TabsList
            variant="line"
            aria-label="Cloud views"
            className="mb-4 max-w-full overflow-x-auto"
          >
            {tabs
              .filter((t) => t.show)
              .map((t) => (
                <TabsTrigger key={t.id} value={t.id}>
                  {t.label}
                  {t.count !== undefined && <TabsCount>{t.count}</TabsCount>}
                </TabsTrigger>
              ))}
          </TabsList>
          <TabsContent value="overview">
            <Overview
              scope={scope}
              owner={owner}
              targets={targets.list}
              connections={accounts}
              pools={pools}
              servers={servers}
              usage={usage}
              syncs={syncs.list}
              grants={grants}
              projects={projectList}
              onTab={setTab}
              onChanged={refresh}
            />
          </TabsContent>
          <TabsContent value="targets">
            <Targets
              scope={scope}
              targets={targets.list}
              connections={accounts}
              pools={pools}
              usage={usage.data}
              manage={manage}
              onSaved={refresh}
            />
          </TabsContent>
          {!project && (
            <TabsContent value="projects">
              {grantsQuery?.status === "error" ? (
                <QueryError
                  error={grantsQuery.error}
                  fallback="Project access couldn't be loaded."
                  retry={grantsQuery.refetch}
                />
              ) : (
                <Projects
                  scope={scope}
                  grants={grants ?? []}
                  projects={projectList}
                  targets={targets.list}
                  usage={usage.data}
                  syncs={syncs.list}
                  manage={manage}
                  onSaved={refresh}
                />
              )}
            </TabsContent>
          )}
          {!project && (
            <TabsContent value="servers">
              <Servers cloud={cloud} />
            </TabsContent>
          )}
          {!project && (
            <TabsContent value="capacity">
              <Capacity
                cloudId={cloud.id}
                pools={pools ?? []}
                connections={accounts}
              />
            </TabsContent>
          )}
          <TabsContent value="changes">
            <Changes
              scope={scope}
              targets={targets.list}
              connections={accounts}
              onChanged={refresh}
            />
          </TabsContent>
          {manage && (
            <TabsContent value="settings">
              <Settings
                cloud={cloud}
                environments={environments}
                onSaved={onSaved}
              />
            </TabsContent>
          )}
        </Tabs>
      )}
    </>
  )
}
