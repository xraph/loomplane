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
import { selectClass } from "./workspace"

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
  const manage = can("cloud_account:manage")
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
      region !== target.region)
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
              <label className="grid gap-1 text-xs">
                Target slug
                <Input
                  aria-label="Target slug"
                  disabled={!!target || !manage}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="frontend"
                />
              </label>
              <label className="grid gap-1 text-xs">
                Display name
                <Input
                  aria-label="Target display name"
                  disabled={!manage || busy}
                  value={label}
                  onChange={(e) => setLabel(e.target.value)}
                />
              </label>
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
                    const ready = (connections.data?.connections ?? []).filter(
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
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => setConnecting(true)}
                      className="flex h-12 items-center gap-2.5 rounded-xl border border-dashed px-3 text-left text-[13px] text-muted-foreground transition-colors hover:border-foreground/30 hover:text-foreground disabled:opacity-50"
                    >
                      <PlusIcon className="size-4" />
                      {accounts.length
                        ? `Connect another ${selected.title} account`
                        : `Connect a ${selected.title} account`}
                    </button>
                  )}
                </div>
              </div>
            )}
            <label className="grid gap-1 text-xs">
              Region
              {selected?.regions?.length ? (
                <select
                  className={selectClass}
                  aria-label="Target region"
                  disabled={!manage || busy}
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                >
                  <option value="">Provider default</option>
                  {selected.regions.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                  {region && !selected.regions.includes(region) && (
                    <option value={region}>{region}</option>
                  )}
                </select>
              ) : (
                <Input
                  aria-label="Target region"
                  disabled={!manage || busy}
                  value={region}
                  onChange={(e) => setRegion(e.target.value)}
                  placeholder="Provider default"
                />
              )}
            </label>
          </div>
          {error && (
            <p role="alert" className="text-sm text-destructive">
              {error}
            </p>
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
                  !accounts.some((c) => c.id === connectionId) ||
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
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      disabled={disabled}
      onClick={onSelect}
      className={cn(
        "flex h-12 min-w-0 items-center gap-2.5 rounded-xl bg-card px-2.5 text-left ring-1 transition-shadow disabled:opacity-60",
        checked ? "ring-2 ring-primary" : "ring-border hover:ring-foreground/25"
      )}
    >
      {children}
    </button>
  )
}
