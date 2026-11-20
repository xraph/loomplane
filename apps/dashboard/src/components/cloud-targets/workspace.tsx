import { Card } from "@/components/ui/card"
import { Item, ItemGroup } from "@/components/ui/item"
import { listItemClass } from "@/components/cloud-targets/infrastructure-ui"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { type ReactNode, useState } from "react"
import { ChevronRightIcon, PlusIcon } from "lucide-react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  DefaultPlacement,
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
import { StatusPill } from "@/components/runs"

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
/**
 * CloudWorkspace is a project's Clouds: every Cloud granted to it, with the
 * default first. Every environment deploys through the default Cloud, so
 * nothing here is per environment; an environment differs only where
 * loomplane.yaml, or the deploy screen, says so.
 */
export function CloudWorkspace({
  projectId,
  cloudId,
}: {
  projectId: string
  /** The Cloud to open on, such as the one a Cloud's own page is about. */
  cloudId?: string
}) {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  const review = manage && can("deployment:approve")
  const profiles = useQuery(api.useCloudsList, { path: { id: projectId } })
  const current = useQuery(api.useDefaultCloudGet, { path: { id: projectId } })
  const [open, setOpen] = useState<string | undefined>(cloudId)
  const [editing, setEditing] = useState<Profile | "new" | null>(null)
  const [making, setMaking] = useState<string | null>(null)
  if (profiles.status === "error" || current.status === "error")
    return (
      <QueryError
        error={profiles.error ?? current.error}
        fallback="This project's Clouds couldn't be loaded."
        retry={() =>
          Promise.allSettled([profiles.refetch(), current.refetch()])
        }
      />
    )
  if (!profiles.data || !current.data) return <Skeleton className="h-32" />
  const defaultId = current.data.cloud?.cloudId
  const list = (profiles.data.clouds ?? [])
    .filter((p): p is Profile => !!p)
    .sort((a, b) =>
      a.id === defaultId
        ? -1
        : b.id === defaultId
          ? 1
          : a.name.localeCompare(b.name)
    )
  const expanded = open ?? defaultId ?? list[0]?.id
  return (
    <div className="grid gap-5">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid max-w-prose gap-1">
          <h2 className="text-[15px] font-semibold">Clouds</h2>
          <p className="text-[13px] text-muted-foreground">
            Every environment deploys through the default Cloud. Put a service
            on one of its targets here, or with{" "}
            <code className="font-mono text-[12px]">cloud_target</code> in
            loomplane.yaml.
          </p>
        </div>
        {manage && (
          <Button size="sm" variant="outline" onClick={() => setEditing("new")}>
            <PlusIcon />
            New Cloud
          </Button>
        )}
      </header>
      {!list.length ? (
        <EmptyClouds
          title="No Clouds yet"
          description="A Cloud groups the targets your services deploy to. The first one you add becomes this project's default."
        >
          {manage && (
            <Button size="sm" onClick={() => setEditing("new")}>
              New Cloud
            </Button>
          )}
        </EmptyClouds>
      ) : (
        <div className="grid gap-3">
          {!defaultId && (
            <Alert>
              <AlertDescription>
                This project has no default Cloud, so services can&apos;t name a
                target yet. Make one of these its default.
              </AlertDescription>
            </Alert>
          )}
          {list.map((profile) => (
            <CloudCard
              key={profile.id}
              projectId={projectId}
              profile={profile}
              isDefault={profile.id === defaultId}
              fallback={
                profile.id === defaultId
                  ? current.data?.cloud?.defaultPlacement
                  : undefined
              }
              expanded={profile.id === expanded}
              onToggle={() =>
                setOpen(profile.id === expanded ? "" : profile.id)
              }
              onMakeDefault={review ? () => setMaking(profile.id) : undefined}
              onManage={manage ? () => setEditing(profile) : undefined}
            />
          ))}
        </div>
      )}
      {making !== null && (
        <ChangeReview
          projectId={projectId}
          title="Change the default Cloud"
          defaultCloud={{ cloudId: making, revision: current.data.revision }}
          onClose={() => setMaking(null)}
          onApplied={() => setMaking(null)}
        />
      )}
      <SyncHistory
        projectId={projectId}
        refresh={0}
        onChanged={async () => {}}
      />
      {editing && (
        <CloudForm
          projectId={projectId}
          profile={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={(id) => {
            if (id) setOpen(id)
            setEditing(null)
          }}
        />
      )}
    </div>
  )
}

/**
 * CloudCard is one Cloud granted to the project: a one-line summary that
 * opens to its targets, and for the default Cloud, where each service runs.
 */
function CloudCard({
  projectId,
  profile,
  isDefault,
  fallback,
  expanded,
  onToggle,
  onMakeDefault,
  onManage,
}: {
  projectId: string
  profile: Profile
  isDefault: boolean
  fallback?: DefaultPlacement | null
  expanded: boolean
  onToggle: () => void
  onMakeDefault?: () => void
  onManage?: () => void
}) {
  const targets = useQuery(api.useCloudTargetsList, {
    path: { id: projectId, cloudId: profile.id },
  })
  const count = (targets.data?.targets ?? []).filter(Boolean).length
  return (
    <Card size="sm" className="gap-0 py-0">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-2 text-left"
          aria-expanded={expanded}
          onClick={onToggle}
        >
          <ChevronRightIcon
            className={cn(
              "size-4 shrink-0 text-muted-foreground transition-transform",
              expanded && "rotate-90"
            )}
          />
          <span className="truncate text-[14px] font-medium">
            {profile.name}
          </span>
          {isDefault && <StatusPill tone="live">Default</StatusPill>}
          <span className="text-xs text-muted-foreground">
            {targets.data
              ? `${count} ${count === 1 ? "target" : "targets"}`
              : ""}
            {profile.defaultPlacement ? " · default placement" : ""}
          </span>
        </button>
        <div className="flex gap-2">
          {!isDefault && onMakeDefault && (
            <Button size="sm" variant="outline" onClick={onMakeDefault}>
              Make default
            </Button>
          )}
          {onManage && (
            <Button size="sm" variant="ghost" onClick={onManage}>
              Manage
            </Button>
          )}
        </div>
      </div>
      {expanded && (
        <div className="grid gap-4 border-t px-4 py-4">
          <Targets projectId={projectId} profile={profile} />
          {isDefault && (
            <ServiceAdoption
              projectId={projectId}
              targets={(targets.data?.targets ?? []).filter(
                (t): t is Target => !!t
              )}
              fallback={fallback}
            />
          )}
        </div>
      )}
    </Card>
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
              ? "Rename this Cloud, or delete it once no project uses it."
              : "A named set of deployment targets. A project's first Cloud becomes its default."}
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
}: {
  projectId: string
  profile: Profile
}) {
  const { can } = useOrganization()
  const manage = can("cloud_account:manage")
  const targets = useQuery(api.useCloudTargetsList, {
    path: { id: projectId, cloudId: profile.id },
  })
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
            Promise.allSettled([targets.refetch(), connections.refetch()])
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
      {editing && (
        <TargetEditor
          key={editing === "new" ? "new" : editing.id}
          projectId={projectId}
          profile={profile}
          target={editing === "new" ? undefined : editing}
          siblingTags={cloudTags(all, editing === "new" ? undefined : editing)}
          onClose={() => setEditing(null)}
          onSaved={() => setEditing(null)}
        />
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
