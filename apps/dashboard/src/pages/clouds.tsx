import { Alert, AlertDescription } from "@/components/ui/alert"
import { CloudsIcon } from "@/components/icons"
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
// Clouds: every Cloud in the organization, with the accounts their targets
// can point at. A Cloud's own page (CloudDetail) holds the workspace where
// its targets, assignments and syncs are managed, one environment at a time.
import { ChevronRightIcon, CloudIcon, PlusIcon } from "lucide-react"
import { useState } from "react"

import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Profile,
  Project,
} from "@/client/forge/loomplane/types"
import { messageOf } from "@/client/errors"
import { useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import {
  noPlacement,
  type Placement,
  PlacementPicker,
} from "@/components/cloud-targets/org-cloud"
import { AccountStack } from "@/components/cloud-targets/target-mix"
import { statusLabels } from "@/components/cloud-connections"
import {
  CloudForm,
  CloudWorkspace,
  EmptyClouds,
} from "@/components/cloud-targets/workspace"
import { Page, PageHeading } from "@/components/page"
import { QueryError } from "@/components/query-error"
import { StatusPill, type Tone } from "@/components/runs"
import { SiteHeader } from "@/components/site-header"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { cloudLabel } from "@/lib/clouds"
import { relative } from "@/lib/format"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link, useNavigate } from "@/lib/router"
import { useNow } from "@/lib/use-runs"

const connectionTone: Record<string, Tone> = {
  ready: "live",
  incomplete: "waiting",
  failed: "failed",
}

export function Clouds() {
  const { session, can } = useOrganization()
  const allowed = can("organization_cloud:manage") || can("cloud_account:read")
  // Without access nothing about Clouds is read.
  if (!allowed) {
    return (
      <>
        <SiteHeader crumbs={[{ label: "Clouds" }]} />
        <Page>
          <PageHeading
            title="Clouds"
            description="Named targets keep your services connected to the accounts you choose."
          />
          <EmptyClouds
            kind="notice"
            title="Cloud access is restricted"
            description="Ask your organization owner for permission to view Cloud accounts."
          />
        </Page>
      </>
    )
  }
  return <CloudsList key={session.orgId} />
}

function CloudsList() {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  // Infrastructure managers own Clouds for the whole organization and give
  // projects access; everyone else sees their projects' own Clouds.
  const shared = can("organization_cloud:manage")
  const projects = useQuery(api.useProjectsList)
  const connections = useQuery(api.useConnectionsList)
  const [creating, setCreating] = useState(false)
  const list = projects.data?.projects ?? []

  return (
    <>
      <SiteHeader crumbs={[{ label: "Clouds" }]} />
      <Page>
        <PageHeading
          title="Clouds"
          description="A Cloud is a named set of targets, like frontend or backend, each pointing at an account you've connected. Assign one to an environment and its services follow."
          actions={
            <>
              <Button
                variant="outline"
                nativeButton={false}
                render={<Link to="/integrations" />}
              >
                <PlusIcon />
                Connect an account
              </Button>
              {(shared || (manage && list.length > 0)) && (
                <Button onClick={() => setCreating(true)}>
                  <CloudIcon />
                  New Cloud
                </Button>
              )}
            </>
          }
        />
        {shared ? (
          <OrganizationClouds onCreate={() => setCreating(true)} />
        ) : (
          <>
            {projects.status === "error" ? (
              <QueryError
                error={projects.error}
                fallback="Projects couldn't be loaded."
                retry={projects.refetch}
              />
            ) : !projects.data ? (
              <Skeleton className="h-40 rounded-xl" />
            ) : list.length === 0 ? (
              <EmptyClouds
                kind="notice"
                title="Create a project first"
                description="Your organization manager can create Clouds before projects exist. Create a project to use granted targets."
              >
                <Button nativeButton={false} render={<Link to="/projects" />}>
                  <PlusIcon />
                  Projects
                </Button>
              </EmptyClouds>
            ) : (
              <section aria-label="Your Clouds" className="grid gap-2">
                <h2 className="text-[15px] font-semibold">Your Clouds</h2>
                <div className="overflow-x-auto rounded-xl bg-card ring-1 ring-border">
                  <div className="min-w-[680px]">
                    <div className="grid grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_220px_110px_16px] gap-4 border-b bg-subtle px-4 py-2 text-[11px] font-medium tracking-[0.04em] text-muted-foreground uppercase">
                      <span>Cloud</span>
                      <span>Project</span>
                      <span>Targets</span>
                      <span>Updated</span>
                      <span />
                    </div>
                    {list.map((p) => (
                      <ProjectClouds key={p.id} project={p} />
                    ))}
                  </div>
                </div>
                <p className="text-xs text-muted-foreground">
                  Open a Cloud to assign it to an environment, edit its targets
                  and review each sync.
                </p>
              </section>
            )}
          </>
        )}

        {
          <section aria-label="Connected accounts" className="grid gap-2">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <h2 className="text-[15px] font-semibold">
                Connected accounts{" "}
                <span className="font-mono text-xs font-normal text-muted-foreground">
                  {connections.data?.connections.length ?? ""}
                </span>
              </h2>
              <span className="text-[12.5px] text-muted-foreground">
                Where targets can point. Each one was checked with its cloud
                before it was kept.
              </span>
            </div>
            {connections.status === "error" ? (
              <QueryError
                error={connections.error}
                fallback="Connected accounts couldn't be loaded."
                retry={connections.refetch}
              />
            ) : !connections.data ? (
              <Skeleton className="h-24 rounded-xl" />
            ) : (
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {connections.data.connections.map((c) => (
                  <AccountCard key={c.id} connection={c} />
                ))}
                <Link
                  to="/integrations"
                  className="flex min-h-24 items-center justify-center gap-2.5 rounded-xl border border-dashed text-[13.5px] font-medium text-muted-foreground transition-colors hover:bg-subtle hover:text-foreground"
                >
                  <span className="flex size-7 items-center justify-center rounded-lg bg-muted">
                    <PlusIcon className="size-4" />
                  </span>
                  Connect an account
                </Link>
              </div>
            )}
          </section>
        }
      </Page>
      {shared ? (
        <NewOrganizationCloud open={creating} onOpenChange={setCreating} />
      ) : (
        <NewCloudDialog
          open={creating}
          onOpenChange={setCreating}
          projects={list}
        />
      )}
    </>
  )
}

