import { useEffect, useRef, useState } from "react"
import { useLiveState } from "@/lib/realtime-state"

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
  ClusterDetail,
  ClusterOperationDetail,
} from "@/components/cloud-detail/cluster-detail"
import {
  CloudInfrastructure,
  type InfrastructureView,
} from "@/components/cloud-detail/clusters"
import {
  movingSync,
  useScopedSyncs,
  useScopedTargets,
  useScopedUsage,
  type CloudScope,
} from "@/components/cloud-detail/data"
import { Overview, type CloudTab } from "@/components/cloud-detail/overview"
import { PlatformCloudUsage } from "@/components/platform-cloud-usage"
import { Projects } from "@/components/cloud-detail/projects"
import { Settings } from "@/components/cloud-detail/settings"
import { Targets } from "@/components/cloud-detail/targets"
import {
  CloudPageHeader,
  PageMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudError,
  useInfrastructureRefresh,
} from "@/components/cloud-targets/infrastructure-ui"
import { Page } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { SiteHeader, type Crumb } from "@/components/site-header"
import { Button } from "@/components/ui/button"
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
import { useOrganization } from "@/lib/organization"
import { Link, useNavigate } from "@/lib/router"

/** Where the page is: its tab, the list inside Infrastructure, and a child page. */
type Place = {
  tab: CloudTab
  view: InfrastructureView
  cluster: string
  clusterOperation: string
}

type CloudDetailProps = {
  id: string
  project?: string
  /** The view the page opens on; Overview when unset. */
  tab?: CloudTab
  view?: InfrastructureView
  cluster?: string
  clusterOperation?: string
}

export function CloudDetail(props: CloudDetailProps) {
  const { id, project } = props
  const { session, can } = useOrganization()
  return can("organization_cloud:manage") ? (
    <FromOrganization key={`${session.orgId}:${id}`} {...props} />
  ) : project ? (
    <FromProject key={`${session.orgId}:${id}:${project}`} {...props} />
  ) : (
    <Frame loading={false} missing={false} restricted />
  )
}

/**
 * usePlace keeps the page's place in the URL. Local state answers at once;
 * the URL follows, so a tab or cluster can be linked and Back works. Links
 * elsewhere on the page that change the search land here too.
 */
function placeOf(p: Omit<CloudDetailProps, "id">): Place {
  const child = !!(p.cluster || p.clusterOperation)
  return {
    tab: p.tab ?? (child ? "infrastructure" : "overview"),
    view: p.view ?? (child ? "clusters" : "servers"),
    cluster: p.cluster ?? "",
    clusterOperation: p.clusterOperation ?? "",
  }
}

function usePlace(
  props: CloudDetailProps
): [Place, (next: Partial<Place>) => void] {
  const navigate = useNavigate()
  const [place, setPlace] = useState<Place>(() => {
    // A saved cluster operation reopens from a full page load.
    const saved = new URLSearchParams(window.location.search).get(
      "clusterOperation"
    )
    return placeOf(
      !props.clusterOperation && saved
        ? { ...props, clusterOperation: saved }
        : props
    )
  })
  const { tab, view, cluster, clusterOperation } = props
  const first = useRef(true)
  useEffect(() => {
    // The first render already holds the search; later ones follow it, so a
    // breadcrumb or a link with a new search moves the page.
    if (first.current) {
      first.current = false
      return
    }
    setPlace(placeOf({ tab, view, cluster, clusterOperation }))
  }, [tab, view, cluster, clusterOperation])
  const go = (next: Partial<Place>) => {
    const merged = { ...place, ...next }
    setPlace(merged)
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({
        ...prev,
        tab: merged.tab === "overview" ? undefined : merged.tab,
        view:
          merged.tab === "infrastructure" && merged.view !== "servers"
            ? merged.view
            : undefined,
        cluster: merged.cluster || undefined,
        clusterOperation: merged.clusterOperation || undefined,
      }),
      // A child page is a new step in history; switching tabs is not.
      replace: !(next.cluster || next.clusterOperation),
    } as never)
  }
  return [place, go]
}

