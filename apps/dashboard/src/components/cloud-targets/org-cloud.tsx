// A Cloud the organization owns: its targets, and the projects allowed to
// place services on them. A project gets the Cloud with a chosen set of its
// targets, so one Cloud can serve many projects, each on only what it needs.
import { PlusIcon } from "lucide-react"
import { useState } from "react"

import { messageOf, statusOf } from "@/client/errors"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Profile,
  Project,
  ProjectGrant,
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { CloudGlyph } from "@/components/cloud-glyph"
import { QueryError } from "@/components/query-error"
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
  EmptyTitle,
} from "@/components/ui/empty"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { cloudLabel } from "@/lib/clouds"
import { useOrganization } from "@/lib/organization"
import { ProjectIcon, projectStyle } from "@/lib/project-style"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

import { Choice, TargetEditor } from "./target-editor"
import { TargetTable, cloudTags, selectClass } from "./workspace"

export function OrganizationCloud({ cloud }: { cloud: Profile }) {
  const targets = useQuery(api.useOrganizationCloudTargetsList, {
    path: { cloudId: cloud.id },
  })
  const list = (targets.data?.targets ?? []).filter((t): t is Target => !!t)
  return (
    <>
      <DefaultPlacementCard cloud={cloud} targets={list} />
      <Targets cloud={cloud} />
      <ProjectAccess cloud={cloud} targets={list} loaded={!!targets.data} />
    </>
  )
}

function Targets({ cloud }: { cloud: Profile }) {
  // The same cached read OrganizationCloud makes.
  const targets = useQuery(api.useOrganizationCloudTargetsList, {
    path: { cloudId: cloud.id },
  })
  const list = (targets.data?.targets ?? []).filter((t): t is Target => !!t)
  const { can } = useOrganization()
  const manage = can("organization_cloud:manage")
  const connections = useQuery(api.useConnectionsList)
  const [editing, setEditing] = useState<Target | "new" | null>(null)
  return (
    <section aria-label="Targets" className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="grid gap-0.5">
          <h2 className="text-[15px] font-semibold">Targets</h2>
          <p className="text-[12.5px] text-muted-foreground">
            Each target is one place services run: a provider, one of your
            accounts there, and a region.
          </p>
        </div>
        {manage && (
          <Button onClick={() => setEditing("new")}>
            <PlusIcon />
            Add target
          </Button>
        )}
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
        <Skeleton className="h-28 rounded-xl" />
      ) : list.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyTitle>No targets yet</EmptyTitle>
            <EmptyDescription>
              Add a target such as frontend or backend and pick the account it
              runs on. Targets can use different providers, and several accounts
              of the same one.
            </EmptyDescription>
          </EmptyHeader>
          {manage && (
            <EmptyContent>
              <Button onClick={() => setEditing("new")}>Add target</Button>
            </EmptyContent>
          )}
        </Empty>
      ) : (
        <TargetTable
          targets={list}
          all={list}
          connections={connections.data.connections}
          manage={manage}
          onEdit={setEditing}
        />
      )}
      {editing && (
        <TargetEditor
          key={editing === "new" ? "new" : editing.id}
          projectId=""
          profile={cloud}
          target={editing === "new" ? undefined : editing}
          siblingTags={cloudTags(list, editing === "new" ? undefined : editing)}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void targets.refetch()
          }}
        />
      )}
    </section>
  )
}