/** ProjectClouds is one project's Clouds as rows of the list. */
function ProjectClouds({
  project,
  hideEmpty = false,
}: {
  project: Project
  hideEmpty?: boolean
}) {
  const clouds = useQuery(api.useCloudsList, { path: { id: project.id } })
  // A project's list also holds the organization's Clouds it was granted.
  // Next to the organization's own list, show only the ones it owns.
  const list = (clouds.data?.clouds ?? []).filter((c): c is Profile => !!c)
  if (clouds.status === "error") {
    return (
      <div className="border-t px-4 py-3">
        <QueryError
          error={clouds.error}
          fallback={`${project.name}'s Clouds couldn't be loaded.`}
          retry={clouds.refetch}
        />
      </div>
    )
  }
  if (!clouds.data) {
    return (
      <div className="border-t px-4 py-3">
        <Skeleton className="h-5 w-64" />
      </div>
    )
  }
  if (list.length === 0) {
    if (hideEmpty) return null
    return (
      <div className="grid grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_220px_110px_16px] items-center gap-4 border-t px-4 py-2.5 text-[13px] text-muted-foreground">
        <span>No Clouds yet</span>
        <ProjectName project={project} />
        <span>Direct placement</span>
        <span />
        <span />
      </div>
    )
  }
  return list.map((c) => <CloudRow key={c.id} cloud={c} project={project} />)
}

function ProjectName({ project }: { project: Project }) {
  const s = projectStyle(project)
  return (
    <span className="flex min-w-0 items-center gap-2 text-[13px] text-foreground">
      <ProjectIcon icon={s.icon} color={s.color} size="xs" />
      <span className="truncate">{project.name}</span>
    </span>
  )
}

