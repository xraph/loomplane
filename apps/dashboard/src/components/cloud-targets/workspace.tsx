import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { Card } from "@/components/ui/card"
import { Item, ItemGroup } from "@/components/ui/item"
import {
  CloudLoading,
  listItemClass,
} from "@/components/cloud-targets/infrastructure-ui"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { type ReactNode, useEffect, useState } from "react"
import { PlusIcon } from "lucide-react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Environment,
  Profile,
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  ZeroState,
  ZeroStateContent,
  ZeroStateDescription,
  ZeroStateHeader,
  ZeroStateMedia,
  ZeroStateTitle,
} from "@/components/zero-state"
import { cloudDeletionError } from "@/lib/cloud-targets"
import { useOrganization } from "@/lib/organization"
import { CloudGlyph } from "@/components/cloud-glyph"
import { CloudsIcon } from "@/components/icons"
import { cloudLabel } from "@/lib/clouds"
import { cn } from "@/lib/utils"

import { AccountSummary, cloudOf } from "./target-mix"
import { TargetEditor } from "./target-editor"
import { ChangeReview } from "./change-review"
import { ServiceAdoption } from "./service-adoption"
import { SyncHistory } from "./sync-history"

export function EmptyClouds({
  title,
  description,
  children,
  kind = "list",
}: {
  title: string
  description: string
  children?: ReactNode
  /** A list that will fill gets the faded preview; a notice doesn't. */
  kind?: "list" | "notice"
}) {
  return (
    <ZeroState preview={kind === "list"}>
      <ZeroStateHeader>
        <ZeroStateMedia variant="icon">
          <CloudsIcon />
        </ZeroStateMedia>
        <ZeroStateTitle>{title}</ZeroStateTitle>
        <ZeroStateDescription>{description}</ZeroStateDescription>
      </ZeroStateHeader>
      {children && <ZeroStateContent>{children}</ZeroStateContent>}
    </ZeroState>
  )
}
export function CloudWorkspace({
  projectId,
  projectControl,
  cloudId,
}: {
  projectId: string
  projectControl?: ReactNode
  /** The Cloud to open on, such as the one a Cloud's own page is about. */
  cloudId?: string
}) {
  const environments = useQuery(api.useEnvironmentsList, {
    path: { id: projectId },
  })
  const { session } = useOrganization()
  const preference = `loomplane.clouds.environment.${session.orgId}.${projectId}`
  const [chosen, setChosen] = useState(
    () => window.localStorage.getItem(preference) ?? ""
  )
  const environment =
    environments.data?.environments.find((e) => e.id === chosen) ??
    environments.data?.environments[0]
  const content =
    environments.status === "error" ? (
      <QueryError
        error={environments.error}
        fallback="Environments couldn't be loaded."
        retry={environments.refetch}
      />
    ) : !environments.data ? (
      <Skeleton className="h-32" />
    ) : !environment ? (
      <EmptyClouds
        title="No environments yet"
        description="Add an environment in your project before assigning a Cloud."
      />
    ) : (
      <EnvironmentWorkspace
        key={environment.id}
        projectId={projectId}
        cloudId={cloudId}
        environment={environment}
        environments={environments.data.environments}
        scopeControls={
          <>
            {projectControl}
            <FormField className="grid min-w-32 flex-1 gap-1 text-xs text-muted-foreground">
              <FieldLabel className="contents">
                Environment
                <Select
                  value={environment.id}
                  onValueChange={(e) => {
                    setChosen(e ?? "")
                    window.localStorage.setItem(preference, e ?? "")
                  }}
                >
                  <SelectTrigger
                    aria-label="Environment"
                    className={"w-full min-w-0"}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {environments.data.environments.map((e) => (
                      <SelectItem key={e.id} value={e.id}>
                        {e.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FieldLabel>
            </FormField>
          </>
        }
      />
    )
  return (
    <>
      {(environments.status === "error" || !environment) && (
        <section className="flex flex-wrap items-end gap-2">
          {projectControl}
        </section>
      )}
      {content}
    </>
  )
}
function EnvironmentWorkspace({
  projectId,
  cloudId,
  environment,
  environments,
  scopeControls,
}: {
  projectId: string
  cloudId?: string
  environment: Environment
  environments: Environment[]
  scopeControls: ReactNode
}) {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  const deploy = manage && can("deployment:approve")
  const profiles = useQuery(api.useCloudsList, { path: { id: projectId } })
  const assignment = useQuery(api.useEnvironmentCloudsGet, {
    path: { id: projectId, environmentId: environment.id },
  })
  const { refetch: refetchAssignment } = assignment
  const { refetch: refetchProfiles } = profiles
  const [chosen, setChosen] = useState(cloudId ?? "")
  const [editing, setEditing] = useState<Profile | "new" | null>(null)
  const [assigning, setAssigning] = useState(false)
  const [unassign, setUnassign] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const list = (profiles.data?.clouds ?? []).filter((p): p is Profile => !!p)
  const profile =
    list.find((p) => p.id === chosen) ??
    list.find((p) => p.id === assignment.data?.assignment?.cloudId) ??
    list[0]
  const reload = async () => {
    await Promise.allSettled([refetchProfiles(), refetchAssignment()])
    setRefresh((n) => n + 1)
  }
  // Lists have no generated cache tags. Bounded reads also cover reconnects and
  // changes made in another tab while retaining the server's persisted status.
  useEffect(() => {
    const timer = window.setInterval(() => {
      void refetchProfiles().catch(() => undefined)
      void refetchAssignment().catch(() => undefined)
    }, 15000)
    return () => window.clearInterval(timer)
  }, [refetchProfiles, refetchAssignment])
  return (
    <>
      {(profiles.status === "error" ||
        assignment.status === "error" ||
        !profiles.data ||
        !assignment.data) && (
        <section className="flex flex-wrap items-end gap-2">
          {scopeControls}
        </section>
      )}
      {profiles.status === "error" || assignment.status === "error" ? (
        <QueryError
          error={profiles.error ?? assignment.error}
          fallback="Cloud configuration couldn't be loaded."
          retry={reload}
        />
      ) : !profiles.data || !assignment.data ? (
        <Skeleton className="h-28" />
      ) : (
        <>
          <section className="flex flex-wrap items-end gap-2">
            {scopeControls}
            <FormField className="grid min-w-36 flex-1 gap-1 text-xs text-muted-foreground">
              <FieldLabel className="contents">
                Cloud
                <Select
                  value={profile?.id ?? ""}
                  onValueChange={(e) => setChosen(e ?? "")}
                >
                  <SelectTrigger
                    aria-label="Cloud"
                    className={"w-full min-w-0"}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {!profile && <SelectItem value="">No Clouds</SelectItem>}
                    {list.map((p) => (
                      <SelectItem key={p.id} value={p.id}>
                        {p.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FieldLabel>
            </FormField>
            {manage && (
              <>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => setEditing("new")}
                >
                  <PlusIcon />
                  New Cloud
                </Button>
                {profile && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setEditing(profile)}
                  >
                    Manage Cloud
                  </Button>
                )}
              </>
            )}
            <div className="basis-full text-xs text-muted-foreground">
              {environment.name} currently uses{" "}
              {list.find((p) => p.id === assignment.data?.assignment?.cloudId)
                ?.name ??
                (assignment.data.assignment
                  ? assignment.data.assignment.cloudId
                  : "direct service placement")}
              .
              {profile &&
                assignment.data.assignment?.cloudId !== profile.id && (
                  <>
                    {" "}
                    <Button
                      size="sm"
                      variant="link"
                      disabled={!deploy}
                      onClick={() => {
                        setUnassign(false)
                        setAssigning(true)
                      }}
                    >
                      Review assignment
                    </Button>
                  </>
                )}
              {assignment.data.assignment && (
                <Button
                  size="sm"
                  variant="link"
                  disabled={!deploy}
                  onClick={() => {
                    setUnassign(true)
                    setAssigning(true)
                  }}
                >
                  Review unassignment
                </Button>
              )}
            </div>
          </section>
          {!profile ? (
            <EmptyClouds
              title="Create your first Cloud"
              description="Group named service targets in a Cloud, then assign it to this environment."
            >
              {manage && (
                <Button size="sm" onClick={() => setEditing("new")}>
                  New Cloud
                </Button>
              )}
            </EmptyClouds>
          ) : (
            <Targets
              key={profile.id}
              projectId={projectId}
              profile={profile}
              environment={environment}
              environments={environments}
              assigned={assignment.data.assignment?.cloudId === profile.id}
              refresh={refresh}
              reload={reload}
            />
          )}
          {assigning && (
            <ChangeReview
              projectId={projectId}
              title="Review Cloud assignment"
              assignment={{
                environmentId: environment.id,
                cloudId: unassign ? "" : (profile?.id ?? ""),
                revision: assignment.data.revision,
              }}
              onClose={() => setAssigning(false)}
              onApplied={() => {
                setAssigning(false)
                void reload()
              }}
            />
          )}
          <SyncHistory
            projectId={projectId}
            environmentId={environment.id}
            refresh={refresh}
            onChanged={reload}
          />
        </>
      )}
      {editing && (
        <CloudForm
          projectId={projectId}
          profile={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={(id) => {
            setChosen(id)
            setEditing(null)
            void reload()
          }}
        />
      )}
    </>
  )
}
export function CloudForm({
  projectId,
  profile,
  onClose,
  onSaved,
}: {
  projectId: string
  profile?: Profile
  onClose: () => void
  onSaved: (id: string) => void
}) {
  const create = useMutation(api.useCloudsCreate)
  const update = useMutation(api.useCloudsUpdate)
  const remove = useMutation(api.useCloudsDelete)
  const [name, setName] = useState(profile?.name ?? "")
  const [error, setError] = useState("")
  const [confirmDelete, setConfirmDelete] = useState(false)
  const busy = create.isPending || update.isPending || remove.isPending
  const save = async () => {
    setError("")
    try {
      const result = profile
        ? await update.mutateAsync({
            path: { id: projectId, cloudId: profile.id },
            body: { name: name.trim(), revision: profile.revision },
          })
        : await create.mutateAsync({
            path: { id: projectId },
            body: { name: name.trim() },
          })
      onSaved(result.id)
    } catch (err) {
      setError(messageOf(err, "The Cloud wasn't saved."))
    }
  }
  const destroy = async () => {
    try {
      await remove.mutateAsync({
        path: { id: projectId, cloudId: profile!.id },
      })
      onSaved("")
    } catch (err) {
      setError(cloudDeletionError(err, "Cloud"))
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{profile ? "Manage Cloud" : "New Cloud"}</DialogTitle>
          <DialogDescription>
            {profile
              ? "Rename this Cloud or delete it after removing its assignments and references."
              : "Create a named set of deployment targets for this project."}
          </DialogDescription>
        </DialogHeader>
        <FormField className="grid gap-1.5">
          <FieldLabel className="contents">
            Name
            <Input
              aria-label="Cloud name"
              value={name}
              maxLength={64}
              onChange={(e) => setName(e.target.value)}
            />
          </FieldLabel>
        </FormField>
        {error && (
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <DialogFooter className="flex-wrap">
          {profile && (
            <Button
              variant="destructive"
              disabled={busy}
              onClick={() =>
                confirmDelete ? void destroy() : setConfirmDelete(true)
              }
            >
              {confirmDelete ? "Confirm delete Cloud" : "Delete Cloud"}
            </Button>
          )}
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || !name.trim()} onClick={() => void save()}>
            Save Cloud
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
function Targets({
  projectId,
  profile,
  environment,
  environments,
  assigned,
  refresh,
  reload,
}: {
  projectId: string
  profile: Profile
  environment: Environment
  environments: Environment[]
  assigned: boolean
  refresh: number
  reload: () => Promise<void>
}) {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  const targets = useQuery(api.useCloudTargetsList, {
    path: { id: projectId, cloudId: profile.id },
  })
  const { refetch: refetchTargets } = targets
  const connections = useQuery(api.useConnectionsList)
  const [query, setQuery] = useState("")
  const [editing, setEditing] = useState<Target | "new" | null>(null)
  const all = (targets.data?.targets ?? []).filter((t): t is Target => !!t)
  const accounts = connections.data?.connections ?? []
  const visible = all.filter((t) =>
    `${t.name} ${t.displayName} ${t.provider} ${(t.tags ?? []).join(" ")} ${connections.data?.connections.find((c) => c.id === t.connectionId)?.name ?? ""}`
      .toLowerCase()
      .includes(query.toLowerCase())
  )
  useEffect(() => {
    void refetchTargets().catch(() => undefined)
    const timer = window.setInterval(() => {
      void refetchTargets().catch(() => undefined)
    }, 15000)
    return () => window.clearInterval(timer)
  }, [refresh, refetchTargets])
  return (
    <section className="grid min-w-0 gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-[15px] font-semibold">Targets</h2>
        <div className="flex flex-wrap gap-2">
          <Input
            className="h-8 w-44"
            aria-label="Filter targets"
            placeholder="Filter targets"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          {manage && (
            <Button size="sm" onClick={() => setEditing("new")}>
              <PlusIcon />
              Add target
            </Button>
          )}
        </div>
      </div>
      {targets.status === "error" || connections.status === "error" ? (
        <QueryError
          error={targets.error ?? connections.error}
          fallback="Targets couldn't be loaded."
          retry={() =>
            Promise.allSettled([refetchTargets(), connections.refetch()])
          }
        />
      ) : !targets.data || !connections.data ? (
        <Skeleton className="h-24" />
      ) : !visible.length ? (
        <EmptyClouds
          title={all.length ? "No matching targets" : "No targets yet"}
          description={
            all.length
              ? "Try another name, provider or account."
              : "Add a target such as frontend or backend and choose its connected account."
          }
        >
          {all.length ? (
            <Button size="sm" variant="outline" onClick={() => setQuery("")}>
              Clear filter
            </Button>
          ) : (
            manage && (
              <Button size="sm" onClick={() => setEditing("new")}>
                Add target
              </Button>
            )
          )}
        </EmptyClouds>
      ) : (
        <TargetTable
          targets={visible}
          all={all}
          connections={accounts}
          manage={manage}
          onEdit={setEditing}
        />
      )}
      {assigned && (
        <AppliedPlacement
          projectId={projectId}
          environment={environment}
          refresh={refresh}
        />
      )}
      {assigned && (
        <ServiceAdoption
          projectId={projectId}
          environment={environment}
          environments={environments}
          targets={all}
          onSaved={reload}
        />
      )}
      {editing && (
        <TargetEditor
          key={editing === "new" ? "new" : editing.id}
          projectId={projectId}
          profile={profile}
          target={editing === "new" ? undefined : editing}
          siblingTags={cloudTags(all, editing === "new" ? undefined : editing)}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void refetchTargets()
            void reload()
          }}
        />
      )}
    </section>
  )
}

function AppliedPlacement({
  projectId,
  environment,
  refresh,
}: {
  projectId: string
  environment: Environment
  refresh: number
}) {
  const bindings = useQuery(api.useBindingsList, {
    path: { id: projectId },
    query: { environment: environment.name },
  })
  const { refetch: refetchBindings } = bindings
  const accounts = useQuery(api.useConnectionsList)
  useEffect(() => {
    void refetchBindings().catch(() => undefined)
    const timer = window.setInterval(() => {
      void refetchBindings().catch(() => undefined)
    }, 5000)
    return () => window.clearInterval(timer)
  }, [refresh, refetchBindings])
  if (bindings.status === "error")
    return (
      <QueryError
        error={bindings.error}
        fallback="Applied service accounts couldn't be loaded."
        retry={bindings.refetch}
      />
    )
  if (!bindings.data)
    return <CloudLoading>Loading applied accounts...</CloudLoading>
  const services = bindings.data.bindings.filter((b) => b.subject === "service")
  return (
    <section className="grid gap-2">
      <h3 className="text-[14px] font-semibold">
        Applied accounts in {environment.name}
      </h3>
      {!services.length ? (
        <EmptyClouds
          title="No applied services"
          description="Deploy this environment to see each service's applied account here."
        />
      ) : (
        <div className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
          {services.map((b) => (
            <div
              key={b.id}
              className="flex flex-wrap items-center justify-between gap-2 px-4 py-2.5 text-[12.5px]"
            >
              <span className="font-medium">{b.name}</span>
              <span className="break-words text-muted-foreground">
                {b.provider} ·{" "}
                {accounts.data?.connections.find((c) => c.id === b.connectionId)
                  ?.name ?? b.connectionId}{" "}
                · {b.commitSha?.slice(0, 8)}
              </span>
              {b.url && (
                <a
                  className="underline underline-offset-4"
                  href={b.url}
                  target="_blank"
                  rel="noreferrer"
                >
                  Open service
                </a>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

/**
 * TargetTable lists a Cloud's targets with the account each points at, under
 * a summary of the accounts and providers they span.
 */
export function TargetTable({
  targets: visible,
  all,
  connections: accounts,
  manage,
  onEdit: setEditing,
}: {
  targets: Target[]
  all: Target[]
  connections: Connection[]
  manage: boolean
  onEdit: (target: Target) => void
}) {
  return (
    <div className="grid gap-2.5">
      <AccountSummary targets={all} connections={accounts} />
      <Card size="sm" className="gap-0 py-0">
        <div
          aria-hidden="true"
          className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)_110px_auto] gap-4 border-b bg-subtle px-4 py-2 text-[11.5px] font-medium tracking-[0.04em] text-muted-foreground uppercase max-md:hidden"
        >
          <span>Target</span>
          <span>Account</span>
          <span>Region</span>
          <span className="w-[86px]" />
        </div>
        <ItemGroup className="gap-0">
          {[...visible]
            .sort((a, b) =>
              `${cloudOf(a, accounts)}:${a.connectionId}:${a.name}`.localeCompare(
                `${cloudOf(b, accounts)}:${b.connectionId}:${b.name}`
              )
            )
            .map((t) => {
              const account = accounts.find((c) => c.id === t.connectionId)
              const cloud = cloudOf(t, accounts)
              return (
                <Item
                  role="listitem"
                  size="sm"
                  key={t.id}
                  className={cn(
                    listItemClass,
                    "grid items-center gap-x-4 gap-y-2 py-2.5 md:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)_110px_auto]"
                  )}
                >
                  <span className="grid min-w-0 gap-0.5">
                    <span className="truncate text-[13px] font-medium">
                      {t.displayName || t.name}
                    </span>
                    {t.displayName && t.displayName !== t.name && (
                      <span className="truncate font-mono text-[11.5px] text-muted-foreground">
                        {t.name}
                      </span>
                    )}
                    {(t.tags ?? []).length > 0 && (
                      <span className="mt-1 flex flex-wrap gap-1">
                        {t.tags.map((tag) => (
                          <span
                            key={tag}
                            className="rounded-[5px] bg-brand-tint px-1.5 font-mono text-[11px] text-brand-ink"
                          >
                            {tag}
                          </span>
                        ))}
                      </span>
                    )}
                  </span>
                  <span className="flex min-w-0 items-center gap-2.5">
                    <CloudGlyph cloud={cloud} size="sm" />
                    <span className="grid min-w-0 gap-0.5">
                      <span className="truncate text-[13px]">
                        {t.runtime === "server"
                          ? `Server pool ${t.poolId}`
                          : (account?.name ?? "An account that was removed")}
                      </span>
                      <span className="truncate text-xs text-muted-foreground">
                        {cloudLabel(cloud)}
                        {account?.account && ` · ${account.account}`}
                      </span>
                    </span>
                  </span>
                  <span className="truncate font-mono text-xs text-muted-foreground">
                    {t.region || "Default"}
                  </span>
                  <Button
                    size="sm"
                    variant="outline"
                    className="justify-self-start md:justify-self-end"
                    onClick={() => setEditing(t)}
                  >
                    {manage ? "Edit target" : "View target"}
                  </Button>
                </Item>
              )
            })}
        </ItemGroup>
      </Card>
    </div>
  )
}

/** cloudTags is every tag on a Cloud's targets except the one being edited. */
export function cloudTags(targets: Target[], except?: Target): string[] {
  return [
    ...new Set(
      targets.filter((t) => t.id !== except?.id).flatMap((t) => t.tags ?? [])
    ),
  ].sort()
}
