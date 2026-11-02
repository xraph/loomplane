// A Cloud's shared settings dialogs and sections: the projects allowed on
// it, and its default. A project gets the Cloud with a chosen set of its
// targets, so one Cloud can serve many projects, each on only what it needs.
import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Field as FormField,
  FieldLabel,
  FieldSet,
  FieldLegend,
} from "@/components/ui/field"
import { AsteriskIcon } from "lucide-react"
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
import {
  ActionsCell,
  Cell,
  CloudCallout,
  CloudConfirm,
  CloudTable,
  CloudTableRow,
  NameCell,
  RowMenu,
} from "@/components/cloud-targets/cloud-page"
import {
  CloudEmpty,
  CloudSectionHead,
} from "@/components/cloud-targets/infrastructure-ui"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { DropdownMenuItem } from "@/components/ui/dropdown-menu"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { cloudLabel } from "@/lib/clouds"
import { useOrganization } from "@/lib/organization"
import { Link } from "@/lib/router"
import { cn } from "@/lib/utils"

import { ChangeReview } from "./change-review"
import { Choice } from "./target-editor"

/** GrantDialog picks a project and the targets it may place services on. */
export function GrantDialog({
  cloud,
  targets,
  grant,
  projects,
  project,
  onClose,
  onSaved,
  onRemove,
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
  /** Removes the grant being edited; the caller asks to confirm first. */
  onRemove?: () => void
}) {
  const save = useMutation(api.useOrganizationCloudsGrantProject)
  const [projectId, setProjectId] = useState(
    grant?.projectId ?? projects[0]?.id ?? ""
  )
  const [chosen, setChosen] = useState<string[]>(
    grant?.targetIds ?? targets.map((t) => t.id)
  )
  const [wholeCloud, setWholeCloud] = useState(
    grant ? grant.targetIds.length === 0 : targets.length === 0
  )
  const [error, setError] = useState("")
  const busy = save.isPending
  const toggle = (id: string) =>
    setChosen((c) => (c.includes(id) ? c.filter((x) => x !== id) : [...c, id]))
  const submit = async () => {
    setError("")
    try {
      await save.mutateAsync({
        path: { cloudId: cloud.id, projectId },
        body: { targetIds: wholeCloud ? [] : chosen },
      })
      onSaved()
    } catch (err) {
      setError(messageOf(err, "Access wasn't saved."))
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
            <FormField className="grid gap-1 text-xs">
              <FieldLabel className="contents">
                Project
                <Select
                  value={projectId}
                  disabled={busy}
                  onValueChange={(e) => setProjectId(e ?? "")}
                >
                  <SelectTrigger
                    aria-label="Project"
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
          )}
          <FormField
            orientation="horizontal"
            className="flex items-center gap-2 text-xs"
          >
            <FieldLabel className="contents">
              <Checkbox
                checked={wholeCloud}
                disabled={busy}
                onCheckedChange={(e) => setWholeCloud(e)}
              />
              Whole Cloud, including its default and future targets
            </FieldLabel>
          </FormField>
          <FieldSet
            disabled={wholeCloud}
            className="grid gap-1.5 disabled:opacity-50"
          >
            <FieldLegend className="mb-1.5 text-xs">
              Targets it may use
            </FieldLegend>
            {targets.map((t) => {
              const on = chosen.includes(t.id)
              return (
                <FormField
                  orientation="horizontal"
                  key={t.id}
                  className={cn(
                    "flex h-11 cursor-pointer items-center gap-3 rounded-xl bg-card px-3 ring-1 transition-shadow",
                    on ? "ring-2 ring-primary" : "ring-border"
                  )}
                >
                  <FieldLabel className="contents">
                    <Checkbox
                      checked={on}
                      disabled={busy || wholeCloud}
                      onCheckedChange={() => toggle(t.id)}
                    />
                    <span className="min-w-0 flex-1 truncate text-[13px] font-medium">
                      {t.displayName || t.name}
                    </span>
                    <span className="truncate font-mono text-[11.5px] text-muted-foreground">
                      {t.name}
                    </span>
                  </FieldLabel>
                </FormField>
              )
            })}
          </FieldSet>
          {error && <CloudCallout tone="error" title={error} />}
        </div>
        <DialogFooter className="flex-wrap">
          {grant && onRemove && (
            <Button
              variant="outline"
              className="mr-auto text-destructive"
              disabled={busy}
              onClick={onRemove}
            >
              Remove access
            </Button>
          )}
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={
              busy || !projectId || (!wholeCloud && chosen.length === 0)
            }
            onClick={() => void submit()}
          >
            {grant ? "Save access" : "Give access"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/**
 * DefaultPlacementSection is where a Cloud's services go when they don't name
 * or tag a target. With no targets it's the Cloud's one provider and account.
 * Its row shares the targets table's columns so the two line up.
 */
export function DefaultPlacementSection({
  cloud,
  targets,
  columns,
  services,
}: {
  cloud: Profile
  targets: Target[]
  /** The targets table's grid template. */
  columns: string
  /** Running services on the default; undefined while usage loads. */
  services?: number
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
  const [pendingDefault, setPendingDefault] = useState<
    Placement | null | undefined
  >(undefined)
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
    <section aria-label="Default" className="grid gap-2.5">
      <CloudSectionHead
        title="Default"
        description="Where a service goes when it names no target."
      >
        {manage && (
          <Button
            size="sm"
            variant="outline"
            disabled={set.isPending}
            onClick={() => {
              setError("")
              setEditing(true)
            }}
          >
            {current ? "Change" : "Set default"}
          </Button>
        )}
      </CloudSectionHead>
      {current ? (
        <CloudTable
          label="Default"
          columns={columns}
          head={["Target", "Runs on", "Tags", "Services", ""]}
        >
          <CloudTableRow>
            <NameCell
              tile={<AsteriskIcon />}
              name="Default"
              meta={
                targets.length === 0
                  ? "Every service on this Cloud"
                  : "Services with no target"
              }
            />
            <Cell>
              <span className="flex min-w-0 items-center gap-2.5">
                <CloudGlyph
                  cloud={account?.cloud ?? current.provider}
                  size="sm"
                />
                <span className="grid min-w-0 gap-0.5">
                  {account ? (
                    <Link
                      to="/clouds/accounts/$id"
                      params={{ id: account.id }}
                      className="truncate text-[13px] font-medium hover:underline"
                    >
                      {account.name}
                    </Link>
                  ) : (
                    <span className="truncate text-[13px]">
                      An account that was removed
                    </span>
                  )}
                  <span className="truncate text-xs text-muted-foreground">
                    {provider?.title ??
                      cloudLabel(account?.cloud ?? current.provider)}
                    {account?.account && ` · ${account.account}`}
                    {` · ${current.region || "provider default region"}`}
                  </span>
                </span>
              </span>
            </Cell>
            <Cell />
            <Cell className="text-[12.5px] text-muted-foreground">
              {services === undefined ? (
                "…"
              ) : (
                <>
                  <span className="font-mono text-foreground tabular-nums">
                    {services}
                  </span>{" "}
                  running
                </>
              )}
            </Cell>
            <ActionsCell>
              {manage && (
                <RowMenu label="More default actions">
                  <DropdownMenuItem
                    variant="destructive"
                    disabled={set.isPending}
                    onClick={() => setConfirmClear(true)}
                  >
                    Clear default
                  </DropdownMenuItem>
                </RowMenu>
              )}
            </ActionsCell>
          </CloudTableRow>
        </CloudTable>
      ) : (
        <CloudEmpty kind="compact" title="No default yet">
          {targets.length === 0
            ? "With no targets, every service on it runs here. Until you set one, services on this Cloud have nowhere to run."
            : "Services that don't name or tag a target have nowhere to run."}
        </CloudEmpty>
      )}
      {error && !editing && <CloudCallout tone="error" title={error} />}
      <CloudConfirm
        open={confirmClear}
        onOpenChange={setConfirmClear}
        title="Clear the default?"
        confirmLabel="Clear default"
        onConfirm={() => {
          setConfirmClear(false)
          setPendingDefault(null)
        }}
      >
        Services without a target of their own will have nowhere to run. You
        review the change before it applies.
      </CloudConfirm>
      {pendingDefault !== undefined && (
        <ChangeReview
          projectId=""
          title={
            pendingDefault
              ? "Review default switch"
              : "Review clearing the default"
          }
          change={{
            cloudId: cloud.id,
            default: true,
            revision: profile.revision,
            ...(pendingDefault ?? {
              provider: "",
              connectionId: "",
              region: "",
            }),
          }}
          onClose={() => setPendingDefault(undefined)}
          onApplied={() => {
            setPendingDefault(undefined)
            void api
              .useOrganizationCloudsGet({ path: { cloudId: cloud.id } })
              .refetch()
              .then(setSaved)
          }}
        />
      )}
      {editing && (
        <DefaultPlacementDialog
          cloud={cloud}
          current={current}
          busy={set.isPending}
          error={error}
          onClose={() => setEditing(false)}
          onSave={(next) => {
            if (current) {
              setPendingDefault(next)
              setEditing(false)
            } else void save(next)
          }}
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
        <FormField className="grid gap-1 text-xs">
          <FieldLabel className="contents">
            Region
            {selected.regions?.length ? (
              <Select
                disabled={disabled}
                value={value.region}
                onValueChange={(e) => emit({ ...value, region: e ?? "" })}
              >
                <SelectTrigger
                  aria-label={`${label} region`}
                  className={"w-full min-w-0"}
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="">Provider default</SelectItem>
                  {selected.regions.map((r) => (
                    <SelectItem key={r} value={r}>
                      {r}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <Input
                aria-label={`${label} region`}
                disabled={disabled}
                value={value.region}
                onChange={(e) => emit({ ...value, region: e.target.value })}
                placeholder="Provider default"
              />
            )}
          </FieldLabel>
        </FormField>
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
          <DialogTitle>Default for {cloud.name}</DialogTitle>
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
          <Alert variant="destructive">
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={busy || unchanged || !complete}
            onClick={() => onSave({ ...value, region: value.region.trim() })}
          >
            {current ? "Review default switch" : "Set default"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