function CloudRow({ cloud: c, project }: { cloud: Profile; project: Project }) {
  const now = useNow()
  const targets = useQuery(api.useCloudTargetsList, {
    path: { id: project.id, cloudId: c.id },
  })
  const connections = useQuery(api.useConnectionsList)
  const list = (targets.data?.targets ?? []).filter(
    (t): t is NonNullable<typeof t> => !!t
  )
  return (
    <Link
      data-cloud-row
      to="/clouds/$id"
      params={{ id: c.id }}
      search={{ project: project.id }}
      className="group grid grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_220px_110px_16px] items-center gap-4 border-t px-4 py-2.5 text-foreground transition-colors hover:bg-subtle"
    >
      <span className="flex min-w-0 items-center gap-2.5">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-muted">
          <CloudIcon className="size-4" />
        </span>
        <span className="truncate text-sm font-semibold">{c.name}</span>
      </span>
      <ProjectName project={project} />
      <span className="min-w-0 text-[12.5px]">
        {targets.data ? (
          <AccountStack
            targets={list}
            connections={connections.data?.connections ?? []}
          />
        ) : (
          "…"
        )}
      </span>
      <span className="text-[12.5px] text-muted-foreground">
        {relative(Date.parse(c.updatedAt), now)}
      </span>
      <ChevronRightIcon className="size-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5" />
    </Link>
  )
}

function AccountCard({ connection: c }: { connection: Connection }) {
  const now = useNow()
  return (
    <Link
      to="/clouds/accounts/$id"
      params={{ id: c.id }}
      className="grid gap-2.5 rounded-xl bg-card p-4 text-foreground ring-1 ring-border transition-shadow hover:shadow-[0_6px_18px_oklch(0.2_0.01_50/0.08)]"
    >
      <div className="flex items-center gap-2.5">
        <CloudGlyph cloud={c.cloud} size="md" />
        <span className="grid min-w-0 flex-1 gap-0.5">
          <span className="truncate text-sm font-semibold">{c.name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {cloudLabel(c.cloud)}
            {c.account && ` · ${c.account}`}
          </span>
        </span>
        <StatusPill tone={connectionTone[c.status] ?? "quiet"}>
          {statusLabels[c.status] ?? c.status}
        </StatusPill>
      </div>
      <span className="text-xs text-muted-foreground">
        {c.checkedAt
          ? `Checked ${relative(Date.parse(c.checkedAt), now)}`
          : "Not checked yet"}
      </span>
    </Link>
  )
}

/** OrganizationClouds lists the Clouds the organization owns. */
function OrganizationClouds({ onCreate }: { onCreate: () => void }) {
  const clouds = useQuery(api.useOrganizationCloudsList)
  const list = (clouds.data?.clouds ?? []).filter((c): c is Profile => !!c)
  return (
    <section aria-label="Your Clouds" className="grid gap-2">
      <h2 className="text-[15px] font-semibold">Your Clouds</h2>
      {clouds.status === "error" ? (
        <QueryError
          error={clouds.error}
          fallback="Clouds couldn't be loaded."
          retry={clouds.refetch}
        />
      ) : !clouds.data ? (
        <Skeleton className="h-28 rounded-xl" />
      ) : list.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <CloudsIcon />
            </EmptyMedia>
            <EmptyTitle>No Clouds yet</EmptyTitle>
            <EmptyDescription>
              A Cloud is a set of targets on the accounts you&apos;ve connected.
              Make one, then give projects access to the targets they need.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button onClick={onCreate}>New Cloud</Button>
          </EmptyContent>
        </Empty>
      ) : (
        <div className="overflow-x-auto rounded-xl bg-card ring-1 ring-border">
          <div className="min-w-[680px]">
            <div className="grid grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_220px_110px_16px] gap-4 border-b bg-subtle px-4 py-2 text-[11px] font-medium tracking-[0.04em] text-muted-foreground uppercase">
              <span>Cloud</span>
              <span>Used by</span>
              <span>Targets</span>
              <span>Updated</span>
              <span />
            </div>
            {list.map((c) => (
              <OrganizationCloudRow key={c.id} cloud={c} />
            ))}
          </div>
        </div>
      )}
    </section>
  )
}

