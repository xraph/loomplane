import { CloudSelect as Select } from "@/components/cloud-targets/cloud-select"
import { Alert, AlertDescription } from "@/components/ui/alert"
import {
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select"
import { Field as FormField, FieldLabel } from "@/components/ui/field"
import { PlusIcon } from "lucide-react"
import { useState } from "react"
import * as api from "@/client/forge/loomplane/hooks"
import type {
  Connection,
  Profile,
  Target,
} from "@/client/forge/loomplane/types"
import { useMutation, useQuery } from "@/client/hooks"
import { messageOf } from "@/client/errors"
import { CloudGlyph } from "@/components/cloud-glyph"
import { ConnectDialog } from "@/components/cloud-connections"
import { QueryError } from "@/components/query-error"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { cloudDeletionError } from "@/lib/cloud-targets"
import { useOrganization } from "@/lib/organization"
import { cn } from "@/lib/utils"
import { ChangeReview } from "./change-review"
import { TagInput } from "./tag-input"

export function TargetEditor({
  projectId,
  profile,
  target,
  siblingTags = [],
  onClose,
  onSaved,
}: {
  projectId: string
  profile: Profile
  target?: Target
  /** Tags on this Cloud's other targets, offered as shortcuts. */
  siblingTags?: string[]
  onClose: () => void
  onSaved: () => void
}) {
  const { can } = useOrganization()
  const manage = can(
    projectId === "" ? "organization_cloud:manage" : "cloud_account:manage"
  )
  const providers = useQuery(api.useProvidersList)
  const connections = useQuery(api.useConnectionsList)
  const create = useMutation(api.useCloudTargetsCreate)
  const update = useMutation(api.useCloudTargetsUpdate)
  const remove = useMutation(api.useCloudTargetsDelete)
  const createOrg = useMutation(api.useOrganizationCloudTargetsCreate)
  const updateOrg = useMutation(api.useOrganizationCloudTargetsUpdate)
  const removeOrg = useMutation(api.useOrganizationCloudTargetsDelete)
  // An empty project means a Cloud the organization owns.
  const org = projectId === ""
  const [name, setName] = useState(target?.name ?? "")
  const [label, setLabel] = useState(target?.displayName ?? "")
  const [provider, setProvider] = useState(target?.provider ?? "")
  const [connectionId, setConnection] = useState(target?.connectionId ?? "")
  const [runtime, setRuntime] = useState(target?.runtime || "managed")
  const [poolId, setPool] = useState(target?.poolId ?? "")
  const [region, setRegion] = useState(target?.region ?? "")
  const [tags, setTags] = useState<string[]>(target?.tags ?? [])
  const [error, setError] = useState("")
  const [connecting, setConnecting] = useState(false)
  const [review, setReview] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [added, setAdded] = useState<Connection | null>(null)
  const selected = providers.data?.providers.find((p) => p.name === provider)
  const accounts = [
    ...(connections.data?.connections ?? []),
    ...(added && !connections.data?.connections.some((c) => c.id === added.id)
      ? [added]
      : []),
  ].filter((c) => c.status === "ready" && c.cloud === selected?.cloud)
  const changed =
    !!target &&
    (provider !== target.provider ||
      connectionId !== target.connectionId ||
      region !== target.region ||
      runtime !== (target.runtime || "managed") ||
      poolId !== (target.poolId ?? ""))
  // The name and tags save with the target; a placement switch is reviewed.
  const detailsChanged =
    !!target &&
    (label !== target.displayName ||
      tags.join(",") !== [...(target.tags ?? [])].sort().join(","))
  const busy =
    create.isPending ||
    update.isPending ||
    remove.isPending ||
    createOrg.isPending ||
    updateOrg.isPending ||
    removeOrg.isPending
  const save = async () => {
    setError("")
    try {
      const body = {
        name: name.trim(),
        displayName: label.trim() || name.trim(),
        provider,
        connectionId,
        region,
        tags,
        runtime,
        poolId: poolId || undefined,
      }
      const rename = {
        displayName: label.trim(),
        tags,
        revision: target?.revision,
      }
      if (target && org)
        await updateOrg.mutateAsync({
          path: { cloudId: profile.id, targetId: target.id },
          body: rename,
        })
      else if (target)
        await update.mutateAsync({
          path: { id: projectId, cloudId: profile.id, targetId: target.id },
          body: rename,
        })
      else if (org)
        await createOrg.mutateAsync({ path: { cloudId: profile.id }, body })
      else
        await create.mutateAsync({
          path: { id: projectId, cloudId: profile.id },
          body,
        })
      onSaved()
    } catch (err) {
      setError(messageOf(err, "The target wasn't saved."))
    }
  }
  const destroy = async () => {
    setError("")
    try {
      if (org)
        await removeOrg.mutateAsync({
          path: { cloudId: profile.id, targetId: target!.id },
        })
      else
        await remove.mutateAsync({
          path: { id: projectId, cloudId: profile.id, targetId: target!.id },
        })
      onSaved()
    } catch (err) {
      setError(cloudDeletionError(err, "target"))
    }
  }
  return (
    <>
      <Dialog
        open={!review && !connecting}
        onOpenChange={(open) => !open && !busy && onClose()}
      >
        <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>
              {target ? `Edit ${target.name}` : "Add target"}
            </DialogTitle>
            <DialogDescription>
              Targets place services. Resources and gateways keep their own
              placement.
            </DialogDescription>
          </DialogHeader>
          {(providers.status === "error" || connections.status === "error") && (
            <QueryError
              error={providers.error ?? connections.error}
              fallback="Providers and accounts couldn't be loaded."
              retry={() =>
                Promise.allSettled([providers.refetch(), connections.refetch()])
              }
            />
          )}
          <div className="grid gap-3">
            <div className="grid gap-2 sm:grid-cols-2">
              <FormField className="grid gap-1 text-xs">
                <FieldLabel className="contents">
                  Target slug
                  <Input
                    aria-label="Target slug"
                    disabled={!!target || !manage}
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="frontend"
                  />
                </FieldLabel>
              </FormField>
              <FormField className="grid gap-1 text-xs">
                <FieldLabel className="contents">
                  Display name
                  <Input
                    aria-label="Target display name"
                    disabled={!manage || busy}
                    value={label}
                    onChange={(e) => setLabel(e.target.value)}
                  />
                </FieldLabel>
              </FormField>
            </div>
            <div className="grid gap-1">
              <span className="text-xs">Tags</span>
              <TagInput
                tags={tags}
                onChange={setTags}
                suggestions={siblingTags}
                disabled={!manage || busy}
              />
            </div>
            <FormField className="grid gap-1 text-xs">
              <FieldLabel className="contents">
                Placement runtime
                <Select
                  value={runtime}
                  disabled={!manage || busy}
                  onValueChange={(e) => {
                    const value = e ?? ""
                    setRuntime(value)
                    setProvider(value === "server" ? "server" : "")
                    setConnection("")
                    setRegion("")
                    setPool("")
                  }}
                >
                  <SelectTrigger
                    aria-label="Placement runtime"
                    className={"w-full min-w-0"}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="managed">Managed deployment</SelectItem>
                    {(can("organization_cloud:manage") ||
                      runtime === "server") && (
                      <SelectItem value="server">Server pool</SelectItem>
                    )}
                  </SelectContent>
                </Select>
              </FieldLabel>
            </FormField>
            {runtime === "server" ? (
              <PoolChoice
                cloudId={profile.id}
                value={poolId}
                onChange={setPool}
                disabled={!manage || busy}
              />
            ) : (
              <>
                <div className="grid gap-1.5">
                  <span className="text-xs">Provider</span>
                  <div
                    role="radiogroup"
                    aria-label="Target provider"
                    className="grid gap-2 sm:grid-cols-2"
                  >
                    {providers.data?.providers
                      .filter((p) => p.available && p.runs?.length)
                      .map((p) => {
                        const ready = (
                          connections.data?.connections ?? []
                        ).filter(
                          (c) => c.status === "ready" && c.cloud === p.cloud
                        ).length
                        return (
                          <Choice
                            key={p.name}
                            checked={provider === p.name}
                            disabled={!manage || busy}
                            onSelect={() => {
                              if (provider === p.name) return
                              setProvider(p.name)
                              setConnection("")
                              setRegion("")
                            }}
                          >
                            <CloudGlyph cloud={p.cloud} size="sm" />
                            <span className="grid min-w-0 gap-0.5">
                              <span className="truncate text-[13px] font-medium">
                                {p.title}
                              </span>
                              <span className="truncate text-xs text-muted-foreground">
                                {ready
                                  ? `${ready} ${ready === 1 ? "account" : "accounts"} ready`
                                  : "No account yet"}
                              </span>
                            </span>
                          </Choice>
                        )
                      })}
                  </div>
                </div>
                {selected && (
                  <div className="grid gap-1.5">
                    <span className="text-xs">
                      Account
                      <span className="text-muted-foreground">
                        {" "}
                        · any {selected.title} account you&apos;ve connected
                      </span>
                    </span>
                    <div
                      role="radiogroup"
                      aria-label="Desired account"
                      className="grid gap-2"
                    >
                      {accounts.map((c) => (
                        <Choice
                          key={c.id}
                          checked={connectionId === c.id}
                          disabled={!manage || busy}
                          onSelect={() => setConnection(c.id)}
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
                          {target?.connectionId === c.id && (
                            <span className="shrink-0 rounded-md bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
                              Current
                            </span>
                          )}
                        </Choice>
                      ))}
                      {manage && (
                        <Button
                          variant="ghost"
                          type="button"
                          disabled={busy}
                          onClick={() => setConnecting(true)}
                          className="flex h-12 items-center gap-2.5 rounded-xl border border-dashed px-3 text-left text-[13px] text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground disabled:opacity-50"
                        >
                          <PlusIcon className="size-4" />
                          {accounts.length
                            ? `Connect another ${selected.title} account`
                            : `Connect a ${selected.title} account`}
                        </Button>
                      )}
                    </div>
                  </div>
                )}
                <FormField className="grid gap-1 text-xs">
                  <FieldLabel className="contents">
                    Region
                    {selected?.regions?.length ? (
                      <Select
                        disabled={!manage || busy}
                        value={region}
                        onValueChange={(e) => setRegion(e ?? "")}
                      >
                        <SelectTrigger
                          aria-label="Target region"
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
                          {region && !selected.regions.includes(region) && (
                            <SelectItem value={region}>{region}</SelectItem>
                          )}
                        </SelectContent>
                      </Select>
                    ) : (
                      <Input
                        aria-label="Target region"
                        disabled={!manage || busy}
                        value={region}
                        onChange={(e) => setRegion(e.target.value)}
                        placeholder="Provider default"
                      />
                    )}
                  </FieldLabel>
                </FormField>
              </>
            )}
          </div>
          {error && (
            <Alert variant="destructive" className="text-sm text-destructive">
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <DialogFooter className="flex-wrap">
            {target && manage && (
              <Button
                variant="destructive"
                disabled={busy}
                onClick={() => (deleting ? void destroy() : setDeleting(true))}
              >
                {deleting ? "Confirm delete target" : "Delete target"}
              </Button>
            )}
            <Button variant="outline" disabled={busy} onClick={onClose}>
              Cancel
            </Button>
            {manage && (
              <Button
                disabled={
                  busy ||
                  !provider ||
                  (runtime === "server"
                    ? !poolId
                    : !accounts.some((c) => c.id === connectionId)) ||
                  (!target && !/^[a-z][a-z0-9]*(-[a-z0-9]+)*$/.test(name)) ||
                  (changed && (!can("deployment:approve") || detailsChanged))
                }
                onClick={() => (changed ? setReview(true) : void save())}
              >
                {changed ? "Review switch" : "Save target"}
              </Button>
            )}
          </DialogFooter>
          {changed && detailsChanged && (
            <p className="text-xs text-muted-foreground">
              Save the display name and tags before changing placement.
            </p>
          )}
        </DialogContent>
      </Dialog>
      {connecting && (
        <ConnectDialog
          open
          onOpenChange={setConnecting}
          cloud={selected?.cloud}
          onConnected={(c) => {
            if (c.status === "ready" && c.cloud === selected?.cloud) {
              setAdded(c)
              setConnection(c.id)
              setError("")
            } else
              setError(
                "The new account isn't ready for this provider. Check its permissions in Integrations."
              )
          }}
        />
      )}
      {review && target && (
        <ChangeReview
          projectId={projectId}
          title={`Switch ${target.name}`}
          change={{
            cloudId: profile.id,
            targetId: target.id,
            revision: target.revision,
            provider,
            connectionId,
            region,
            runtime,
            poolId,
          }}
          onClose={() => setReview(false)}
          onApplied={onSaved}
        />
      )}
    </>
  )
}

/** Choice is one pickable card in a radio group. */
export function Choice({
  checked,
  disabled,
  onSelect,
  children,
}: {
  checked: boolean
  disabled?: boolean
  onSelect: () => void
  children: React.ReactNode
}) {
  return (
    <Button
      type="button"
      variant="outline"
      role="radio"
      aria-checked={checked}
      disabled={disabled}
      onClick={onSelect}
      className={cn(
        "h-auto min-h-12 min-w-0 justify-start gap-2.5 px-2.5 text-left whitespace-normal",
        checked && "border-primary ring-1 ring-primary"
      )}
    >
      {children}
    </Button>
  )
}

function PoolChoice({
  cloudId,
  value,
  onChange,
  disabled,
}: {
  cloudId: string
  value: string
  onChange: (value: string) => void
  disabled: boolean
}) {
  const pools = useQuery(api.useCloudPoolsList, { path: { cloudId } })
  return (
    <FormField className="grid gap-1 text-xs">
      <FieldLabel className="contents">
        Server pool
        {pools.status === "error" ? (
          <QueryError
            error={pools.error}
            fallback="Pools couldn't be loaded."
            retry={pools.refetch}
          />
        ) : (
          <Select
            disabled={disabled || !pools.data}
            value={value}
            onValueChange={(e) => onChange(e ?? "")}
          >
            <SelectTrigger
              aria-label="Target server pool"
              className={"w-full min-w-0"}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">Choose a server pool</SelectItem>
              {pools.data?.pools.map(
                (pool) =>
                  pool && (
                    <SelectItem key={pool.id} value={pool.id}>
                      {pool.name} · {pool.members.length} servers
                    </SelectItem>
                  )
              )}
            </SelectContent>
          </Select>
        )}
        <span className="text-muted-foreground">
          Services reserve CPU and memory on one eligible server. Existing data
          requires explicit migration.
        </span>
      </FieldLabel>
    </FormField>
  )
}