function ProjectAccess({
  cloud,
  targets,
  loaded,
}: {
  cloud: Profile
  targets: Target[]
  loaded: boolean
}) {
  const { can } = useOrganization()
  const manage = can("organization_cloud:manage")
  const grants = useQuery(api.useOrganizationCloudsListGrants, {
    path: { cloudId: cloud.id },
  })
  const projects = useQuery(api.useProjectsList)
  const [editing, setEditing] = useState<ProjectGrant | "new" | null>(null)
  const active = (grants.data?.grants ?? []).filter(
    (g): g is ProjectGrant => !!g && !g.revoked
  )
  const projectOf = (id: string) =>
    projects.data?.projects.find((p) => p.id === id)
  const available = (projects.data?.projects ?? []).filter(
    (p) => !active.some((g) => g.projectId === p.id)
  )
  return (
    <section aria-label="Projects" className="grid gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="grid gap-0.5">
          <h2 className="text-[15px] font-semibold">Projects</h2>
          <p className="text-[12.5px] text-muted-foreground">
            Projects that can place services on this Cloud, and the targets each
            one may use.
          </p>
        </div>
        {manage && (
          <Button
            variant="outline"
            disabled={!loaded || targets.length === 0 || available.length === 0}
            onClick={() => setEditing("new")}
          >
            <PlusIcon />
            Give a project access
          </Button>
        )}
      </div>
      {grants.status === "error" || projects.status === "error" ? (
        <QueryError
          error={grants.error ?? projects.error}
          fallback="Project access couldn't be loaded."
          retry={() =>
            Promise.allSettled([grants.refetch(), projects.refetch()])
          }
        />
      ) : !grants.data || !projects.data ? (
        <Skeleton className="h-20 rounded-xl" />
      ) : active.length === 0 ? (
        <Empty>
          <EmptyHeader>
            <EmptyTitle>No project uses this Cloud yet</EmptyTitle>
            <EmptyDescription>
              {targets.length
                ? "Give a project access to some or all of its targets, then assign the Cloud to one of that project's environments."
                : "Add a target first. A project is given access to specific targets."}
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : (
        <ul className="divide-y overflow-hidden rounded-xl bg-card ring-1 ring-border">
          {active.map((g) => {
            const p = projectOf(g.projectId)
            const s = p ? projectStyle(p) : null
            const allowed = targets.filter((t) => g.targetIds.includes(t.id))
            return (
              <li
                key={g.projectId}
                className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3"
              >
                <span className="flex min-w-0 flex-[1_1_200px] items-center gap-2.5">
                  {s && <ProjectIcon icon={s.icon} color={s.color} size="sm" />}
                  {p ? (
                    <Link
                      to="/projects/$id"
                      params={{ id: p.id }}
                      className="truncate text-[13px] font-medium hover:underline"
                    >
                      {p.name}
                    </Link>
                  ) : (
                    <span className="truncate text-[13px] text-muted-foreground">
                      A project you can&apos;t see
                    </span>
                  )}
                </span>
                <span className="flex min-w-0 flex-[2_1_260px] flex-wrap gap-1.5">
                  {allowed.length ? (
                    allowed.map((t) => (
                      <span
                        key={t.id}
                        className="rounded-md bg-muted px-2 py-0.5 text-[12px]"
                      >
                        {t.displayName || t.name}
                      </span>
                    ))
                  ) : (
                    <span className="text-[12.5px] text-muted-foreground">
                      No targets allowed
                    </span>
                  )}
                </span>
                {manage && (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => setEditing(g)}
                  >
                    Edit access
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {editing && (
        <GrantDialog
          cloud={cloud}
          targets={targets}
          grant={editing === "new" ? undefined : editing}
          projects={editing === "new" ? available : []}
          project={editing === "new" ? undefined : projectOf(editing.projectId)}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void grants.refetch()
          }}
        />
      )}
    </section>
  )
}

/** GrantDialog picks a project and the targets it may place services on. */
function GrantDialog({
  cloud,
  targets,
  grant,
  projects,
  project,
  onClose,
  onSaved,
}: {
  cloud: Profile
  targets: Target[]
  grant?: ProjectGrant
  /** Projects that don't have access yet, when granting a new one. */
  projects: Project[]
  /** The project being edited. */
  project?: Project
  onClose: () => void
  onSaved: () => void
}) {
  const save = useMutation(api.useOrganizationCloudsGrantProject)
  const revoke = useMutation(api.useOrganizationCloudsRevokeProject)
  const [projectId, setProjectId] = useState(
    grant?.projectId ?? projects[0]?.id ?? ""
  )
  const [chosen, setChosen] = useState<string[]>(
    grant?.targetIds ?? targets.map((t) => t.id)
  )
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState("")
  const busy = save.isPending || revoke.isPending
  const toggle = (id: string) =>
    setChosen((c) => (c.includes(id) ? c.filter((x) => x !== id) : [...c, id]))
  const submit = async () => {
    setError("")
    try {
      await save.mutateAsync({
        path: { cloudId: cloud.id, projectId },
        body: { targetIds: chosen },
      })
      onSaved()
    } catch (err) {
      setError(messageOf(err, "Access wasn't saved."))
    }
  }
  const remove = async () => {
    setError("")
    try {
      await revoke.mutateAsync({ path: { cloudId: cloud.id, projectId } })
      onSaved()
    } catch (err) {
      setError(messageOf(err, "Access wasn't removed."))
    }
  }
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {grant
              ? `${project?.name ?? "Project"}'s access`
              : "Give a project access"}
          </DialogTitle>
          <DialogDescription>
            The project can place services on the targets you tick. Its
            environments still choose to use {cloud.name} themselves.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-4">
          {!grant && (
            <label className="grid gap-1 text-xs">
              Project
              <select
                className={selectClass}
                aria-label="Project"
                value={projectId}
                disabled={busy}
                onChange={(e) => setProjectId(e.target.value)}
              >
                {projects.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </label>
          )}
          <fieldset className="grid gap-1.5">
            <legend className="mb-1.5 text-xs">Targets it may use</legend>
            {targets.map((t) => {
              const on = chosen.includes(t.id)
              return (
                <label
                  key={t.id}
                  className={cn(
                    "flex h-11 cursor-pointer items-center gap-3 rounded-xl bg-card px-3 ring-1 transition-shadow",
                    on ? "ring-2 ring-primary" : "ring-border"
                  )}
                >
                  <input
                    type="checkbox"
                    checked={on}
                    disabled={busy}
                    onChange={() => toggle(t.id)}
                    className="size-4 accent-[var(--primary)]"
                  />
                  <span className="min-w-0 flex-1 truncate text-[13px] font-medium">
                    {t.displayName || t.name}
                  </span>
                  <span className="truncate font-mono text-[11.5px] text-muted-foreground">
                    {t.name}
                  </span>
                </label>
              )
            })}
          </fieldset>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
          )}
        </div>
        <DialogFooter className="flex-wrap">
          {grant && (
            <Button
              variant="outline"
              className="mr-auto text-destructive"
              disabled={busy}
              onClick={() => (confirming ? void remove() : setConfirming(true))}
            >
              {confirming ? "Confirm remove access" : "Remove access"}
            </Button>
          )}
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || !projectId || chosen.length === 0}
            onClick={() => void submit()}
          >
            {grant ? "Save access" : "Give access"}
          </Button>
        </DialogFooter>
        {confirming && (
          <p className="text-xs text-muted-foreground">
            Services already running keep running. The project just can&apos;t
            place new ones on {cloud.name}.
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}

/**
 * DefaultPlacementCard is where a Cloud's services go when they don't name
 * or tag a target. With no targets it's the Cloud's one provider and account.
 */
function DefaultPlacementCard({
  cloud,
  targets,
}: {
  cloud: Profile
  targets: Target[]
}) {
  const { can } = useOrganization()
  const manage = can("organization_cloud:manage")
  const connections = useQuery(api.useConnectionsList)
  const providers = useQuery(api.useProvidersList)
  const set = useMutation(api.useOrganizationCloudsSetDefaultPlacement)
  // The write returns the Cloud at its new revision; show that until the
  // page's own read catches up.
  const [saved, setSaved] = useState<Profile | null>(null)
  const profile = saved && saved.revision >= cloud.revision ? saved : cloud
  const current = profile.defaultPlacement
  const [editing, setEditing] = useState(false)
  const [confirmClear, setConfirmClear] = useState(false)
  const [error, setError] = useState("")
  const account = (connections.data?.connections ?? []).find(
    (c) => c.id === current?.connectionId
  )
  const provider = (providers.data?.providers ?? []).find(
    (p) => p.name === current?.provider
  )
  const save = async (next: Profile["defaultPlacement"]) => {
    setError("")
    try {
      const out = await set.mutateAsync({
        path: { cloudId: cloud.id },
        body: { defaultPlacement: next, revision: profile.revision },
      })
      setSaved(out)
      setEditing(false)
      setConfirmClear(false)
      return true
    } catch (err) {
      setError(
        statusOf(err) === 409
          ? "An environment uses this Cloud, so its default can't move without a reviewed sync. Unassign the Cloud from those environments first, then change it."
          : messageOf(err, "The default wasn't saved.")
      )
      return false
    }
  }
  return (
    <section
      aria-label="Default provider"
      className="grid gap-3 rounded-[14px] bg-card p-4 ring-1 ring-border"
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="grid max-w-[64ch] gap-0.5">
          <h2 className="text-[15px] font-semibold">Default provider</h2>
          <p className="text-[12.5px] leading-relaxed text-muted-foreground">
            {targets.length === 0
              ? "This Cloud has no targets, so every service on it runs here. A Cloud without targets uses exactly one provider and account."
              : "Services that don't name or tag a target run here. Adding a target doesn't move them."}
          </p>
        </div>
        {manage && (
          <div className="flex flex-wrap gap-2">
            {current && (
              <Button
                variant="outline"
                className="text-destructive"
                disabled={set.isPending}
                onClick={() =>
                  confirmClear ? void save(null) : setConfirmClear(true)
                }
              >
                {confirmClear ? "Confirm clear default" : "Clear"}
              </Button>
            )}
            <Button
              variant={current ? "outline" : "default"}
              disabled={set.isPending}
              onClick={() => {
                setError("")
                setEditing(true)
              }}
            >
              {current ? "Change" : "Set default provider"}
            </Button>
          </div>
        )}
      </div>
      {current ? (
        <div className="flex min-w-0 items-center gap-3 rounded-xl bg-subtle px-3 py-2.5">
          <CloudGlyph cloud={account?.cloud ?? current.provider} size="md" />
          <span className="grid min-w-0 gap-0.5">
            <span className="truncate text-[13px] font-medium">
              {account?.name ?? "An account that was removed"}
            </span>
            <span className="truncate text-xs text-muted-foreground">
              {provider?.title ??
                cloudLabel(account?.cloud ?? current.provider)}
              {account?.account && ` · ${account.account}`}
              {` · ${current.region || "provider default region"}`}
            </span>
          </span>
        </div>
      ) : (
        <p className="text-[13px] text-muted-foreground">
          No default yet.{" "}
          {targets.length === 0
            ? "Services on this Cloud have nowhere to run until you set one or add a target."
            : "Services that don't name or tag a target have nowhere to run."}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        Only projects given access to the whole Cloud use the default. A project
        limited to some targets uses just those.
      </p>
      {confirmClear && (
        <p className="text-xs text-muted-foreground">
          Clearing leaves services without a target of their own with nowhere to
          run.{" "}
          <button
            type="button"
            className="underline underline-offset-4"
            onClick={() => setConfirmClear(false)}
          >
            Keep it
          </button>
        </p>
      )}
      {error && !editing && (
        <p role="alert" className="text-[13px] text-destructive">
          {error}
        </p>
      )}
      {editing && (
        <DefaultPlacementDialog
          cloud={cloud}
          current={current}
          busy={set.isPending}
          error={error}
          onClose={() => setEditing(false)}
          onSave={(next) => void save(next)}
        />
      )}
    </section>
  )
}

export type Placement = {
  provider: string
  connectionId: string
  region: string
}

export const noPlacement: Placement = {
  provider: "",
  connectionId: "",
  region: "",
}

/**
 * PlacementPicker chooses one provider, one ready account there and a
 * region. onChange reports whether the choice is complete enough to save.
 */
export function PlacementPicker({
  value,
  onChange,
  disabled = false,
  label = "Default",
}: {
  value: Placement
  onChange: (next: Placement, complete: boolean) => void
  disabled?: boolean
  /** Names the radio groups, such as "Default provider". */
  label?: string
}) {
  const providers = useQuery(api.useProvidersList)
  const connections = useQuery(api.useConnectionsList)
  const all = providers.data?.providers ?? []
  const ready = (cloudKey?: string) =>
    (connections.data?.connections ?? []).filter(
      (c) => c.status === "ready" && c.cloud === cloudKey
    )
  const selected = all.find((p) => p.name === value.provider)
  const accounts = ready(selected?.cloud)
  const emit = (next: Placement) => {
    const cloud = all.find((p) => p.name === next.provider)?.cloud
    onChange(
      next,
      !!next.provider && ready(cloud).some((c) => c.id === next.connectionId)
    )
  }
  if (providers.status === "error" || connections.status === "error")
    return (
      <QueryError
        error={providers.error ?? connections.error}
        fallback="Providers and accounts couldn't be loaded."
        retry={() =>
          Promise.allSettled([providers.refetch(), connections.refetch()])
        }
      />
    )
  if (!providers.data || !connections.data)
    return <Skeleton className="h-40 rounded-xl" />
  return (
    <div className="grid gap-4">
      <div
        role="radiogroup"
        aria-label={`${label} provider`}
        className="grid gap-2 sm:grid-cols-2"
      >
        {all
          .filter((p) => p.available && p.runs?.length)
          .map((p) => {
            const n = ready(p.cloud).length
            return (
              <Choice
                key={p.name}
                checked={value.provider === p.name}
                disabled={disabled}
                onSelect={() => {
                  if (value.provider === p.name) return
                  emit({ provider: p.name, connectionId: "", region: "" })
                }}
              >
                <CloudGlyph cloud={p.cloud} size="sm" />
                <span className="grid min-w-0 gap-0.5">
                  <span className="truncate text-[13px] font-medium">
                    {p.title}
                  </span>
                  <span className="truncate text-xs text-muted-foreground">
                    {n
                      ? `${n} ${n === 1 ? "account" : "accounts"} ready`
                      : "No account yet"}
                  </span>
                </span>
              </Choice>
            )
          })}
      </div>
      {selected && (
        <div className="grid gap-1.5">
          <span className="text-xs">Account</span>
          {accounts.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">
              No ready {selected.title} account.{" "}
              <Link
                to="/integrations"
                className="font-medium text-brand-ink hover:underline"
              >
                Connect one
              </Link>
            </p>
          ) : (
            <div
              role="radiogroup"
              aria-label={`${label} account`}
              className="grid gap-2"
            >
              {accounts.map((c) => (
                <Choice
                  key={c.id}
                  checked={value.connectionId === c.id}
                  disabled={disabled}
                  onSelect={() => emit({ ...value, connectionId: c.id })}
                >
                  <CloudGlyph cloud={c.cloud} size="sm" />
                  <span className="grid min-w-0 flex-1 gap-0.5">
                    <span className="truncate text-[13px] font-medium">
                      {c.name}
                    </span>
                    <span className="truncate font-mono text-[11.5px] text-muted-foreground">
                      {c.account}
                    </span>
                  </span>
                </Choice>
              ))}
            </div>
          )}
        </div>
      )}
      {selected && (
        <label className="grid gap-1 text-xs">
          Region
          {selected.regions?.length ? (
            <select
              className={selectClass}
              aria-label={`${label} region`}
              disabled={disabled}
              value={value.region}
              onChange={(e) => emit({ ...value, region: e.target.value })}
            >
              <option value="">Provider default</option>
              {selected.regions.map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            </select>
          ) : (
            <Input
              aria-label={`${label} region`}
              disabled={disabled}
              value={value.region}
              onChange={(e) => emit({ ...value, region: e.target.value })}
              placeholder="Provider default"
            />
          )}
        </label>
      )}
    </div>
  )
}

function DefaultPlacementDialog({
  cloud,
  current,
  busy,
  error,
  onClose,
  onSave,
}: {
  cloud: Profile
  current: Profile["defaultPlacement"]
  busy: boolean
  error: string
  onClose: () => void
  onSave: (next: NonNullable<Profile["defaultPlacement"]>) => void
}) {
  const [value, setValue] = useState<Placement>(current ?? noPlacement)
  const [complete, setComplete] = useState(!!current)
  const unchanged =
    !!current &&
    current.provider === value.provider &&
    current.connectionId === value.connectionId &&
    current.region === value.region
  return (
    <Dialog open onOpenChange={(open) => !open && !busy && onClose()}>
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Default provider for {cloud.name}</DialogTitle>
          <DialogDescription>
            One provider and one of your accounts there. Services with no target
            of their own run on it.
          </DialogDescription>
        </DialogHeader>
        <PlacementPicker
          value={value}
          disabled={busy}
          onChange={(next, ok) => {
            setValue(next)
            setComplete(ok)
          }}
        />
        {error && (
          <p role="alert" className="text-[13px] text-destructive">
            {error}
          </p>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || unchanged || !complete}
            onClick={() => onSave({ ...value, region: value.region.trim() })}
          >
            {current ? "Save default" : "Set default"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