function OrganizationCloudRow({ cloud: c }: { cloud: Profile }) {
  const now = useNow()
  const targets = useQuery(api.useOrganizationCloudTargetsList, {
    path: { cloudId: c.id },
  })
  const grants = useQuery(api.useOrganizationCloudsListGrants, {
    path: { cloudId: c.id },
  })
  const projects = useQuery(api.useProjectsList)
  const connections = useQuery(api.useConnectionsList)
  const list = (targets.data?.targets ?? []).filter(
    (t): t is NonNullable<typeof t> => !!t
  )
  const users = (grants.data?.grants ?? [])
    .filter((g) => !!g && !g.revoked)
    .map((g) => projects.data?.projects.find((p) => p.id === g!.projectId))
    .filter((p): p is Project => !!p)
  return (
    <Link
      to="/clouds/$id"
      params={{ id: c.id }}
      className="group grid grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_220px_110px_16px] items-center gap-4 border-t px-4 py-2.5 text-foreground transition-colors first-of-type:border-t-0 hover:bg-subtle"
    >
      <span className="flex min-w-0 items-center gap-2.5">
        <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-brand-tint text-brand-ink">
          <CloudIcon className="size-4" />
        </span>
        <span className="truncate text-sm font-semibold">{c.name}</span>
      </span>
      <span className="flex min-w-0 items-center gap-2 text-[12.5px]">
        {!grants.data ? (
          "…"
        ) : users.length === 0 ? (
          <span className="text-muted-foreground">No projects yet</span>
        ) : (
          <>
            <span className="flex shrink-0 -space-x-1">
              {users.slice(0, 3).map((p) => {
                const s = projectStyle(p)
                return (
                  <span key={p.id} className="rounded-[6px] ring-2 ring-card">
                    <ProjectIcon icon={s.icon} color={s.color} size="xs" />
                  </span>
                )
              })}
            </span>
            <span className="truncate">
              {users.length === 1 ? users[0]!.name : `${users.length} projects`}
            </span>
          </>
        )}
      </span>
      <span className="min-w-0 text-[12.5px]">
        {targets.data ? (
          <AccountStack
            targets={list}
            connections={connections.data?.connections ?? []}
          />
        ) : (
          "…"
        )}
      </span>
      <span className="text-[12.5px] text-muted-foreground">
        {relative(Date.parse(c.updatedAt), now)}
      </span>
      <ChevronRightIcon className="size-4 text-muted-foreground/60 transition-transform group-hover:translate-x-0.5" />
    </Link>
  )
}