function FromOrganization(props: CloudDetailProps) {
  const { id } = props
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
      retry={cloud.refetch}
    >
      {profile && (
        <OrganizationCloud
          cloud={profile}
          place={props}
          onSaved={(next) => {
            setSaved(next)
            void cloud.refetch()
          }}
        />
      )}
    </Frame>
  )
}
function FromProject(props: CloudDetailProps) {
  const { id } = props
  const project = props.project ?? ""
  const clouds = useQuery(api.useCloudsList, { path: { id: project } })
  const cloud = clouds.data?.clouds.find((c): c is Profile => c?.id === id)
  return (
    <Frame
      loading={!clouds.data && clouds.status !== "error"}
      missing={!!clouds.data && !cloud}
      error={clouds.status === "error" ? clouds.error : undefined}
      retry={clouds.refetch}
    >
      {cloud && <ProjectCloud cloud={cloud} project={project} place={props} />}
    </Frame>
  )
}

function Frame({
  loading,
  missing,
  error,
  retry,
  restricted,
  children,
}: {
  loading: boolean
  missing: boolean
  error?: unknown
  retry?: () => Promise<unknown>
  restricted?: boolean
  children?: React.ReactNode
}) {
  const crumbs: Crumb[] = [
    { label: "Clouds", to: "/clouds" },
    { label: "Cloud" },
  ]
  return restricted ||
    error !== undefined ||
    missing ||
    loading ||
    !children ? (
    <>
      <SiteHeader crumbs={crumbs} />
      <Page>
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
          retry ? (
            <QueryError
              error={error}
              fallback="The Cloud couldn't be loaded."
              retry={retry}
            />
          ) : (
            <CloudError
              message={messageOf(error, "The Cloud couldn't be loaded.")}
            />
          )
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
        ) : (
          <div aria-busy="true" className="grid gap-3">
            <Skeleton className="h-12 w-64" />
            <Skeleton className="h-64" />
          </div>
        )}
      </Page>
    </>
  ) : (
    children
  )
}

/** The organization's view: every project's share, servers and settings. */
function OrganizationCloud({
  cloud,
  place,
  onSaved,
}: {
  cloud: Profile
  place: CloudDetailProps
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
      place={place}
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
        void pools.refetch()
      }}
    />
  )
}