/** NewOrganizationCloud names a Cloud the whole organization owns. */
export function NewOrganizationCloud({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const navigate = useNavigate()
  const create = useMutation(api.useOrganizationCloudsCreate)
  const [name, setName] = useState("")
  const [error, setError] = useState("")
  // Optional: a Cloud can start with its one provider and account, so it
  // places services before it has any targets.
  const [withDefault, setWithDefault] = useState(false)
  const [placement, setPlacement] = useState<Placement>(noPlacement)
  const [complete, setComplete] = useState(false)
  const reset = () => {
    setName("")
    setWithDefault(false)
    setPlacement(noPlacement)
    setComplete(false)
  }
  const submit = async () => {
    setError("")
    try {
      const cloud = await create.mutateAsync({
        body: {
          name: name.trim(),
          ...(withDefault && complete
            ? {
                defaultPlacement: {
                  ...placement,
                  region: placement.region.trim(),
                },
              }
            : {}),
        },
      })
      onOpenChange(false)
      reset()
      void navigate({ to: "/clouds/$id", params: { id: cloud.id } })
    } catch (err) {
      setError(messageOf(err, "The Cloud wasn't created."))
    }
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(o) => !create.isPending && onOpenChange(o)}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>New Cloud</DialogTitle>
          <DialogDescription>
            Name it for what it holds, like Production or EU. You add its
            targets and choose which projects can use them next.
          </DialogDescription>
        </DialogHeader>
        <form
          className="grid gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            if (name.trim()) void submit()
          }}
        >
          <FormField className="grid gap-1.5 text-[12.5px] font-medium text-muted-foreground">
            <FieldLabel className="contents">
              Name
              <Input
                autoFocus
                aria-label="Cloud name"
                value={name}
                disabled={create.isPending}
                onChange={(e) => setName(e.target.value)}
                placeholder="Production"
              />
            </FieldLabel>
          </FormField>
          <div className="grid gap-3 rounded-xl bg-subtle p-3">
            <FormField
              orientation="horizontal"
              className="flex items-start gap-2.5 text-[13px]"
            >
              <FieldLabel className="contents">
                <Checkbox
                  checked={withDefault}
                  disabled={create.isPending}
                  onCheckedChange={(v) => setWithDefault(!!v)}
                  className="mt-0.5"
                />
                <span className="grid gap-0.5">
                  <span className="font-medium">
                    Start with a default provider
                  </span>
                  <span className="text-xs text-muted-foreground">
                    With no targets, every service on the Cloud runs on this one
                    provider and account. You can add targets or change it
                    later.
                  </span>
                </span>
              </FieldLabel>
            </FormField>
            {withDefault && (
              <PlacementPicker
                value={placement}
                disabled={create.isPending}
                onChange={(next, ok) => {
                  setPlacement(next)
                  setComplete(ok)
                }}
              />
            )}
          </div>
          {error && (
            <Alert variant="destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={create.isPending}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={
                !name.trim() || create.isPending || (withDefault && !complete)
              }
            >
              Create Cloud
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** NewCloudDialog asks which project a new Cloud belongs to, then names it. */
function NewCloudDialog({
  open,
  onOpenChange,
  projects,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  projects: Project[]
}) {
  const navigate = useNavigate()
  const [project, setProject] = useState("")
  const [naming, setNaming] = useState(false)
  const chosen = project || projects[0]?.id || ""
  if (naming && chosen) {
    return (
      <CloudForm
        projectId={chosen}
        onClose={() => {
          setNaming(false)
          onOpenChange(false)
        }}
        onSaved={(id) => {
          setNaming(false)
          onOpenChange(false)
          void navigate({
            to: "/clouds/$id",
            params: { id },
            search: { project: chosen },
          })
        }}
      />
    )
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New Cloud</DialogTitle>
          <DialogDescription>
            A Cloud belongs to a project. Its targets place that project&apos;s
            services once it&apos;s assigned to an environment.
          </DialogDescription>
        </DialogHeader>
        <FormField className="grid gap-1.5 text-[12.5px] font-medium text-muted-foreground">
          <FieldLabel className="contents">
            Project
            <Select value={chosen} onValueChange={(e) => setProject(e ?? "")}>
              <SelectTrigger
                aria-label="Project for the new Cloud"
                className={"w-full min-w-0"}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {projects.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FieldLabel>
        </FormField>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button disabled={!chosen} onClick={() => setNaming(true)}>
            Continue
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * CloudsWorkspace is the full Cloud workspace for one project: pick an
 * environment and a Cloud, edit targets, review assignments and syncs.
 * A Cloud's page opens it on that Cloud.
 */
export function CloudsWorkspace({
  projectId,
  cloudId,
}: {
  projectId?: string
  cloudId?: string
} = {}) {
  const { session, can } = useOrganization()
  return (
    <WorkspaceScope
      key={session.orgId}
      organizationId={session.orgId}
      allowed={can("cloud_account:read")}
      projectId={projectId}
      cloudId={cloudId}
    />
  )
}

function WorkspaceScope({
  allowed,
  organizationId,
  projectId,
  cloudId,
}: {
  allowed: boolean
  organizationId: string
  projectId?: string
  cloudId?: string
}) {
  const projects = useQuery(api.useProjectsList)
  const preference = `loomplane.clouds.project.${organizationId}`
  const [selected, setSelected] = useState(
    () => projectId ?? window.localStorage.getItem(preference) ?? ""
  )
  const project =
    projects.data?.projects.find((p) => p.id === selected) ??
    projects.data?.projects[0]
  return !allowed ? (
    <EmptyClouds
      kind="notice"
      title="Cloud access is restricted"
      description="Ask your organization owner for permission to view Cloud accounts."
    />
  ) : projects.status === "error" ? (
    <QueryError
      error={projects.error}
      fallback="Projects couldn't be loaded."
      retry={projects.refetch}
    />
  ) : !projects.data ? (
    <Skeleton className="h-24" />
  ) : !project ? (
    <EmptyClouds
      kind="notice"
      title="Create a project first"
      description="Your organization manager can create Clouds before projects exist. Create a project to use granted targets."
    >
      <Link to="/projects">
        <Button>
          <PlusIcon />
          Projects
        </Button>
      </Link>
    </EmptyClouds>
  ) : (
    <CloudWorkspace
      key={project.id}
      projectId={project.id}
      cloudId={project.id === projectId ? cloudId : undefined}
      projectControl={
        <FormField className="grid min-w-32 flex-1 gap-1 text-xs text-muted-foreground">
          <FieldLabel className="contents">
            Project
            <Select
              value={project.id}
              onValueChange={(e) => {
                setSelected(e ?? "")
                window.localStorage.setItem(preference, e ?? "")
              }}
            >
              <SelectTrigger aria-label="Project" className={"w-full min-w-0"}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {projects.data.projects.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FieldLabel>
        </FormField>
      }
    />
  )
}