/** A project's view: only what the Cloud's grant lets this project use. */
function ProjectCloud({
  cloud,
  project,
  place,
}: {
  cloud: Profile
  project: string
  place: CloudDetailProps
}) {
  return (
    <CloudPage
      scope={{ cloud, project }}
      place={place}
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
  place: wanted,
  manage,
  grants,
  grantsQuery,
  pools,
  servers,
  onSaved,
  onChanged,
}: {
  scope: CloudScope
  place: CloudDetailProps
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
  // The instance owner runs a platform Cloud, so a workspace never edits it.
  const edit = manage && !cloud.platformOwned
  // Its servers, clusters and pools are the owner's too, so they stay hidden.
  const infra = !project && !cloud.platformOwned
  const [place, go] = usePlace(wanted)
  const [copied, setCopied] = useState(false)
  const targets = useScopedTargets(scope)
  const usage = useScopedUsage(scope)
  const syncs = useScopedSyncs(scope)
  const connections = useQuery(api.useConnectionsList)
  const projects = useQuery(api.useProjectsList)
  const live = useLiveState()
  const { refetch: refetchTargets } = targets
  const { refetch: refetchUsage } = usage
  const { refetch: refetchConnections } = connections
  useEffect(() => {
    if (!live.lastSync) return
    void refetchTargets()
    void refetchUsage()
    void refetchConnections()
  }, [live.lastSync, refetchTargets, refetchUsage, refetchConnections])
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
          : `Shared with ${viewing?.name ?? "this project"} by your organization`,
        targets.data &&
          plural(
            targets.list.length,
            "target you may use",
            "targets you may use"
          ),
      ]
    : [
        cloud.projectId ? `Created in ${owner}` : "Organization Cloud",
        targets.data && plural(targets.list.length, "target"),
        grants && plural(grants.length, "project"),
        environments !== undefined && plural(environments, "environment"),
      ]

  const failed =
    targets.status === "error" ||
    connections.status === "error" ||
    projects.status === "error"
  const open = (n: number) => n || undefined
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
      { id: "infrastructure", label: "Infrastructure", show: infra },
      { id: "capacity", label: "Capacity", show: infra },
      {
        id: "changes",
        label: "Changes",
        // Changes counts what is still open, not every change.
        count: open(syncs.list.filter(movingSync).length),
        show: true,
      },
      { id: "settings", label: "Settings", show: edit },
    ]
  const tab = tabs.some((t) => t.id === place.tab && t.show)
    ? place.tab
    : "overview"
  const backToInfrastructure = () =>
    go({
      tab: "infrastructure",
      view: "clusters",
      cluster: "",
      clusterOperation: "",
    })

  const crumbs: Crumb[] = [
    { label: "Clouds", to: "/clouds" },
    { label: cloud.name },
  ]

  // A cluster and a cluster operation are pages of their own, under the
  // Cloud's Infrastructure tab.
  if (infra && (place.cluster || place.clusterOperation))
    return (
      <>
        <SiteHeader
          crumbs={[
            { label: "Clouds", to: "/clouds" },
            {
              label: cloud.name,
              link: <Link to="/clouds/$id" params={{ id: cloud.id }} />,
            },
            {
              label: "Infrastructure",
              link: (
                <Link
                  to="/clouds/$id"
                  params={{ id: cloud.id }}
                  search={{ tab: "infrastructure", view: "clusters" }}
                />
              ),
            },
            { label: place.clusterOperation ? "Cluster operation" : "Cluster" },
          ]}
        />
        <Page>
          {place.clusterOperation ? (
            <ClusterOperationDetail
              key={place.clusterOperation}
              cloud={cloud}
              id={place.clusterOperation}
              onSaved={(id) => go({ clusterOperation: id, cluster: "" })}
              onBack={backToInfrastructure}
              onRefreshReview={backToInfrastructure}
            />
          ) : (
            <ClusterDetail
              key={place.cluster}
              cloud={cloud}
              id={place.cluster}
              onSaved={(id) => go({ clusterOperation: id, cluster: "" })}
              onBack={backToInfrastructure}
              onOpenChanges={() =>
                go({ tab: "changes", cluster: "", clusterOperation: "" })
              }
              onOpenTargets={() =>
                go({ tab: "targets", cluster: "", clusterOperation: "" })
              }
            />
          )}
        </Page>
      </>
    )

  return (
    <>
      <SiteHeader crumbs={crumbs} />
      <Page>
        <CloudPageHeader
          glyph={<CloudsIcon className="size-[22px]" />}
          title={cloud.name}
          status={
            syncs.data && (
              <StatusPill tone={state.tone}>{state.label}</StatusPill>
            )
          }
          meta={summary.filter(Boolean).join(" · ")}
          actions={
            <>
              <PageMenu label="More Cloud actions">
                {edit && (
                  <DropdownMenuItem onClick={() => go({ tab: "settings" })}>
                    Rename
                  </DropdownMenuItem>
                )}
                {manage && (
                  <DropdownMenuItem onClick={() => go({ tab: "projects" })}>
                    Give a project access
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem
                  onClick={() => {
                    void navigator.clipboard
                      ?.writeText(cloud.id)
                      .then(() => setCopied(true))
                      .catch(() => undefined)
                  }}
                >
                  {copied ? "Cloud ID copied" : "Copy Cloud ID"}
                </DropdownMenuItem>
                {edit && (
                  <>
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      variant="destructive"
                      onClick={() => go({ tab: "settings" })}
                    >
                      Delete Cloud
                    </DropdownMenuItem>
                  </>
                )}
              </PageMenu>
            </>
          }
        />
        {cloud.platformOwned && (
          <PlatformCloudUsage
            project={project}
            projects={projectList}
            environments={usage.data?.environments}
          />
        )}
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
          <Tabs value={tab} onValueChange={(v) => go({ tab: v as CloudTab })}>
            <TabsList
              variant="line"
              aria-label="Cloud views"
              className="mb-5 max-w-full overflow-x-auto"
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
                onTab={(t) => go({ tab: t })}
              />
            </TabsContent>
            <TabsContent value="targets">
              <Targets
                scope={scope}
                targets={targets.list}
                connections={accounts}
                pools={pools}
                usage={usage.data}
                manage={edit}
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
            {infra && (
              <TabsContent value="infrastructure">
                <CloudInfrastructure
                  cloud={cloud}
                  view={place.view}
                  onView={(view) => go({ view })}
                  onOpenCluster={(id) =>
                    go({ cluster: id, clusterOperation: "" })
                  }
                  onOpenOperation={(id) =>
                    go({ clusterOperation: id, cluster: "" })
                  }
                />
              </TabsContent>
            )}
            {infra && (
              <TabsContent value="capacity">
                <Capacity
                  cloudId={cloud.id}
                  pools={pools ?? []}
                  connections={accounts}
                  onPoolsChanged={onChanged}
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
            {edit && (
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
      </Page>
    </>
  )
}
